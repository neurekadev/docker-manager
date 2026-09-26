package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/buildinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

type echoInput struct {
	Body struct {
		Name  string `json:"name" minLength:"1" maxLength:"10"`
		Count int    `json:"count" minimum:"0"`
	}
}

type echoOutput struct {
	Body struct {
		Name string `json:"name"`
	}
}

// newTestAPI builds the real API plus test-only operations that exercise
// the error paths, and wraps it with a request-ID context like the server.
func newTestAPI(t *testing.T, deps Deps) (http.Handler, *testutil.LogBuffer) {
	t.Helper()
	mux := http.NewServeMux()
	a := New(mux, deps)
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "test-echo", Method: http.MethodPost, Path: BasePath + "/test/echo", Summary: "echo"},
		Capability: "test.echo", Scope: ScopeInstance,
	}, func(_ context.Context, in *echoInput) (*echoOutput, error) {
		out := &echoOutput{}
		out.Body.Name = in.Body.Name
		return out, nil
	})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "test-missing", Method: http.MethodGet, Path: BasePath + "/test/missing", Summary: "missing"},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(context.Context, *struct{}) (*echoOutput, error) {
		return nil, NotFound("stack not found")
	})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "test-boom", Method: http.MethodGet, Path: BasePath + "/test/boom", Summary: "boom"},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(context.Context, *struct{}) (*echoOutput, error) {
		return nil, errors.New("db exploded: password=hunter2")
	})
	logger, buf := testutil.CaptureLogger()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := logging.WithRequestID(r.Context(), "req-123")
		ctx = logging.IntoContext(ctx, logger)
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	return h, buf
}

func do(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeError asserts the exact error shape: all five members, nothing else.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) Error {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != ErrorContentType {
		t.Fatalf("content type = %q", ct)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body not JSON: %v: %s", err, rec.Body)
	}
	want := []string{"code", "message", "details", "requestId", "retryable"}
	if len(raw) != len(want) {
		t.Fatalf("error members = %v, want exactly %v", keys(raw), want)
	}
	for _, k := range want {
		if _, ok := raw[k]; !ok {
			t.Fatalf("error lacks %q: %s", k, rec.Body)
		}
	}
	var e Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Code != code {
		t.Fatalf("code = %q, want %q", e.Code, code)
	}
	if e.RequestID != "req-123" {
		t.Fatalf("requestId = %q", e.RequestID)
	}
	return e
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestErrorShape404(t *testing.T) {
	h, _ := newTestAPI(t, Deps{})
	e := decodeError(t, do(t, h, http.MethodGet, BasePath+"/test/missing", ""), http.StatusNotFound, CodeNotFound)
	if e.Message != "stack not found" || e.Retryable || len(e.Details) != 0 {
		t.Fatalf("unexpected error %+v", e)
	}
}

func TestErrorShape422(t *testing.T) {
	h, _ := newTestAPI(t, Deps{})
	e := decodeError(t, do(t, h, http.MethodPost, BasePath+"/test/echo", `{"name":"","count":-1}`), http.StatusUnprocessableEntity, CodeValidationFailed)
	fields := map[string]bool{}
	for _, d := range e.Details {
		fields[d.Field] = true
		if d.Message == "" {
			t.Errorf("detail without message: %+v", d)
		}
	}
	if !fields["body.name"] || !fields["body.count"] {
		t.Fatalf("details = %+v", e.Details)
	}
	// Malformed JSON is a 400 bad_request in the same shape.
	decodeError(t, do(t, h, http.MethodPost, BasePath+"/test/echo", `{"name":`), http.StatusBadRequest, CodeBadRequest)
}

func TestErrorShape500HidesCause(t *testing.T) {
	h, logs := newTestAPI(t, Deps{})
	rec := do(t, h, http.MethodGet, BasePath+"/test/boom", "")
	e := decodeError(t, rec, http.StatusInternalServerError, CodeInternal)
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "exploded") {
		t.Fatalf("internal cause leaked to client: %s", rec.Body)
	}
	if e.Message != "internal server error" || e.Retryable {
		t.Fatalf("unexpected error %+v", e)
	}
	if !strings.Contains(logs.String(), "db exploded") {
		t.Fatalf("cause not logged: %s", logs.String())
	}
}

