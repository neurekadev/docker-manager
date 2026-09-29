package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz/authztest"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/managermove"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// fakeMoveLock is the move lock at a level: "" open, "read-only",
// "waiting".
type fakeMoveLock string

func (l fakeMoveLock) ReadOnly() bool { return l != "" }
func (l fakeMoveLock) Waiting() bool  { return l == "waiting" }

// TestMoveLockRefusesMutations: while the manager moves, every non-GET
// operation answers 409 manager_moved (audited like any refusal) except
// sign-in, sign-out and the move routes; reads keep working.
func TestMoveLockRefusesMutations(t *testing.T) {
	h, _ := newTestAPI(t, Deps{MoveLock: fakeMoveLock("read-only")})
	decodeError(t, do(t, h, http.MethodPost, "/api/v1/test/echo", `{"name":"x","count":1}`), http.StatusConflict, CodeManagerMoved)
	if rec := do(t, h, http.MethodGet, "/api/v1/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("health while moving: %d", rec.Code)
	}
	for _, path := range []string{"/api/v1/auth/session", "/api/v1/manager/move/handoff", "/api/v1/manager/move/confirm"} {
		rec := do(t, h, http.MethodPost, path, `{"username":"a","password":"b"}`)
		if rec.Code == http.StatusConflict {
			t.Errorf("POST %s refused while moving: %s", path, rec.Body)
		}
	}
	open, _ := newTestAPI(t, Deps{MoveLock: fakeMoveLock("")})
	if rec := do(t, open, http.MethodPost, "/api/v1/test/echo", `{"name":"x","count":1}`); rec.Code != http.StatusOK {
		t.Fatalf("echo without the lock: %d %s", rec.Code, rec.Body)
	}
}

// TestWaitingClosesEverythingButTheMoveStatus: a manager in waiting mode
// answers 503 manager_move_waiting (Retry-After) to every operation, GETs
// included, except the move status and health.
func TestWaitingClosesEverythingButTheMoveStatus(t *testing.T) {
	h, _ := newTestAPI(t, Deps{MoveLock: fakeMoveLock("waiting"), ManagerMove: &fakeMoves{}})
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/test/echo", `{"name":"x","count":1}`},
		{http.MethodGet, "/api/v1/setup/status", ""},
		{http.MethodPost, "/api/v1/auth/session", `{"username":"a","password":"b"}`},
		{http.MethodPost, "/api/v1/manager/move/handoff", ""},
	} {
		rec := do(t, h, c.method, c.path, c.body)
		decodeError(t, rec, http.StatusServiceUnavailable, CodeManagerMoveWaiting)
		if rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s %s: no Retry-After", c.method, c.path)
		}
	}
	for _, path := range []string{"/api/v1/health", "/api/v1/health/ready", "/api/v1/move/status"} {
		if rec := do(t, h, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s while waiting: %d %s", path, rec.Code, rec.Body)
		}
	}
	rec := do(t, h, http.MethodGet, "/api/v1/move/status", "")
	var st MoveStatus
	if json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.Phase != managermove.WaitWaiting || st.StacksMoved != 2 || st.StacksTotal != 5 ||
		st.OldManager != "http://192.168.1.10:8080" {
		t.Fatalf("status %s", rec.Body)
	}
}

// TestAllowedOperationsExist keeps the allowlists in step with the
// catalog: every operation allowed while moving is a registered mutating
// operation, every one allowed while waiting a registered GET.
func TestAllowedOperationsExist(t *testing.T) {
	oapi := New(http.NewServeMux(), Deps{}).OpenAPI()
	mutating, gets := map[string]bool{}, map[string]bool{}
	for _, item := range oapi.Paths {
		for _, op := range []*huma.Operation{item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil {
				mutating[op.OperationID] = true
			}
		}
		if item.Get != nil {
			gets[item.Get.OperationID] = true
		}
	}
	for id := range allowedWhileMoved {
		if !mutating[id] {
			t.Errorf("%s is allowed while moving but is not a registered mutating operation", id)
		}
	}
	for id := range allowedWhileWaiting {
		if !gets[id] {
			t.Errorf("%s is allowed while waiting but is not a registered GET", id)
		}
	}
}

// fakeMoves records the calls of the move routes.
type fakeMoves struct {
	mu        sync.Mutex
	auths     []managermove.MoveAuth
	handoff   error
	createErr error
	ackErr    error
	runErr    error
	filesErr  error
	created   []managermove.CreateRequest
}

