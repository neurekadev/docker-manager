package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/jobs"
	"github.com/neurekadev/dockyard/internal/manager/jobs/jobstest"
	"github.com/neurekadev/dockyard/internal/manager/requestinfo"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// auditAuthz allows everything except for user "eve".
type auditAuthz struct{}

func (auditAuthz) Can(_ context.Context, p authz.Principal, _ string, _ authz.Resource) authz.Decision {
	if p.UserID == "eve" {
		return authz.Deny("eve")
	}
	return authz.Allow("test")
}

type auditFixture struct {
	t      *testing.T
	ctx    context.Context
	db     *bun.DB
	clk    *clock.Fake
	log    *audit.Log
	eng    *jobs.Engine
	disp   *jobstest.Dispatcher
	api    huma.API
	h      http.Handler
	mirror *testutil.LogBuffer
}

func openTestDB(t *testing.T) *bun.DB {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: dir, Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	return db
}

// newAuditFixture builds the real API with a real audit log, job engine and
// log mirror. Requests carry test principals (X-Test-User, X-Test-Token =
// token ID of a token owned by X-Test-User) and a resolved client IP
// (X-Test-IP), like the server's middleware would provide.
func newAuditFixture(t *testing.T, az authz.Authorizer) *auditFixture {
	t.Helper()
	f := &auditFixture{t: t, ctx: testutil.Context(t), db: openTestDB(t), clk: testutil.FakeClock(), disp: jobstest.New()}
	mlog, mirror := testutil.CaptureLogger()
	f.mirror = mirror
	var err error
	if f.log, err = audit.New(audit.Options{DB: f.db, Clock: f.clk, Logger: testutil.Logger(t), Mirror: mlog}); err != nil {
		t.Fatal(err)
	}
	f.eng, err = jobs.New(jobs.Options{DB: f.db, Clock: f.clk, Logger: testutil.Logger(t), Dispatcher: f.disp, Audit: f.log,
		Authorizer: authz.Func(func(context.Context, authz.Principal, string, authz.Resource) authz.Decision { return authz.Allow("t") })})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.eng.Close)
	mux := http.NewServeMux()
	f.api = New(mux, Deps{Jobs: f.eng, Authorizer: az, Clock: f.clk, Audit: f.log, Idempotency: &memIdempotency{recs: map[string]*memRec{}}})
	logger := testutil.Logger(t)
	f.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := logging.WithRequestID(r.Context(), "req-123")
		ctx = logging.IntoContext(ctx, logger)
		info := requestinfo.Info{Scheme: "https", Host: "dy.example.com"}
		if ip := r.Header.Get("X-Test-IP"); ip != "" {
			info.ClientIP = netip.MustParseAddr(ip)
		}
		ctx = requestinfo.With(ctx, info)
		if u := r.Header.Get("X-Test-User"); u != "" {
			p := authz.Principal{Kind: authz.KindUser, UserID: u}
			if tok := r.Header.Get("X-Test-Token"); tok != "" {
				p = authz.Principal{Kind: authz.KindAPIToken, UserID: u, TokenID: tok}
			}
			ctx, _ = authz.WithPrincipal(ctx, p)
		}
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
	return f
}

func (f *auditFixture) do(method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *auditFixture) records() []domain.AuditRecord {
	f.t.Helper()
	recs, err := f.log.Records(f.ctx, domain.AuditFilter{Ascending: true, Limit: 10000})
	if err != nil {
		f.t.Fatal(err)
	}
	return recs
}

func (f *auditFixture) last() domain.AuditRecord {
	f.t.Helper()
	recs := f.records()
	if len(recs) == 0 {
		f.t.Fatal("no audit records")
	}
	return recs[len(recs)-1]
}