func TestHealthAndCapabilities(t *testing.T) {
	h, _ := newTestAPI(t, Deps{Build: buildinfo.Info{Version: "1.2.3", Commit: "abc"}, Features: []string{"x"}})
	rec := do(t, h, http.MethodGet, BasePath+"/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status %d", rec.Code)
	}
	var hb HealthBody
	_ = json.Unmarshal(rec.Body.Bytes(), &hb)
	if hb != (HealthBody{Status: "ok", Version: "1.2.3", Commit: "abc"}) {
		t.Fatalf("health = %+v", hb)
	}

	rec = do(t, h, http.MethodGet, BasePath+"/capabilities", "")
	var cb CapabilitiesBody
	_ = json.Unmarshal(rec.Body.Bytes(), &cb)
	if cb.APIVersion != "v1" || cb.AgentProtocolVersion != "docker-manager.agent/v1" || cb.ManagerVersion != "1.2.3" || len(cb.Features) != 1 {
		t.Fatalf("capabilities = %+v", cb)
	}
	h, _ = newTestAPI(t, Deps{})
	rec = do(t, h, http.MethodGet, BasePath+"/capabilities", "")
	if !strings.Contains(rec.Body.String(), `"features":[]`) {
		t.Fatalf("features must be an empty list, got %s", rec.Body)
	}
}

func TestReadiness(t *testing.T) {
	ready := true
	h, _ := newTestAPI(t, Deps{Readiness: func(context.Context) []Check {
		return []Check{{Name: "database", OK: true}, {Name: "migrations", OK: ready, Message: "2 pending"}}
	}})
	rec := do(t, h, http.MethodGet, BasePath+"/health/ready", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ready"`) {
		t.Fatalf("ready: %d %s", rec.Code, rec.Body)
	}
	ready = false
	e := decodeError(t, do(t, h, http.MethodGet, BasePath+"/health/ready", ""), http.StatusServiceUnavailable, CodeNotReady)
	if !e.Retryable || len(e.Details) != 1 || e.Details[0].Field != "check.migrations" {
		t.Fatalf("not ready error = %+v", e)
	}
}

func TestRegisterRejectsMissingMetadata(t *testing.T) {
	base := huma.Operation{OperationID: "get-thing", Method: http.MethodGet, Path: BasePath + "/thing", Summary: "thing"}
	cases := map[string]Operation{
		"no operation id":    {Operation: huma.Operation{Method: http.MethodGet, Path: "/x", Summary: "s"}, Capability: CapabilityPublic, Scope: ScopeNone},
		"camelCase id":       {Operation: withID(base, "getThing"), Capability: CapabilityPublic, Scope: ScopeNone},
		"no capability":      {Operation: base, Scope: ScopeNone},
		"bad capability":     {Operation: base, Capability: "Container Restart", Scope: ScopeResource},
		"no scope":           {Operation: base, Capability: CapabilityPublic},
		"bad scope":          {Operation: base, Capability: "container.restart", Scope: "global"},
		"public with scope":  {Operation: base, Capability: CapabilityPublic, Scope: ScopeInstance},
		"key with none":      {Operation: base, Capability: "container.restart", Scope: ScopeNone},
		"no summary":         {Operation: withSummary(base, ""), Capability: CapabilityPublic, Scope: ScopeNone},
		"single-segment key": {Operation: base, Capability: "restart", Scope: ScopeResource},
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("Register did not panic")
				}
			}()
			a := New(http.NewServeMux(), Deps{})
			Register(a, op, func(context.Context, *struct{}) (*healthOutput, error) { return nil, nil })
		})
	}
}

func withID(op huma.Operation, id string) huma.Operation { op.OperationID = id; return op }
func withSummary(op huma.Operation, s string) huma.Operation {
	op.Summary = s
	return op
}

// TestOpenAPICompleteness walks the generated spec: every operation must
// have a unique kebab-case operationId, a capability and a scope.
func TestOpenAPICompleteness(t *testing.T) {
	spec, err := SpecJSON()
	if err != nil {
		t.Fatal(err)
	}
	checkCompleteness(t, spec)
}

type reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

