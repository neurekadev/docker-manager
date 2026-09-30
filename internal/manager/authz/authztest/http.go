package authztest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/neurekadev/docker-manager/internal/manager/authz"
)

// Request headers read by Authenticate.
const (
	UserHeader  = "X-Authztest-User"
	TokenHeader = "X-Authztest-Token" //nolint:gosec // G101: a header name, not a credential
)

// Authenticate sets the request principal from UserHeader (a user) or
// UserHeader + TokenHeader (an API token of that user), like the identity
// middleware does for real sessions. Requests without the header stay
// anonymous (401 from the route).
func Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get(UserHeader)
		if user == "" {
			next.ServeHTTP(w, r)
			return
		}
		p := authz.Principal{Kind: authz.KindUser, UserID: user}
		if tok := r.Header.Get(TokenHeader); tok != "" {
			p = authz.Principal{Kind: authz.KindAPIToken, UserID: user, TokenID: tok}
		}
		ctx, err := authz.WithPrincipal(r.Context(), p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Call is one API request.
type Call struct {
	Method string
	Path   string
	// Body is marshaled as JSON when set.
	Body any
	// OperationID, Capability and CapabilityValues are the route's
	// declared metadata (from Routes); informational for hand-made calls.
	OperationID      string
	Capability       string
	CapabilityValues []string
	// Headers are extra request headers (If-Match, Idempotency-Key, ...).
	Headers map[string]string
	// List routes filter per item: denied means 403/404 or an empty page.
	List bool
}

func (c Call) String() string {
	if c.OperationID != "" {
		return fmt.Sprintf("%s %s (%s, %s)", c.Method, c.Path, c.OperationID, c.Capability)
	}
	return c.Method + " " + c.Path
}

// Response is the status, headers and (for non-stream responses) body.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do sends call as user (empty: anonymous) through h. Streams are cut
// after the response headers arrive, so allowed SSE routes do not block.
func Do(t testing.TB, h http.Handler, user string, call Call) Response {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	var body io.Reader
	if call.Body != nil {
		b, err := json.Marshal(call.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, call.Method, srv.URL+call.Path, body)
	if err != nil {
		t.Fatal(err)
	}
	if call.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set(UserHeader, user)
	}
	for k, v := range call.Headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s: %v", call, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := Response{Status: resp.StatusCode, Header: resp.Header}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") && !strings.HasPrefix(ct, "application/octet-stream") {
		out.Body, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	}
	return out
}

// Denied reports whether a response is an authorization refusal: 403, or
// 404 for resources the caller may not know about. 401 is not a refusal
// of a grant (the caller is anonymous).
func Denied(status int) bool { return status == http.StatusForbidden || status == http.StatusNotFound }

// AssertOnly requires every allowed call to pass authorization (any status
// but 401, 403 and 404; the route may still answer 409, 422, 503, ...) and
// every denied call to be refused with 403 or 404 — or, for list calls, to
// answer an empty page — for user.
func AssertOnly(t testing.TB, h http.Handler, user string, allowed, denied []Call) {
	t.Helper()
	for _, c := range allowed {
		r := Do(t, h, user, c)
		if r.Status == http.StatusUnauthorized || Denied(r.Status) {
			t.Errorf("%s as %s: %d %s, want the grant to pass authorization", c, user, r.Status, r.Body)
		}
	}
	for _, c := range denied {
		r := Do(t, h, user, c)
		if Denied(r.Status) || (c.List && r.Status == http.StatusOK && EmptyPage(r.Body)) {
			continue
		}
		t.Errorf("%s as %s: %d %s, want 403 or 404 (or an empty page)", c, user, r.Status, r.Body)
	}
}

// EmptyPage reports whether body is a page ({"items": [...]}) without items.
func EmptyPage(body []byte) bool {
	var p struct {
		Items []json.RawMessage `json:"items"`
	}
	return json.Unmarshal(body, &p) == nil && p.Items != nil && len(p.Items) == 0
}

// Discoverable moves the calls with these operation IDs from denied to
// allowed: routes that show a resource's minimal view to any capability on
// it (get/list of the resource type), so a grant for another action makes
// them answer 200 with the minimal view.
func Discoverable(allowed, denied []Call, operationIDs ...string) ([]Call, []Call) {
	var keep []Call
	for _, c := range denied {
		if slices.Contains(operationIDs, c.OperationID) {
			allowed = append(allowed, c)
		} else {
			keep = append(keep, c)
		}
	}
	return allowed, keep
}

// AssertAbsent fails when body contains any of the values (fields of a
// minimal view must not leak, e.g. an Engine ID or a label).
func AssertAbsent(t testing.TB, what string, body []byte, values ...string) {
	t.Helper()
	for _, v := range values {
		if v != "" && bytes.Contains(body, []byte(v)) {
			t.Errorf("%s leaks %q: %s", what, v, body)
		}
	}
}

// Split partitions calls into those whose declared capability (or one of
// its selector values) is granted and the rest.
func Split(calls []Call, granted ...string) (allowed, denied []Call) {
	for _, c := range calls {
		ok := slices.Contains(granted, c.Capability)
		for _, v := range c.CapabilityValues {
			ok = ok || slices.Contains(granted, v)
		}
		if ok {
			allowed = append(allowed, c)
		} else {
			denied = append(denied, c)
		}
	}
	return allowed, denied
}

type inventoryFile struct {
	Routes []struct {
		Method           string   `yaml:"method"`
		Path             string   `yaml:"path"`
		OperationID      string   `yaml:"operationId"`
		Capability       string   `yaml:"capability"`
		CapabilityValues []string `yaml:"capabilityValues"`
		Status           string   `yaml:"status"`
	} `yaml:"routes"`
}

var paramRE = regexp.MustCompile(`\{([A-Za-z0-9]+)\}`)

// Routes returns one Call per implemented route in api/route-inventory.yaml
// whose path starts with one of prefixes (all routes without prefixes),
// with path parameters taken from params. Routes naming a parameter missing
// from params are skipped. Public and authenticated routes are skipped:
// they need no grant.
func Routes(t testing.TB, params map[string]string, prefixes ...string) []Call {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "api", "route-inventory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var inv inventoryFile
	if err := yaml.Unmarshal(b, &inv); err != nil {
		t.Fatal(err)
	}
	var out []Call
	for _, r := range inv.Routes {
		if r.Status != "implemented" || r.Capability == "public" || r.Capability == "authenticated" {
			continue
		}
		if len(prefixes) > 0 && !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(r.Path, p) }) {
			continue
		}
		path, missing := r.Path, false
		for _, m := range paramRE.FindAllStringSubmatch(r.Path, -1) {
			v, ok := params[m[1]]
			if !ok {
				missing = true
				break
			}
			path = strings.ReplaceAll(path, m[0], v)
		}
		if missing {
			continue
		}
		c := Call{Method: r.Method, Path: path, OperationID: r.OperationID, Capability: r.Capability, CapabilityValues: r.CapabilityValues,
			List: strings.HasPrefix(r.OperationID, "list-")}
		if r.Method != http.MethodGet && r.Method != http.MethodDelete {
			c.Body = map[string]any{}
		}
		out = append(out, c)
	}
	return out
}

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("authztest: repository root (go.mod) not found")
		}
		dir = parent
	}
}