func details(t *testing.T, r domain.AuditRecord) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(r.Details, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

type selectorInput struct {
	StackID string `path:"stackId"`
	Body    struct {
		Action string `json:"action" enum:"start,stop"`
	}
}

type nameInput struct {
	EnvironmentID string `path:"environmentId"`
	ContainerID   string `path:"containerId"`
	Body          struct {
		Name string `json:"name" minLength:"1" maxLength:"10"`
	}
}

type signInInput struct {
	Body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
}

func registerAuditTestOps(a huma.API) {
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "create-test-stack-operation", Method: http.MethodPost, Path: BasePath + "/test/stacks/{stackId}/operations", Summary: "op"},
		Capability: "stack.{action}", CapabilityValues: []Capability{"stack.start", "stack.stop"}, Scope: ScopeResource,
	}, func(ctx context.Context, in *selectorInput) (*struct{}, error) {
		audit.SetAction(ctx, "stack."+in.Body.Action)
		audit.AddTarget(ctx, domain.AuditTarget{Type: "container", ID: "web-1"})
		audit.SetDiff(ctx, map[string]any{"state": "running"}, map[string]any{"state": "stopped"})
		return nil, nil
	})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "rename-test-container", Method: http.MethodPatch, Path: BasePath + "/test/environments/{environmentId}/containers/{containerId}", Summary: "rename"},
		Capability: "container.update", Scope: ScopeResource,
	}, func(_ context.Context, in *nameInput) (*struct{}, error) {
		if in.Body.Name == "taken" {
			return nil, Conflict("container_name_taken", "taken")
		}
		if in.Body.Name == "boom" {
			panic("boom")
		}
		return nil, nil
	})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "create-test-session", Method: http.MethodPost, Path: BasePath + "/test/session", Summary: "sign in"},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *signInInput) (*struct{}, error) {
		if in.Body.Password != "right-password" {
			audit.SetDetail(ctx, "userName", in.Body.User)
			return nil, Unauthenticated("invalid credentials")
		}
		audit.SetPrincipal(ctx, authz.Principal{Kind: authz.KindUser, UserID: in.Body.User})
		return nil, nil
	})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "create-test-job", Method: http.MethodPost, Path: BasePath + "/test/jobs", Summary: "job"},
		Capability: "stack.deploy", Scope: ScopeResource,
	}, func(context.Context, *struct{}) (*JobAccepted, error) {
		return Accepted(domain.Job{ID: "job-77", Kind: "stack.deploy", State: domain.JobQueued, Origin: domain.OriginManual}), nil
	})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "get-test-download", Method: http.MethodGet, Path: BasePath + "/test/backups/{backupId}/download", Summary: "download"},
		Capability: "backup.contents.download", Scope: ScopeResource, Audit: AuditAlways,
	}, func(context.Context, *struct {
		BackupID string `path:"backupId"`
	}) (*struct{}, error) {
		return nil, nil
	})
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "get-test-thing", Method: http.MethodGet, Path: BasePath + "/test/things", Summary: "read"},
		Capability: "thing.read", Scope: ScopeResource,
	}, func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	Register(a, Operation{
		Operation:  huma.Operation{OperationID: "get-test-webauthn-challenge", Method: http.MethodGet, Path: BasePath + "/test/challenge", Summary: "custom event"},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		// A non-request event recorded from an unaudited route.
		return nil, audit.Record(ctx, domain.AuditEvent{Action: "auth.lockout", Category: domain.AuditIdentity,
			Outcome: domain.AuditDenied, Targets: []domain.AuditTarget{{Type: "user", ID: "u-9"}}})
	})
	Register(a, Operation{
		Operation: huma.Operation{OperationID: "create-test-invitation", Method: http.MethodPost, Path: BasePath + "/test/invitations", Summary: "invite",
			DefaultStatus: http.StatusCreated},
		Capability: CapabilityOwner, Scope: ScopeInstance, Idempotency: IdempotencyStored,
	}, func(context.Context, *struct{ IdempotencyKeyParam }) (*struct{}, error) { return nil, nil })
}