func checkCompleteness(t reporter, spec []byte) {
	t.Helper()
	var doc struct {
		OpenAPI    string                                `json:"openapi"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			SecuritySchemes map[string]json.RawMessage `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("openapi = %q, want 3.1.x", doc.OpenAPI)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("spec has no paths")
	}
	for _, name := range []string{SecurityCookie, SecurityBearer} {
		if _, ok := doc.Components.SecuritySchemes[name]; !ok {
			t.Errorf("components.securitySchemes lacks %s", name)
		}
	}
	seen := map[string]string{}
	methods := map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true, "head": true, "options": true, "trace": true}
	for p, item := range doc.Paths {
		if !strings.HasPrefix(p, BasePath+"/") {
			t.Errorf("path %s is outside %s", p, BasePath)
		}
		for method, raw := range item {
			if !methods[method] {
				continue
			}
			var op map[string]any
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatal(err)
			}
			where := strings.ToUpper(method) + " " + p
			id, _ := op["operationId"].(string)
			if id == "" {
				t.Errorf("%s: missing operationId", where)
			} else if prev, dup := seen[id]; dup {
				t.Errorf("%s: duplicate operationId %q (also %s)", where, id, prev)
			} else {
				seen[id] = where
			}
			capability, _ := op[ExtCapability].(string)
			if err := ValidateCapability(capability); capability == "" || err != nil {
				t.Errorf("%s: missing/invalid %s: %v", where, ExtCapability, err)
			}
			scope, _ := op[ExtScope].(string)
			if err := ValidateScope(scope); scope == "" || err != nil {
				t.Errorf("%s: missing/invalid %s: %v", where, ExtScope, err)
			}
			// Audit by construction (#30): every mutating operation records.
			if action, _ := op[ExtAudit].(string); method != "get" && !IsCapabilityKey(action) {
				t.Errorf("%s: mutating operation without a valid %s action (%q)", where, ExtAudit, action)
			}
			responses, _ := op["responses"].(map[string]any)
			hasError := false
			for code := range responses {
				if code == "default" || strings.HasPrefix(code, "4") || strings.HasPrefix(code, "5") {
					hasError = true
				}
			}
			if !hasError {
				t.Errorf("%s: no error response documented", where)
			}
		}
	}
}

func TestCompletenessCheckCatchesViolations(t *testing.T) {
	bad := []byte(`{"openapi":"3.1.0","paths":{"/api/v1/a":{"get":{"operationId":"x","responses":{"default":{}}}},"/api/v1/b":{"get":{"operationId":"x","x-docker-manager-capability":"public","x-docker-manager-scope":"none","responses":{}}},` +
		`"/api/v1/c":{"post":{"operationId":"y","x-docker-manager-capability":"public","x-docker-manager-scope":"none","responses":{"default":{}}}}}}`)
	ft := &fakeT{T: t}
	checkCompleteness(ft, bad)
	if ft.errors < 5 { // cap+scope missing on /a, duplicate id, no default response on /b, unaudited POST /c
		t.Fatalf("completeness check reported %d problems, want >= 4", ft.errors)
	}
}

type fakeT struct {
	*testing.T
	errors int
}

func (f *fakeT) Errorf(string, ...any) { f.errors++ }
func (f *fakeT) Helper()               {}

// TestOpenAPISnapshot fails when api/openapi.json is stale.
func TestOpenAPISnapshot(t *testing.T) {
	want, err := SpecJSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "..", "api", "openapi.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v (run: bash scripts/generate.sh)", err)
	}
	got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatal("api/openapi.json is out of date with the registered operations.\n" +
			"Regenerate it (and the TypeScript client) with: bash scripts/generate.sh\n" +
			"then review and commit the diff.")
	}
}

func TestServedSpecMatchesSnapshot(t *testing.T) {
	h, _ := newTestAPI(t, Deps{})
	rec := do(t, h, http.MethodGet, OpenAPIPath+".json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var served struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	if _, ok := served.Paths[BasePath+"/health"]; !ok {
		t.Fatal("served spec lacks /api/v1/health")
	}
	rec = do(t, h, http.MethodGet, OpenAPIPath+".yaml", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "openapi: 3.1") {
		t.Fatalf("yaml spec: %d %.80s", rec.Code, rec.Body)
	}
}

func TestPagination(t *testing.T) {
	p := NewPage[string](nil, "", nil)
	b, _ := json.Marshal(p)
	if string(b) != `{"items":[]}` {
		t.Fatalf("empty page = %s", b)
	}
	c, err := EncodeCursor(map[string]string{"after": "id-9"})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := DecodeCursor(c, &v); err != nil || v["after"] != "id-9" {
		t.Fatalf("cursor round trip: %v %v", v, err)
	}
	var e *Error
	if err := DecodeCursor("!!", &v); !errors.As(err, &e) || e.GetStatus() != http.StatusUnprocessableEntity {
		t.Fatalf("bad cursor error = %v", err)
	}

	// Generic pages get stable schema names in the spec.
	a := New(http.NewServeMux(), Deps{})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "list-things", Method: http.MethodGet, Path: BasePath + "/things", Summary: "things"},
		Capability: "thing.read", Scope: ScopeInstance,
	}, func(context.Context, *struct{ PageParams }) (*struct{ Body Page[HealthBody] }, error) {
		return nil, nil
	})
	if _, ok := a.OpenAPI().Components.Schemas.Map()["PageHealthBody"]; !ok {
		t.Fatal("Page[HealthBody] schema not named PageHealthBody")
	}
}