var testMove = domain.ManagerMove{ID: "move-1", State: domain.MoveOpen, CreatedAt: testutil.Epoch, ExpiresAt: testutil.Epoch.Add(7 * 24 * time.Hour),
	ThisServerAddress: "192.168.1.10:8080", NewServerAddress: "192.168.1.20:8080"}

func (f *fakeMoves) Defaults(context.Context) (managermove.Defaults, error) {
	return managermove.Defaults{ThisServerAddress: "192.168.1.10",
		Source: &managermove.SourceStatus{EnvironmentID: "env-old", Name: "old", Online: true, StackCount: 4}}, nil
}

func (f *fakeMoves) CreateMove(_ context.Context, req managermove.CreateRequest) (managermove.Created, error) {
	f.mu.Lock()
	f.created = append(f.created, req)
	f.mu.Unlock()
	return managermove.Created{Move: testMove, Files: managermove.Files{ComposeYAML: "name: docker-manager\n", Env: "DOCKER_MANAGER_MOVE_CODE=dmm_move-1_secret\n"},
		StatusURL: "http://192.168.1.20:8080"}, f.createErr
}

func (f *fakeMoves) NewSetupFiles(context.Context) (managermove.Created, error) {
	if f.filesErr != nil {
		return managermove.Created{}, f.filesErr
	}
	return managermove.Created{Move: testMove, Files: managermove.Files{ComposeYAML: "name: docker-manager\n",
		Env: "DOCKER_MANAGER_MOVE_CODE=dmm_move-1_new\n"}, StatusURL: "http://192.168.1.20:8080", AgentEnrolled: true}, nil
}

func (f *fakeMoves) Current(context.Context) (managermove.View, error) {
	m := testMove
	m.State, m.MoveJobID = domain.MoveMoving, "job-1"
	return managermove.View{Move: m, NewServer: &managermove.NewServerStatus{EnvironmentID: "env-new", EnvironmentName: "new", Online: true,
		ManagerCheckedIn: true}, Source: &managermove.SourceStatus{EnvironmentID: "env-old", Name: "old", StackCount: 4},
		Progress: &managermove.Progress{JobID: "job-1", JobState: domain.JobRunning, MigrationID: "mig-1", StacksMoved: 1, StacksTotal: 4,
			CurrentStack: "web"}}, nil
}

func (f *fakeMoves) StartRun(context.Context) (domain.Job, error) {
	if f.runErr != nil {
		return domain.Job{}, f.runErr
	}
	return domain.Job{ID: "job-1", Kind: "manager.move", State: domain.JobQueued}, nil
}

func (f *fakeMoves) Cancel(context.Context, managermove.CancelRequest) (domain.ManagerMove, error) {
	m := testMove
	m.State = domain.MoveCancelled
	return m, nil
}

func (f *fakeMoves) CheckIn(_ context.Context, a managermove.MoveAuth) (managermove.CheckIn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, a)
	if a.Header == "" {
		return managermove.CheckIn{}, domain.ErrMoveCodeInvalid
	}
	return managermove.CheckIn{State: domain.MoveMoving, StacksMoved: 2, StacksTotal: 5, CurrentStack: "web"}, nil
}

func (f *fakeMoves) Handoff(_ context.Context, a managermove.MoveAuth) (*managermove.Package, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, a)
	if a.Header == "" {
		return nil, domain.ErrMoveCodeInvalid
	}
	return nil, f.handoff
}

func (f *fakeMoves) Confirm(_ context.Context, a managermove.MoveAuth) (domain.ManagerMove, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, a)
	m := testMove
	m.State = domain.MoveConfirmed
	return m, nil
}

func (f *fakeMoves) AcknowledgeConfirmation(context.Context) (managermove.View, error) {
	if f.ackErr != nil {
		return managermove.View{}, f.ackErr
	}
	m := testMove
	at := testutil.Epoch
	m.State, m.ConfirmError, m.ConfirmAcknowledgedAt = domain.MoveArrived, managermove.ConfirmStateRefused, &at
	return managermove.View{Move: m, Complete: &managermove.Complete{
		Redirects: []managermove.RedirectStatus{{ManagerMoveRedirect: domain.ManagerMoveRedirect{EnvironmentID: "env-old", Role: domain.RedirectOldServer,
			URL: "http://192.168.1.20:8080", ErrorClass: managermove.RedirectOffline}, NeedsFix: true}},
		OldEnvironment: &managermove.OldEnvironment{EnvironmentID: "env-old", Name: "old", StoppedCopies: 3, MigrationID: "mig-1"}}}, nil
}