func TestAuditRecordShape(t *testing.T) {
	f := newAuditFixture(t, auditAuthz{})
	registerAuditTestOps(f.api)
	ua := "dockyard-cli/1.0"

	// Selector operation: concrete action, path + handler targets, diff,
	// API-token actor, client IP, user agent, request ID.
	rec := f.do(http.MethodPost, BasePath+"/test/stacks/st-1/operations", `{"action":"stop"}`,
		"X-Test-User", "alice", "X-Test-Token", "tok-1", "X-Test-IP", "198.51.100.7", "User-Agent", ua)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	r := f.last()
	d := details(t, r)
	if r.Action != "stack.stop" || r.OperationID != "create-test-stack-operation" || r.Category != domain.AuditOperations ||
		r.Actor != (domain.AuditActor{Kind: domain.AuditActorAPIToken, UserID: "alice", TokenID: "tok-1"}) ||
		r.ClientIP != "198.51.100.7" || r.UserAgent != ua || r.RequestID != "req-123" || r.Outcome != domain.AuditSuccess ||
		r.ErrorClass != "" || len(r.Targets) != 2 || r.Targets[0] != (domain.AuditTarget{Type: "stack", ID: "st-1"}) ||
		r.Targets[1].ID != "web-1" || d["status"] != float64(204) || d["diff"] == nil {
		t.Fatalf("record %+v %v", r, d)
	}

	// Environment-scoped path, handler error class, failure outcome.
	f.do(http.MethodPatch, BasePath+"/test/environments/env-1/containers/web", `{"name":"taken"}`, "X-Test-User", "alice")
	r = f.last()
	if r.Action != "container.update" || r.EnvironmentID != "env-1" || r.Outcome != domain.AuditFailure || r.ErrorClass != "container_name_taken" ||
		len(r.Targets) != 1 || r.Targets[0] != (domain.AuditTarget{Type: "container", ID: "web", EnvironmentID: "env-1"}) || r.Actor.UserID != "alice" {
		t.Fatalf("conflict record %+v", r)
	}
	// Validation failure before the handler.
	f.do(http.MethodPatch, BasePath+"/test/environments/env-1/containers/web", `{"name":""}`, "X-Test-User", "alice")
	if r = f.last(); r.Outcome != domain.AuditFailure || r.ErrorClass != CodeValidationFailed {
		t.Fatalf("validation record %+v", r)
	}
	// A panicking handler is recorded as an error and still panics.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic swallowed")
			}
		}()
		f.do(http.MethodPatch, BasePath+"/test/environments/env-1/containers/web", `{"name":"boom"}`, "X-Test-User", "alice")
	}()
	if r = f.last(); r.Outcome != domain.AuditError || r.ErrorClass != CodeInternal || details(t, r)["status"] != float64(500) {
		t.Fatalf("panic record %+v", r)
	}
	n := len(f.records())
	// Unauthenticated calls of non-public operations are not recorded.
	if rec := f.do(http.MethodPost, BasePath+"/jobs/nope/cancellations", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	if len(f.records()) != n {
		t.Fatal("unauthenticated request recorded")
	}
	// ... but a signed-in caller without access is (404 hides the job).
	f.do(http.MethodPost, BasePath+"/jobs/nope/cancellations", "", "X-Test-User", "eve")
	if r = f.last(); r.Action != "job.cancel" || r.Outcome != domain.AuditFailure || r.ErrorClass != CodeNotFound ||
		r.Targets[0] != (domain.AuditTarget{Type: "job", ID: "nope"}) {
		t.Fatalf("hidden job record %+v", r)
	}

	// Public sign-in: failure is anonymous and denied, success names the user.
	f.do(http.MethodPost, BasePath+"/test/session", `{"user":"bob","password":"wrong-password"}`, "X-Test-IP", "203.0.113.5")
	r = f.last()
	if r.Action != "test_session.create" || r.Category != domain.AuditIdentity || r.Actor.Kind != domain.AuditActorAnonymous ||
		r.Outcome != domain.AuditDenied || r.ErrorClass != CodeUnauthenticated || r.ClientIP != "203.0.113.5" ||
		details(t, r)["userName"] != "bob" || strings.Contains(string(r.Details), "wrong-password") {
		t.Fatalf("failed sign-in record %+v", r)
	}
	f.do(http.MethodPost, BasePath+"/test/session", `{"user":"bob","password":"right-password"}`)
	if r = f.last(); r.Actor != (domain.AuditActor{Kind: domain.AuditActorUser, UserID: "bob"}) || r.Outcome != domain.AuditSuccess {
		t.Fatalf("sign-in record %+v", r)
	}

	// 202 + job: the job is linked.
	f.do(http.MethodPost, BasePath+"/test/jobs", "", "X-Test-User", "alice")
	if r = f.last(); r.JobID != "job-77" || !slices.Contains(r.Targets, domain.AuditTarget{Type: "job", ID: "job-77"}) || details(t, r)["status"] != float64(202) {
		t.Fatalf("job record %+v", r)
	}

	// GET: audited only with AuditAlways.
	n = len(f.records())
	f.do(http.MethodGet, BasePath+"/test/things", "", "X-Test-User", "alice")
	if len(f.records()) != n {
		t.Fatal("unaudited GET recorded")
	}
	f.do(http.MethodGet, BasePath+"/test/backups/b-1/download", "", "X-Test-User", "alice")
	if r = f.last(); r.Action != "backup.contents.download" || r.Targets[0] != (domain.AuditTarget{Type: "backup", ID: "b-1"}) {
		t.Fatalf("download record %+v", r)
	}

	// Handlers of any route reach audit.Record (context defaults apply).
	if rec := f.do(http.MethodGet, BasePath+"/test/challenge", "", "X-Test-IP", "192.0.2.1"); rec.Code != http.StatusNoContent {
		t.Fatalf("challenge %d %s", rec.Code, rec.Body)
	}
	if r = f.last(); r.Action != "auth.lockout" || r.Actor.Kind != domain.AuditActorAnonymous || r.ClientIP != "192.0.2.1" ||
		r.RequestID != "req-123" || r.OperationID != "" || r.Outcome != domain.AuditDenied {
		t.Fatalf("custom event %+v", r)
	}

	// Idempotency-Key replays are recorded as such.
	for i := range 2 {
		rec := f.do(http.MethodPost, BasePath+"/test/invitations", "", "X-Test-User", "alice", "Idempotency-Key", "k-1")
		if rec.Code != http.StatusCreated {
			t.Fatalf("status %d", rec.Code)
		}
		r = f.last()
		if r.Action != "test_invitation.create" || (details(t, r)["idempotentReplay"] == true) != (i == 1) {
			t.Fatalf("replay %d record %+v %v", i, r, details(t, r))
		}
	}

	if rep, err := f.log.Verify(f.ctx); err != nil || !rep.OK {
		t.Fatalf("verify %+v %v", rep, err)
	}
}

