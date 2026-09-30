package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/logging"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not *Error", err)
	}
	return e
}

func TestCheckIfMatch(t *testing.T) {
	cur := RevisionETag(7)
	if cur != `"7"` || ETag("abc") != `"abc"` {
		t.Fatalf("etag formatting: %s %s", cur, ETag("abc"))
	}
	ok := []struct{ header, current string }{
		{`"7"`, cur},
		{` "3", "7" `, cur},
		{`*`, cur},
	}
	for _, c := range ok {
		if err := CheckIfMatch(c.header, c.current, true); err != nil {
			t.Errorf("If-Match %q vs %s: %v", c.header, c.current, err)
		}
	}
	if err := CheckIfMatch("", cur, false); err != nil {
		t.Errorf("optional missing header: %v", err)
	}
	e := asError(t, CheckIfMatch("", cur, true))
	if e.GetStatus() != http.StatusPreconditionRequired || e.Code != CodePreconditionRequired || e.Details[0].Field != "header.If-Match" {
		t.Fatalf("missing required: %+v", e)
	}
	for _, h := range []string{`"6"`, `W/"7"`, `7`, `"7`} {
		e := asError(t, CheckIfMatch(h, cur, true))
		if e.GetStatus() != http.StatusPreconditionFailed || e.Code != CodePreconditionFailed || e.GetHeaders().Get("ETag") != cur {
			t.Errorf("If-Match %q: %+v headers %v", h, e, e.GetHeaders())
		}
	}
	// "*" never matches a missing resource, and no ETag is disclosed.
	e = asError(t, CheckIfMatch("*", "", true))
	if e.GetStatus() != http.StatusPreconditionFailed || e.GetHeaders().Get("ETag") != "" {
		t.Fatalf("* on missing resource: %+v", e)
	}
}

type editInput struct {
	ID string `path:"id"`
	IfMatchParam
	Body struct {
		Name string `json:"name"`
	}
}

type editOutput struct {
	ETagHeader
	Body struct {
		Name     string `json:"name"`
		Revision int64  `json:"revision"`
	}
}

// TestIfMatchOverHTTP drives a revisioned edit through Huma: the 412 keeps
// the one error shape and carries the current ETag header.
func TestIfMatchOverHTTP(t *testing.T) {
	var mu sync.Mutex
	rev := int64(1)
	mux := http.NewServeMux()
	a := New(mux, Deps{})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "test-edit", Method: http.MethodPatch, Path: BasePath + "/test/things/{id}", Summary: "edit", Errors: []int{http.StatusPreconditionFailed, http.StatusPreconditionRequired}},
		Capability: "thing.write", Scope: ScopeResource,
	}, func(_ context.Context, in *editInput) (*editOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := in.CheckIfMatch(RevisionETag(rev)); err != nil {
			return nil, err
		}
		rev++
		out := &editOutput{}
		out.ETag = RevisionETag(rev)
		out.Body.Name, out.Body.Revision = in.Body.Name, rev
		return out, nil
	})
	h := withTestContext(t, mux, "")
	send := func(ifMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, BasePath+"/test/things/x", strings.NewReader(`{"name":"n"}`))
		req.Header.Set("Content-Type", "application/json")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	decodeError(t, send(""), http.StatusPreconditionRequired, CodePreconditionRequired)
	rec := send(`"1"`)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("edit: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	rec = send(`"1"`) // stale
	decodeError(t, rec, http.StatusPreconditionFailed, CodePreconditionFailed)
	if rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("412 without current ETag: %v", rec.Header())
	}
}

