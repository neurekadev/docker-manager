package app

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// API token (#31) test helpers: tokens are created through the real API
// by a signed-in user and used by a cookie-less client sending
// Authorization: Bearer, like curl.

type apiTokenBody struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Status   string `json:"status"`
	Scopes   []struct {
		Capability string `json:"capability"`
		Scope      struct {
			Kind          string `json:"kind"`
			EnvironmentID string `json:"environmentId"`
			ResourceType  string `json:"resourceType"`
			ResourceID    string `json:"resourceId"`
		} `json:"scope"`
	} `json:"scopes"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastUsedAt    *time.Time `json:"lastUsedAt"`
	LastUsedIP    string     `json:"lastUsedIp"`
	RevokedAt     *time.Time `json:"revokedAt"`
	RevokedReason string     `json:"revokedReason"`
}

// tokenGrants converts allow-rule shorthand to token grants.
func tokenGrants(t *testing.T, shorthand ...string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, r := range apiRules(t, shorthand...) {
		if r["effect"] != "allow" {
			t.Fatalf("token grants are allow rules: %v", r)
		}
		delete(r, "effect")
		out = append(out, r)
	}
	return out
}

// tokenRequest is the body of POST /me/api-tokens with a 30-day expiry.
func (c *client) tokenRequest(name string, grants ...string) map[string]any {
	return map[string]any{"name": name, "expiresAt": c.e.clk.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
		"scopes": tokenGrants(c.e.t, grants...)}
}

// createToken creates a token through the API and registers its value as
// a canary: it must never appear in any later response, log or audit record.
func (c *client) createToken(name string, grants ...string) (apiTokenBody, string) {
	c.e.t.Helper()
	return c.createTokenWith(c.tokenRequest(name, grants...))
}

func (c *client) createTokenWith(body map[string]any, opts ...reqOpt) (apiTokenBody, string) {
	t := c.e.t
	t.Helper()
	var out struct {
		APIToken apiTokenBody `json:"apiToken"`
		Token    string       `json:"token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/me/api-tokens", body, append(opts, secretOK)...).json(t, &out)
	if !regexp.MustCompile(`^dy_` + regexp.QuoteMeta(out.APIToken.ID) + `_[A-Za-z0-9_-]{43}$`).MatchString(out.Token) {
		t.Fatalf("token value format: %q (id %s)", out.Token, out.APIToken.ID)
	}
	c.e.secrets.Register(canary.APIToken, "api token "+out.APIToken.ID, out.Token)
	return out.APIToken, out.Token
}

func bearer(token string) reqOpt { return header("Authorization", "Bearer "+token) }

// bot is a non-browser API client (no cookie) of the given token.
type bot struct {
	c     *client
	token string
}

func (e *env) bot(token string) *bot { return &bot{c: e.client(), token: token} }

func (b *bot) do(method, path string, body any, opts ...reqOpt) response {
	b.c.e.t.Helper()
	return b.c.do(method, path, body, append([]reqOpt{bearer(b.token)}, opts...)...)
}

func (b *bot) must(status int, method, path string, body any, opts ...reqOpt) response {
	b.c.e.t.Helper()
	return b.c.must(status, method, path, body, append([]reqOpt{bearer(b.token)}, opts...)...)
}

func (b *bot) fail(status int, code, method, path string, body any, opts ...reqOpt) response {
	b.c.e.t.Helper()
	return b.c.fail(status, code, method, path, body, append([]reqOpt{bearer(b.token)}, opts...)...)
}

// refused asserts the generic 401 of a token that does not authenticate.
func (b *bot) refused(what string) {
	t := b.c.e.t
	t.Helper()
	r := b.do(http.MethodGet, "/api/v1/me", nil)
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	r.json(t, &body)
	if r.status != http.StatusUnauthorized || body.Code != "unauthenticated" || body.Message != "the API token is not valid" ||
		r.header.Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("%s: %d %s, want the generic 401", what, r.status, r.body)
	}
}

// openTokenStream opens an SSE stream with a bearer token and returns a
// channel receiving the rest of the stream once the server ends it.
func (e *env) openTokenStream(path, token string) <-chan string {
	t := e.t
	t.Helper()
	req, err := http.NewRequestWithContext(testutil.Context(t), http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", publicHost)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("token stream %s: %d %s", path, resp.StatusCode, b)
	}
	out := make(chan string, 1)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		out <- string(b)
	}()
	return out
}

func waitStreamEnd(t *testing.T, ch <-chan string, what, want string) {
	t.Helper()
	select {
	case body := <-ch:
		if !strings.Contains(body, want) {
			t.Fatalf("%s: stream ended with %q, want %q", what, body, want)
		}
	case <-testutil.Context(t).Done():
		t.Fatalf("%s: stream still open", what)
	}
}

func (c *client) myToken(id string) apiTokenBody {
	c.e.t.Helper()
	var out apiTokenBody
	c.must(http.StatusOK, http.MethodGet, "/api/v1/me/api-tokens/"+id, nil).json(c.e.t, &out)
	return out
}

// auditRow is one stored audit record.
type auditRow struct {
	Action     string `bun:"action"`
	Operation  string `bun:"operation_id"`
	ActorKind  string `bun:"actor_kind"`
	ActorUser  string `bun:"actor_user_id"`
	ActorToken string `bun:"actor_token_id"`
	Outcome    string `bun:"outcome"`
	ErrorClass string `bun:"error_class"`
	Targets    string `bun:"targets"`
	Details    string `bun:"details"`
	ClientIP   string `bun:"client_ip"`
}

func (e *env) auditRows() []auditRow {
	e.t.Helper()
	var rows []auditRow
	if err := e.m.DB().NewRaw(`SELECT action, operation_id, actor_kind, actor_user_id, actor_token_id, outcome, error_class, targets, details, client_ip
		FROM audit_events ORDER BY seq`).Scan(testutil.Context(e.t), &rows); err != nil {
		e.t.Fatal(err)
	}
	return rows
}

// tableDump renders every row of the given tables for canary scans.
func (e *env) tableDump(tables ...string) string {
	e.t.Helper()
	var sb strings.Builder
	for _, tbl := range tables {
		rows, err := e.m.DB().QueryContext(testutil.Context(e.t), "SELECT * FROM "+tbl) //nolint:gosec // fixed table names
		if err != nil {
			e.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				e.t.Fatal(err)
			}
			m := map[string]any{}
			for i, c := range cols {
				if b, ok := vals[i].([]byte); ok {
					m[c] = string(b)
				} else {
					m[c] = vals[i]
				}
			}
			b, _ := json.Marshal(m)
			sb.Write(b)
			sb.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			e.t.Fatal(err)
		}
		_ = rows.Close()
	}
	return sb.String()
}