func TestRegisterAuditRules(t *testing.T) {
	noop := func(context.Context, *struct{}) (*healthOutput, error) { return nil, nil }
	get := huma.Operation{OperationID: "get-thing", Method: http.MethodGet, Path: BasePath + "/thing", Summary: "thing"}
	post := huma.Operation{OperationID: "create-thing", Method: http.MethodPost, Path: BasePath + "/thing", Summary: "thing"}
	for name, op := range map[string]Operation{
		"unknown mode":            {Operation: post, Capability: "thing.create", Scope: ScopeResource, Audit: "sometimes"},
		"action on unaudited GET": {Operation: get, Capability: "thing.read", Scope: ScopeResource, AuditAction: "thing.read"},
		"malformed action":        {Operation: post, Capability: "thing.create", Scope: ScopeResource, AuditAction: "Create Thing"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: Register did not panic", name)
				}
			}()
			Register(New(http.NewServeMux(), Deps{}), op, noop)
		}()
	}
	a := New(http.NewServeMux(), Deps{})
	Register(a, Operation{Operation: post, Capability: CapabilityOwner, Scope: ScopeInstance, AuditAction: "thing.make"}, noop)
	Register(a, Operation{Operation: get, Capability: "thing.read", Scope: ScopeResource}, noop)
	p := a.OpenAPI().Paths[BasePath+"/thing"]
	if p.Post.Extensions[ExtAudit] != "thing.make" || p.Get.Extensions[ExtAudit] != nil {
		t.Fatalf("extensions post %v get %v", p.Post.Extensions, p.Get.Extensions)
	}
	for _, tc := range []struct {
		op   Operation
		want string
	}{
		{Operation{Operation: post, Capability: "thing.create"}, "thing.create"},
		{Operation{Operation: post, Capability: CapabilityOwner}, "thing.create"},
		{Operation{Operation: huma.Operation{OperationID: "delete-my-passkey", Method: http.MethodDelete}, Capability: CapabilityAuthenticated}, "my_passkey.delete"},
		{Operation{Operation: huma.Operation{OperationID: "create-stack-operation"}, Capability: "stack.{action}"}, "stack_operation.create"},
	} {
		if got := tc.op.AuditActionKey(); got != tc.want {
			t.Errorf("AuditActionKey(%s) = %s, want %s", tc.op.OperationID, got, tc.want)
		}
	}
	for _, tc := range []struct {
		path string
		want []pathTarget
	}{
		{"/api/v1/environments/{environmentId}/containers/{containerId}", []pathTarget{{"environmentId", "environment"}, {"containerId", "container"}}},
		{"/api/v1/me/api-tokens/{tokenId}", []pathTarget{{"tokenId", "api_token"}}},
		{"/api/v1/backup-repositories/{repositoryId}/key-rotations", []pathTarget{{"repositoryId", "backup_repository"}}},
		{"/api/v1/update-policies/{policyId}", []pathTarget{{"policyId", "update_policy"}}},
		{"/api/v1/registries/{registryId}", []pathTarget{{"registryId", "registry"}}},
		{"/api/v1/stacks/{stackId}/migrations/{migrationId}/source-removals", []pathTarget{{"stackId", "stack"}, {"migrationId", "migration"}}},
	} {
		if got := pathTargets(tc.path); !slices.Equal(got, tc.want) {
			t.Errorf("pathTargets(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
	for status, want := range map[int]domain.AuditOutcome{200: domain.AuditSuccess, 202: domain.AuditSuccess, 304: domain.AuditSuccess,
		401: domain.AuditDenied, 403: domain.AuditDenied, 404: domain.AuditFailure, 409: domain.AuditFailure, 422: domain.AuditFailure,
		500: domain.AuditError, 503: domain.AuditError} {
		if got := AuditOutcomeFor(status); got != want {
			t.Errorf("AuditOutcomeFor(%d) = %s", status, got)
		}
	}
}

// TestEveryServedMutatingOperationIsAudited walks the served OpenAPI
// document: every non-GET operation must declare x-dockyard-audit, and
// calling it (as any caller, with any outcome) must append exactly one
// record carrying that action and the operation ID. Unaudited GETs must not
// record anything. The route inventory counterpart (every cataloged
// mutating route, planned or not) is in inventory/audit_coverage_test.go.
func TestEveryServedMutatingOperationIsAudited(t *testing.T) {
	f := newAuditFixture(t, auditAuthz{})
	checked := 0
	for path, item := range f.api.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put,
			http.MethodPatch: item.Patch, http.MethodDelete: item.Delete} {
			if op == nil {
				continue
			}
			action, audited := op.Extensions[ExtAudit].(string)
			if method != http.MethodGet && !audited {
				t.Errorf("%s %s (%s) is not audited", method, path, op.OperationID)
				continue
			}
			target := pathParamRE.ReplaceAllString(path, "p-$1")
			before := len(f.records())
			f.do(method, target, "", "X-Test-User", "alice")
			recs := f.records()[before:]
			if !audited {
				if len(recs) != 0 {
					t.Errorf("%s %s (%s): unaudited GET recorded %d records", method, path, op.OperationID, len(recs))
				}
				continue
			}
			var mine []domain.AuditRecord
			for _, r := range recs {
				if r.OperationID == op.OperationID {
					mine = append(mine, r)
				}
			}
			if len(mine) != 1 || mine[0].Action != action || mine[0].Actor.UserID != "alice" {
				t.Errorf("%s %s (%s): want one %s record, got %+v", method, path, op.OperationID, action, mine)
			}
			checked++
		}
	}
	if checked < 2 { // create-job-cancellation and export-audit-events at least
		t.Fatalf("only %d audited operations checked", checked)
	}
}