func (f *fakeMoves) LockStatus(context.Context) (managermove.LockStatus, error) {
	return managermove.LockStatus{State: managermove.LockMoving, Address: "https://docker.example.com"}, nil
}

func (f *fakeMoves) WaitStatus(context.Context) (managermove.WaitStatus, error) {
	return managermove.WaitStatus{Phase: managermove.WaitWaiting, OldManager: "http://192.168.1.10:8080", OldState: "moving", StacksMoved: 2,
		StacksTotal: 5}, nil
}

func moveHandler(t *testing.T, svc ManagerMoveService) http.Handler {
	t.Helper()
	pol := authztest.New().Owner("olga").Member("rita", "admins").Group("admins", "allow settings.manage @all")
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, ManagerMove: svc})
	return authztest.Authenticate(withTestContext(t, mux, ""))
}

// TestManagerMoveOwnerRoutes: preparing, creating, running, reading and
// cancelling a move is the owner's only (403 for everyone else, 401
// anonymous), checked before availability (503 only for the owner when
// the service is missing); the files are returned once, on creation.
func TestManagerMoveOwnerRoutes(t *testing.T) {
	calls := []authztest.Call{
		{Method: http.MethodGet, Path: "/api/v1/manager/move/defaults"},
		{Method: http.MethodPost, Path: "/api/v1/manager/moves", Body: map[string]any{"thisServerAddress": "192.168.1.10", "newServerAddress": "192.168.1.20"}},
		{Method: http.MethodGet, Path: "/api/v1/manager/move"},
		{Method: http.MethodPost, Path: "/api/v1/manager/move/runs"},
		{Method: http.MethodPost, Path: "/api/v1/manager/move/cancellations", Body: map[string]any{}},
		{Method: http.MethodPost, Path: "/api/v1/manager/move/acknowledgements"},
		{Method: http.MethodPost, Path: "/api/v1/manager/move/setup-files"},
	}
	svc := &fakeMoves{}
	h := moveHandler(t, svc)
	authztest.AssertOnly(t, h, "olga", calls, nil)
	authztest.AssertOnly(t, h, "rita", nil, calls)
	for _, c := range calls {
		if r := authztest.Do(t, h, "", c); r.Status != http.StatusUnauthorized {
			t.Errorf("%s anonymous: %d", c, r.Status)
		}
	}
	r := authztest.Do(t, h, "olga", calls[1])
	var created CreatedManagerMove
	if r.Status != http.StatusCreated || json.Unmarshal(r.Body, &created) != nil || created.Env == "" || created.ComposeYAML == "" ||
		created.StatusURL != "http://192.168.1.20:8080" || created.Move.State != "open" || created.Move.StatusURL != "http://192.168.1.20:8080" {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	if svc.created[0].ThisServerAddress != "192.168.1.10" || svc.created[0].NewServerAddress != "192.168.1.20" {
		t.Fatalf("create request %+v", svc.created[0])
	}
	var mv ManagerMove
	if r := authztest.Do(t, h, "olga", calls[2]); r.Status != http.StatusOK || json.Unmarshal(r.Body, &mv) != nil || mv.ID != "move-1" ||
		mv.State != "moving" || mv.Progress == nil || mv.Progress.StacksMoved != 1 || mv.Progress.CurrentStack != "web" || mv.NewServer == nil ||
		!mv.NewServer.ManagerCheckedIn || mv.SourceEnvironment == nil || mv.SourceEnvironment.StackCount != 4 {
		t.Fatalf("get: %d %s", r.Status, r.Body)
	}
	var d ManagerMoveDefaults
	if r := authztest.Do(t, h, "olga", calls[0]); r.Status != http.StatusOK || json.Unmarshal(r.Body, &d) != nil ||
		d.ThisServerAddress != "192.168.1.10" || d.SourceEnvironment == nil {
		t.Fatalf("defaults: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "olga", calls[3]); r.Status != http.StatusAccepted {
		t.Fatalf("run: %d %s", r.Status, r.Body)
	}
	svc.runErr = domain.ErrManagerMoveNewServerMissing
	if r := authztest.Do(t, h, "olga", calls[3]); r.Status != http.StatusConflict {
		t.Fatalf("run without the new server: %d %s", r.Status, r.Body)
	}

	missing := moveHandler(t, nil)
	for _, c := range calls {
		if r := authztest.Do(t, missing, "rita", c); r.Status != http.StatusForbidden {
			t.Errorf("%s as rita without the service: %d, want 403 before 503", c, r.Status)
		}
		if r := authztest.Do(t, missing, "olga", c); r.Status != http.StatusServiceUnavailable {
			t.Errorf("%s as owner without the service: %d", c, r.Status)
		}
	}
}

// TestManagerMoveSignedRoutes: the check-in, the handoff and the
// confirmation hand the Authorization header, method and path to the
// service (no session needed); the check-in answers the state and the
// apps' progress; a handoff before the apps moved answers 409
// manager_move_not_ready with the progress in headers, one waiting for
// jobs 409 jobs_running with Retry-After and the count; a missing or
// clock-skewed signature is refused with 401.
func TestManagerMoveSignedRoutes(t *testing.T) {
	svc := &fakeMoves{handoff: &domain.MoveNotReadyError{State: domain.MoveMoving, StacksMoved: 2, StacksTotal: 5, CurrentStack: "web",
		RetryAfter: 10 * time.Second}}
	h := moveHandler(t, svc)
	signed := map[string]string{"Authorization": "DMM move-1:1700000000:bm9uY2U:bWFj"}
	handoff := authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/handoff", Headers: signed}
	r := authztest.Do(t, h, "", handoff)
	var e Error
	if r.Status != http.StatusConflict || json.Unmarshal(r.Body, &e) != nil || e.Code != CodeManagerMoveNotReady ||
		r.Header.Get("Retry-After") != "10" || r.Header.Get(managermove.MoveStateHeader) != "moving" ||
		r.Header.Get(managermove.MoveStacksHeader) != "2/5" || r.Header.Get(managermove.MoveCurrentStackHeader) != "web" {
		t.Fatalf("handoff before ready: %d %v %s", r.Status, r.Header, r.Body)
	}
	svc.handoff = &domain.JobsRunningError{Count: 3, RetryAfter: 10 * time.Second}
	r = authztest.Do(t, h, "", handoff)
	if r.Status != http.StatusConflict || r.Header.Get("Retry-After") != "10" || r.Header.Get(managermove.JobsRunningHeader) != "3" ||
		json.Unmarshal(r.Body, &e) != nil || e.Code != CodeJobsRunning {
		t.Fatalf("handoff while jobs run: %d %v %s", r.Status, r.Header, r.Body)
	}
	svc.handoff = domain.ErrMoveClockSkew
	r = authztest.Do(t, h, "", handoff)
	if r.Status != http.StatusUnauthorized || json.Unmarshal(r.Body, &e) != nil || e.Code != CodeMoveClockSkew {
		t.Fatalf("clock skew: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/handoff"}); r.Status != http.StatusUnauthorized {
		t.Fatalf("handoff without a signature: %d %s", r.Status, r.Body)
	}
	var ci ManagerMoveCheckIn
	if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/manager/move/check-in", Headers: signed}); r.Status != http.StatusOK ||
		json.Unmarshal(r.Body, &ci) != nil || ci.State != "moving" || ci.StacksMoved != 2 || ci.StacksTotal != 5 || ci.CurrentStack != "web" {
		t.Fatalf("check-in: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodGet, Path: "/api/v1/manager/move/check-in"}); r.Status != http.StatusUnauthorized {
		t.Fatalf("check-in without a signature: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/confirm", Headers: signed}); r.Status != http.StatusOK {
		t.Fatalf("confirm: %d %s", r.Status, r.Body)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	last := svc.auths[len(svc.auths)-1]
	if last.Header != signed["Authorization"] || last.Method != http.MethodPost || last.Path != "/api/v1/manager/move/confirm" {
		t.Fatalf("confirm auth %+v", last)
	}
	if svc.auths[0].Path != "/api/v1/manager/move/handoff" {
		t.Fatalf("handoff auth %+v", svc.auths[0])
	}
}

// TestManagerMoveAcknowledgement: the owner acknowledges a confirmation
// the old manager never gave; the move shows when, with Move complete. A
// move that allows no acknowledgement answers 409 manager_move_state, no
// arrived move 404.
func TestManagerMoveAcknowledgement(t *testing.T) {
	svc := &fakeMoves{}
	h := moveHandler(t, svc)
	call := authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/acknowledgements"}
	r := authztest.Do(t, h, "olga", call)
	var mv ManagerMove
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &mv) != nil || mv.ConfirmAcknowledgedAt == nil || mv.State != "arrived" ||
		mv.OldManagerConfirmed || mv.OldEnvironment == nil || mv.OldEnvironment.StoppedCopies != 3 || len(mv.Redirects) != 1 ||
		!mv.Redirects[0].NeedsFix || mv.Redirects[0].ErrorCode != "offline" || mv.StatusURL != "" {
		t.Fatalf("acknowledge: %d %s", r.Status, r.Body)
	}
	svc.ackErr = domain.ErrManagerMoveState
	if r := authztest.Do(t, h, "olga", call); r.Status != http.StatusConflict {
		t.Fatalf("not allowed: %d %s", r.Status, r.Body)
	}
	svc.ackErr = domain.ErrManagerMoveNotFound
	if r := authztest.Do(t, h, "olga", call); r.Status != http.StatusNotFound {
		t.Fatalf("no arrived move: %d %s", r.Status, r.Body)
	}
}

// TestManagerMoveSetupFiles: new setup files answer the creation's shape
// (200, the files once, agentEnrolled when the new server's agent keeps
// its enrollment); a move that allows none answers 409
// manager_move_state, no move 404.
func TestManagerMoveSetupFiles(t *testing.T) {
	svc := &fakeMoves{}
	h := moveHandler(t, svc)
	call := authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/setup-files"}
	r := authztest.Do(t, h, "olga", call)
	var c CreatedManagerMove
	if r.Status != http.StatusOK || json.Unmarshal(r.Body, &c) != nil || c.Env == "" || c.ComposeYAML == "" || !c.AgentEnrolled ||
		c.StatusURL != "http://192.168.1.20:8080" || c.Move.ID != "move-1" {
		t.Fatalf("setup files: %d %s", r.Status, r.Body)
	}
	svc.filesErr = domain.ErrManagerMoveState
	var e Error
	if r := authztest.Do(t, h, "olga", call); r.Status != http.StatusConflict || json.Unmarshal(r.Body, &e) != nil || e.Code != CodeManagerMoveState {
		t.Fatalf("not allowed: %d %s", r.Status, r.Body)
	}
	svc.filesErr = domain.ErrManagerMoveNotFound
	if r := authztest.Do(t, h, "olga", call); r.Status != http.StatusNotFound {
		t.Fatalf("no move: %d %s", r.Status, r.Body)
	}
}

// TestMoveStatusIsPublic: anyone reads the move status; without the
// service it is none.
func TestMoveStatusIsPublic(t *testing.T) {
	call := authztest.Call{Method: http.MethodGet, Path: "/api/v1/move/status"}
	var st MoveStatus
	if r := authztest.Do(t, moveHandler(t, &fakeMoves{}), "", call); r.Status != http.StatusOK || json.Unmarshal(r.Body, &st) != nil ||
		st.Phase != "waiting" || st.OldState != "moving" {
		t.Fatalf("status: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, moveHandler(t, nil), "", call); r.Status != http.StatusOK || json.Unmarshal(r.Body, &st) != nil || st.Phase != "none" {
		t.Fatalf("status without the service: %d %s", r.Status, r.Body)
	}
}

// TestSessionManagerMove: the session carries the move lock (no move ID,
// no secrets); without the service it is left out.
func TestSessionManagerMove(t *testing.T) {
	ctx := testutil.Context(t)
	got := sessionManagerMove(ctx, &fakeMoves{})
	if got == nil || got.State != "moving" || got.Address != "https://docker.example.com" {
		t.Fatalf("session move %+v", got)
	}
	b, _ := json.Marshal(got)
	if string(b) != `{"state":"moving","address":"https://docker.example.com"}` {
		t.Fatalf("session move JSON %s", b)
	}
	if sessionManagerMove(ctx, nil) != nil {
		t.Fatal("a missing service reported a move")
	}
}