// withTestContext adds the request ID, a logger and (when user != "") a
// principal, like the server middleware and #16 authentication would.
func withTestContext(t *testing.T, next http.Handler, user string) http.Handler {
	t.Helper()
	logger := testutil.Logger(t)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := logging.WithRequestID(r.Context(), "req-123")
		ctx = logging.IntoContext(ctx, logger)
		u := user
		if hu := r.Header.Get("X-Test-User"); hu != "" {
			u = hu
		}
		if u != "" {
			var err error
			if ctx, err = authz.WithPrincipal(ctx, authz.Principal{Kind: authz.KindUser, UserID: u}); err != nil {
				t.Error(err)
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestParseSort(t *testing.T) {
	allowed := []string{"name", "createdAt"}
	got, err := ParseSort("-createdAt,name", allowed)
	if err != nil || len(got) != 2 || got[0] != (SortKey{"createdAt", true}) || got[1].String() != "name" || got[0].String() != "-createdAt" {
		t.Fatalf("ParseSort = %v, %v", got, err)
	}
	def, err := ParseSort("", allowed, SortKey{"createdAt", true})
	if err != nil || len(def) != 1 || !def[0].Desc {
		t.Fatalf("default = %v %v", def, err)
	}
	for _, bad := range []string{"size", "name,-name", "-"} {
		if _, err := ParseSort(bad, allowed); asError(t, err).Details[0].Field != "query.sort" {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestCursorBoundToQuery(t *testing.T) {
	fp := QueryFingerprint("running", "stack.deploy")
	if fp == QueryFingerprint("running", "stack.deplo", "y") || fp == QueryFingerprint("runningstack.deploy") {
		t.Fatal("fingerprint parts are not separated")
	}
	c, err := CursorFor(fp, map[string]string{"after": "id-9"})
	if err != nil {
		t.Fatal(err)
	}
	var pos map[string]string
	if err := DecodeCursorFor(c, fp, &pos); err != nil || pos["after"] != "id-9" {
		t.Fatalf("round trip %v %v", pos, err)
	}
	for _, bad := range []struct{ cursor, fp string }{{c, QueryFingerprint("failed")}, {"!!", fp}, {mustCursor(t, 42), fp}} {
		if e := asError(t, DecodeCursorFor(bad.cursor, bad.fp, &pos)); e.GetStatus() != http.StatusUnprocessableEntity || e.Details[0].Field != "query.cursor" {
			t.Errorf("cursor %q accepted: %+v", bad.cursor, e)
		}
	}
	if (PageParams{}).PageLimit() != DefaultPageLimit || (PageParams{Limit: 999}).PageLimit() != MaxPageLimit || (PageParams{Limit: 3}).PageLimit() != 3 {
		t.Fatal("PageLimit")
	}
	if n := Total(3); *n != 3 {
		t.Fatal("Total")
	}
}

func mustCursor(t *testing.T, v any) string {
	c, err := EncodeCursor(v)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fakeRows serves records 1..n in order.
func fakeRows(n int) (fetch func(context.Context, string, int) ([]int, error), calls *int) {
	calls = new(int)
	return func(_ context.Context, after string, limit int) ([]int, error) {
		*calls++
		start := 0
		if after != "" {
			_, _ = fmt.Sscan(after, &start)
		}
		var out []int
		for i := start + 1; i <= n && len(out) < limit; i++ {
			out = append(out, i)
		}
		return out, nil
	}, calls
}

func pos(i int) string { return fmt.Sprint(i) }

func TestScanPage(t *testing.T) {
	ctx := context.Background()
	fetch, _ := fakeRows(10)
	even := func(i int) bool { return i%2 == 0 }
	var all []int
	after := ""
	for range 10 {
		items, next, err := ScanPage(ctx, Scan[int]{Limit: 2, After: after, Fetch: fetch, Position: pos, Visible: even})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, items...)
		if next == "" {
			break
		}
		after = next
	}
	if fmt.Sprint(all) != "[2 4 6 8 10]" {
		t.Fatalf("paged %v", all)
	}

	// A caller who sees almost nothing gets short pages bounded by MaxScans
	// but can still reach every visible item.
	fetch, calls := fakeRows(100)
	only99 := func(i int) bool { return i == 99 }
	items, next, _ := ScanPage(ctx, Scan[int]{Limit: 5, Fetch: fetch, Position: pos, Visible: only99, MaxScans: 3})
	if len(items) != 0 || next != "18" || *calls != 3 {
		t.Fatalf("budget: items %v next %q calls %d", items, next, *calls)
	}
	var found []int
	after = next
	for after != "" {
		items, after, _ = ScanPage(ctx, Scan[int]{Limit: 5, After: after, Fetch: fetch, Position: pos, Visible: only99, MaxScans: 3})
		found = append(found, items...)
	}
	if fmt.Sprint(found) != "[99]" {
		t.Fatalf("found %v", found)
	}
	// Errors propagate.
	boom := errors.New("db")
	if _, _, err := ScanPage(ctx, Scan[int]{Fetch: func(context.Context, string, int) ([]int, error) { return nil, boom }, Position: pos, Visible: even}); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestRegisterContractRules(t *testing.T) {
	noop := func(context.Context, *struct{}) (*healthOutput, error) { return nil, nil }
	base := huma.Operation{OperationID: "do-thing", Method: http.MethodPost, Path: BasePath + "/thing", Summary: "thing"}
	mustPanic := func(name string, f func(a huma.API)) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: Register did not panic", name)
			}
		}()
		f(New(http.NewServeMux(), Deps{}))
	}
	cases := map[string]Operation{
		"owner needs instance":     {Operation: base, Capability: CapabilityOwner, Scope: ScopeResource},
		"selector without values":  {Operation: base, Capability: "stack.{action}", Scope: ScopeResource},
		"selector one value":       {Operation: base, Capability: "stack.{action}", CapabilityValues: []Capability{"stack.start"}, Scope: ScopeResource},
		"selector foreign value":   {Operation: base, Capability: "stack.{action}", CapabilityValues: []Capability{"stack.start", "container.stop"}, Scope: ScopeResource},
		"selector duplicate value": {Operation: base, Capability: "stack.{action}", CapabilityValues: []Capability{"stack.start", "stack.start"}, Scope: ScopeResource},
		"values without selector":  {Operation: base, Capability: "stack.start", CapabilityValues: []Capability{"stack.start", "stack.stop"}, Scope: ScopeResource},
		"unknown idempotency":      {Operation: base, Capability: "stack.start", Scope: ScopeResource, Idempotency: "maybe"},
		"public idempotency":       {Operation: base, Capability: CapabilityPublic, Scope: ScopeNone, Idempotency: IdempotencyStored},
		"mode without key param":   {Operation: base, Capability: "stack.start", Scope: ScopeResource, Idempotency: IdempotencyJob},
		"202 without JobAccepted":  {Operation: withStatus202(base), Capability: "stack.start", Scope: ScopeResource},
	}
	for name, op := range cases {
		mustPanic(name, func(a huma.API) { Register(a, op, noop) })
	}
	mustPanic("key param without mode", func(a huma.API) {
		Register(a, Operation{Operation: base, Capability: "stack.start", Scope: ScopeResource},
			func(context.Context, *struct{ IdempotencyKeyParam }) (*healthOutput, error) { return nil, nil })
	})
	mustPanic("JobAccepted with 200", func(a huma.API) {
		op := base
		op.DefaultStatus = http.StatusOK
		Register(a, Operation{Operation: op, Capability: "stack.start", Scope: ScopeResource},
			func(context.Context, *struct{}) (*JobAccepted, error) { return nil, nil })
	})

	// Valid declarations: selector, owner, JobAccepted defaults to 202 and
	// gets Location, key params with a mode, security on non-public ops.
	a := New(http.NewServeMux(), Deps{})
	Register(a, Operation{Operation: base, Capability: "stack.{action}", CapabilityValues: []Capability{"stack.start", "stack.stop"}, Scope: ScopeResource, Idempotency: IdempotencyJob},
		func(context.Context, *struct{ IdempotencyKeyParam }) (*JobAccepted, error) { return nil, nil })
	Register(a, Operation{Operation: huma.Operation{OperationID: "owner-thing", Method: http.MethodGet, Path: BasePath + "/owner", Summary: "o"}, Capability: CapabilityOwner, Scope: ScopeInstance}, noop)
	op := a.OpenAPI().Paths[BasePath+"/thing"].Post
	if op.Extensions[ExtIdempotency] != "job" || fmt.Sprint(op.Extensions[ExtCapabilityValues]) != "[stack.start stack.stop]" {
		t.Fatalf("extensions %v", op.Extensions)
	}
	r202 := op.Responses["202"]
	if r202 == nil || r202.Headers["Location"] == nil {
		t.Fatalf("202 response %+v", op.Responses)
	}
	if len(op.Security) != 2 || op.Responses["401"] == nil {
		t.Fatalf("security %v responses %v", op.Security, op.Responses)
	}
	if h := a.OpenAPI().Paths[BasePath+"/health"].Get; len(h.Security) != 0 {
		t.Fatalf("public operation has security %v", h.Security)
	}
	if IsCapabilityKey("stack.{action}") || !IsCapabilityKey("stack.start") || IsCapabilityKey("owner") {
		t.Fatal("IsCapabilityKey")
	}
}

func withStatus202(op huma.Operation) huma.Operation {
	op.DefaultStatus = http.StatusAccepted
	return op
}

// memIdempotency is an in-memory IdempotencyStore for tests.
type memIdempotency struct {
	mu   sync.Mutex
	recs map[string]*memRec
}

type memRec struct {
	hash string
	resp *domain.IdempotentResponse
}

func (m *memIdempotency) k(r domain.IdempotencyReservation) string { return r.Scope + "\x00" + r.Key }

func (m *memIdempotency) Begin(_ context.Context, r domain.IdempotencyReservation) (*domain.IdempotentResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recs == nil {
		m.recs = map[string]*memRec{}
	}
	rec, ok := m.recs[m.k(r)]
	switch {
	case !ok:
		m.recs[m.k(r)] = &memRec{hash: r.RequestHash}
		return nil, nil
	case rec.hash != r.RequestHash:
		return nil, domain.ErrIdempotencyMismatch
	case rec.resp == nil:
		return nil, domain.ErrIdempotencyInFlight
	}
	return rec.resp, nil
}

func (m *memIdempotency) Complete(_ context.Context, r domain.IdempotencyReservation, resp domain.IdempotentResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[m.k(r)].resp = &resp
	return nil
}

func (m *memIdempotency) Release(_ context.Context, r domain.IdempotencyReservation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recs, m.k(r))
	return nil
}

type createTokenInput struct {
	IdempotencyKeyParam
	Body struct {
		Name string `json:"name" minLength:"1"`
	}
}

type createTokenOutput struct {
	Location string `header:"Location"`
	Body     struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
}

func newIdempotencyAPI(t *testing.T, store IdempotencyStore) (http.Handler, *int) {
	t.Helper()
	calls := new(int)
	var mu sync.Mutex
	mux := http.NewServeMux()
	a := New(mux, Deps{Idempotency: store})
	Register(a, Operation{
		Operation: huma.Operation{OperationID: "create-test-token", Method: http.MethodPost, Path: BasePath + "/test/tokens", Summary: "create",
			DefaultStatus: http.StatusCreated, Errors: []int{http.StatusConflict}},
		Capability: "api_tokens.create", Scope: ScopeInstance, Idempotency: IdempotencyStored,
	}, func(_ context.Context, in *createTokenInput) (*createTokenOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		*calls++
		if in.Body.Name == "taken" {
			return nil, Conflict("name_taken", "taken")
		}
		out := &createTokenOutput{Location: BasePath + "/test/tokens/" + fmt.Sprint(*calls)}
		out.Body.ID, out.Body.Secret = fmt.Sprint(*calls), fmt.Sprintf("secret-%d", *calls)
		return out, nil
	})
	return withTestContext(t, mux, ""), calls
}

func postToken(h http.Handler, user, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, BasePath+"/test/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIdempotencyStoredReplay(t *testing.T) {
	store := &memIdempotency{}
	h, calls := newIdempotencyAPI(t, store)
	first := postToken(h, "alice", "k-1", `{"name":"ci"}`)
	if first.Code != http.StatusCreated || first.Header().Get(HeaderIdempotentReplayed) != "" {
		t.Fatalf("first: %d %v %s", first.Code, first.Header(), first.Body)
	}
	again := postToken(h, "alice", "k-1", `{"name":"ci"}`)
	if again.Code != http.StatusCreated || again.Body.String() != first.Body.String() ||
		again.Header().Get("Location") != first.Header().Get("Location") ||
		again.Header().Get(HeaderIdempotentReplayed) != "true" || *calls != 1 {
		t.Fatalf("replay: %d %v %s (calls %d)", again.Code, again.Header(), again.Body, *calls)
	}
	// Same key, different body: 409 idempotency_key_reused, handler not run.
	decodeError(t, postToken(h, "alice", "k-1", `{"name":"other"}`), http.StatusConflict, CodeIdempotencyKeyReused)
	// Keys are scoped per principal: bob's k-1 is a new request.
	if rec := postToken(h, "bob", "k-1", `{"name":"ci"}`); rec.Code != http.StatusCreated || *calls != 2 {
		t.Fatalf("bob: %d calls %d", rec.Code, *calls)
	}
	// No key: never deduplicated.
	postToken(h, "alice", "", `{"name":"ci"}`)
	postToken(h, "alice", "", `{"name":"ci"}`)
	if *calls != 4 {
		t.Fatalf("keyless calls %d", *calls)
	}
}

func TestIdempotencyFailuresAreNotStored(t *testing.T) {
	store := &memIdempotency{}
	h, calls := newIdempotencyAPI(t, store)
	decodeError(t, postToken(h, "alice", "k-2", `{"name":"taken"}`), http.StatusConflict, "name_taken")
	decodeError(t, postToken(h, "alice", "k-2", `{"name":"taken"}`), http.StatusConflict, "name_taken")
	if *calls != 2 {
		t.Fatalf("a failed request must run again on retry, calls %d", *calls)
	}
	// Validation failures never reach the handler and leave no reservation.
	decodeError(t, postToken(h, "alice", "k-3", `{"name":""}`), http.StatusUnprocessableEntity, CodeValidationFailed)
	if rec := postToken(h, "alice", "k-3", `{"name":"ok"}`); rec.Code != http.StatusCreated {
		t.Fatalf("key after validation failure: %d %s", rec.Code, rec.Body)
	}
	// Malformed keys are a 422 from the schema.
	decodeError(t, postToken(h, "alice", "bad key", `{"name":"ok"}`), http.StatusUnprocessableEntity, CodeValidationFailed)
	// Unauthenticated requests bypass the store (the handler answers).
	if rec := postToken(h, "", "k-4", `{"name":"ok"}`); rec.Code != http.StatusCreated {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	if _, ok := store.recs["user:alice create-test-token\x00k-2"]; ok {
		t.Fatal("failed request left a reservation")
	}
}

func TestIdempotencyInFlightAndUnavailable(t *testing.T) {
	store := &memIdempotency{}
	h, calls := newIdempotencyAPI(t, store)
	body := `{"name":"ci"}`
	// Reserve the key as if a first request were still running.
	req := httptest.NewRequest(http.MethodPost, BasePath+"/test/tokens", nil)
	r := domain.IdempotencyReservation{Scope: "user:alice create-test-token", Key: "k-5",
		RequestHash: RequestFingerprint(http.MethodPost, req.URL.Path, "", "application/json", []byte(body))}
	if _, err := store.Begin(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	rec := postToken(h, "alice", "k-5", body)
	e := decodeError(t, rec, http.StatusConflict, CodeIdempotencyKeyInFlight)
	if !e.Retryable || rec.Header().Get("Retry-After") != "1" || *calls != 0 {
		t.Fatalf("in flight: %+v %v calls %d", e, rec.Header(), *calls)
	}
	// Without a store, keyed requests fail closed; keyless ones still work.
	h2, _ := newIdempotencyAPI(t, nil)
	decodeError(t, postToken(h2, "alice", "k-6", body), http.StatusServiceUnavailable, CodeUnavailable)
	if rec := postToken(h2, "alice", "", body); rec.Code != http.StatusCreated {
		t.Fatalf("keyless without store: %d", rec.Code)
	}
}

func TestRequestFingerprint(t *testing.T) {
	a := RequestFingerprint("POST", "/p", "b=2&a=1", "application/json", []byte("x"))
	if a != RequestFingerprint("POST", "/p", "a=1&b=2", "application/json", []byte("x")) {
		t.Fatal("query order changes the fingerprint")
	}
	for _, other := range []string{
		RequestFingerprint("PUT", "/p", "a=1&b=2", "application/json", []byte("x")),
		RequestFingerprint("POST", "/q", "a=1&b=2", "application/json", []byte("x")),
		RequestFingerprint("POST", "/p", "a=1", "application/json", []byte("x")),
		RequestFingerprint("POST", "/p", "a=1&b=2", "text/plain", []byte("x")),
		RequestFingerprint("POST", "/p", "a=1&b=2", "application/json", []byte("y")),
	} {
		if other == a {
			t.Fatal("fingerprint collision")
		}
	}
}

func TestSSEWriterRejectsLineBreaks(t *testing.T) {
	var b strings.Builder
	s := &SSEWriter{w: &b}
	if err := s.Event("x\ny", "", 1); err == nil {
		t.Fatal("newline in event accepted")
	}
	if err := s.Event("ok", "1\r", 1); err == nil {
		t.Fatal("CR in id accepted")
	}
	if err := s.Event("ok", "7", map[string]string{"m": "a\nb"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Retry(3000)
	_ = s.Heartbeat()
	if b.String() != "id: 7\nevent: ok\ndata: {\"m\":\"a\\nb\"}\n\nretry: 3000\n\n: heartbeat\n\n" {
		t.Fatalf("framing %q", b.String())
	}
}

func TestErrorHeadersOutsideHuma(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	WriteError(rec, req, RateLimited("slow down").WithHeader("Retry-After", "5"))
	var e Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || rec.Header().Get("Retry-After") != "5" || rec.Code != 429 || !e.Retryable {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}