func seedAudit(t *testing.T, f *auditFixture) {
	t.Helper()
	evs := []domain.AuditEvent{
		{Action: "stack.deploy", Actor: domain.AuditActor{Kind: domain.AuditActorUser, UserID: "alice"}, EnvironmentID: "env-1",
			Targets: []domain.AuditTarget{{Type: "stack", ID: "st-1"}}, JobID: "job-1"},
		{Action: "container.restart", Actor: domain.AuditActor{Kind: domain.AuditActorAPIToken, UserID: "alice", TokenID: "tok-9"},
			EnvironmentID: "env-2", Targets: []domain.AuditTarget{{Type: "container", ID: "web", EnvironmentID: "env-2"}},
			Outcome: domain.AuditDenied, ErrorClass: "forbidden", UserAgent: "=cmd|' /C calc'!A0"},
		{Action: "group_permissions.replace", Actor: domain.AuditActor{Kind: domain.AuditActorUser, UserID: "owner"},
			Targets: []domain.AuditTarget{{Type: "group", ID: "g-1"}}, Details: map[string]any{"diff": map[string]any{"before": []any{}, "after": []any{map[string]any{"capability": "stack.read", "effect": "allow"}}}}},
		{Action: audit.ActionJobFinished, Actor: audit.ServiceActor(), JobID: "job-1", Outcome: domain.AuditFailure, ErrorClass: "agent_offline"},
	}
	for _, ev := range evs {
		if err := f.log.Record(f.ctx, ev); err != nil {
			t.Fatal(err)
		}
		f.clk.Advance(time.Hour)
	}
}

func listAudit(t *testing.T, f *auditFixture, query string, hdr ...string) (Page[AuditEvent], *httptest.ResponseRecorder) {
	t.Helper()
	rec := f.do(http.MethodGet, BasePath+"/audit"+query, "", append([]string{"X-Test-User", "owner"}, hdr...)...)
	var page Page[AuditEvent]
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
	}
	return page, rec
}

func seqs(p Page[AuditEvent]) []int64 {
	var out []int64
	for _, e := range p.Items {
		out = append(out, e.Seq)
	}
	return out
}

func TestAuditListFiltersAndPaging(t *testing.T) {
	f := newAuditFixture(t, auditAuthz{})
	seedAudit(t, f)
	since := testutil.Epoch.Add(time.Hour).Format(time.RFC3339)
	until := testutil.Epoch.Add(3 * time.Hour).Format(time.RFC3339)
	for query, want := range map[string][]int64{
		"":                                  {4, 3, 2, 1},
		"?actorId=alice":                    {2, 1},
		"?actorId=tok-9":                    {2},
		"?actorKind=service&actorKind=user": {4, 3, 1},
		"?action=stack.deploy&action=job.finished": {4, 1},
		"?category=authorization":                  {3},
		"?outcome=denied&outcome=failure":          {4, 2},
		"?environmentId=env-2":                     {2},
		"?resource=stack:st-1":                     {1},
		"?resource=job:job-1":                      {4, 1},
		"?jobId=job-1":                             {4, 1},
		"?since=" + since + "&until=" + until:      {3, 2},
	} {
		page, rec := listAudit(t, f, query)
		if rec.Code != http.StatusOK || !slices.Equal(seqs(page), want) {
			t.Errorf("%q: status %d seqs %v, want %v (%s)", query, rec.Code, seqs(page), want, rec.Body)
		}
	}
	page, _ := listAudit(t, f, "?resource=group:g-1")
	if diff, ok := page.Items[0].Details["diff"].(map[string]any); !ok || diff["after"] == nil {
		t.Fatalf("rule diff not returned: %+v", page.Items[0])
	}

	// Cursor pagination, bound to the filters.
	page, _ = listAudit(t, f, "?limit=3")
	if !slices.Equal(seqs(page), []int64{4, 3, 2}) || page.NextCursor == "" || page.Total != nil {
		t.Fatalf("page 1 %+v", page)
	}
	page2, _ := listAudit(t, f, "?limit=3&cursor="+page.NextCursor)
	if !slices.Equal(seqs(page2), []int64{1}) || page2.NextCursor != "" {
		t.Fatalf("page 2 %+v", page2)
	}
	if _, rec := listAudit(t, f, "?limit=3&action=stack.deploy&cursor="+page.NextCursor); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cursor reused with other filters: %d", rec.Code)
	}
	for _, q := range []string{"?resource=stack", "?since=yesterday", "?outcome=meh", "?action=Bad Action"} {
		if _, rec := listAudit(t, f, strings.ReplaceAll(q, " ", "%20")); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}

	// Access: authentication, then the all-or-nothing capability.
	if rec := f.do(http.MethodGet, BasePath+"/audit", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, BasePath+"/audit", "", "X-Test-User", "eve"); rec.Code != http.StatusForbidden {
		t.Fatalf("list without audit.read: %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, BasePath+"/audit/exports", "", "X-Test-User", "eve"); rec.Code != http.StatusForbidden {
		t.Fatalf("export without audit.export: %d", rec.Code)
	}
	// Reading the list is not itself audited; the denied export is.
	if r := f.last(); r.Action != "audit.export" || r.Outcome != domain.AuditDenied || r.Actor.UserID != "eve" {
		t.Fatalf("denied export record %+v", r)
	}
	// Default deny when no authorizer is configured (#17 not wired).
	g := newAuditFixture(t, nil)
	if rec := g.do(http.MethodGet, BasePath+"/audit", "", "X-Test-User", "owner"); rec.Code != http.StatusForbidden {
		t.Fatalf("deny-all list: %d", rec.Code)
	}
}

func TestAuditExport(t *testing.T) {
	f := newAuditFixture(t, auditAuthz{})
	seedAudit(t, f)
	stored := f.records()

	rec := f.do(http.MethodGet, BasePath+"/audit/exports?format=ndjson", "", "X-Test-User", "owner")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/x-ndjson" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `attachment; filename="dockyard-audit-`) {
		t.Fatalf("ndjson export %d %v", rec.Code, rec.Header())
	}
	sc := bufio.NewScanner(rec.Body)
	var lines []map[string]json.RawMessage
	for sc.Scan() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		lines = append(lines, m)
	}
	if len(lines) != len(stored) {
		t.Fatalf("exported %d lines, stored %d", len(lines), len(stored))
	}
	for i, m := range lines {
		var seq int64
		var hash string
		_ = json.Unmarshal(m["seq"], &seq)
		_ = json.Unmarshal(m["hash"], &hash)
		if seq != stored[i].Seq || hash != stored[i].Hash || string(m["details"]) != string(stored[i].Details) {
			t.Fatalf("line %d: seq %d hash %s details %s; want %+v", i, seq, hash, m["details"], stored[i])
		}
	}
	// The export is audited after it finished (with its filters and count)
	// and is not part of its own output.
	r := f.last()
	d := details(t, r)
	if r.Action != "audit.export" || r.Category != domain.AuditSystem || r.Seq != stored[len(stored)-1].Seq+1 ||
		d["format"] != "ndjson" || d["exportedRecords"] != float64(len(stored)) {
		t.Fatalf("export record %+v %v", r, d)
	}

	// CSV with filters; formula injection neutralized.
	rec = f.do(http.MethodGet, BasePath+"/audit/exports?format=csv&actorId=alice", "", "X-Test-User", "owner")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv export %d %v", rec.Code, rec.Header())
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || !slices.Equal(rows[0], auditCSVHeader) || rows[1][4] != "stack.deploy" || rows[2][4] != "container.restart" {
		t.Fatalf("csv rows %q", rows)
	}
	if ua := rows[2][11]; ua != "'=cmd|' /C calc'!A0" {
		t.Fatalf("user agent cell %q", ua)
	}
	if d := details(t, f.last()); d["format"] != "csv" || d["exportedRecords"] != float64(2) ||
		d["filters"].(map[string]any)["actorId"] != "alice" {
		t.Fatalf("csv export record %v", d)
	}
	if rec := f.do(http.MethodGet, BasePath+"/audit/exports?format=xml", "", "X-Test-User", "owner"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad format %d", rec.Code)
	}
	if rep, err := f.log.Verify(f.ctx); err != nil || !rep.OK {
		t.Fatalf("verify %+v %v", rep, err)
	}
}
