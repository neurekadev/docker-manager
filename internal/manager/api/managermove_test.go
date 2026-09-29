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

type fakeMoveLock bool

func (l fakeMoveLock) ReadOnly() bool { return bool(l) }

// TestMoveLockRefusesMutations: while the manager moves, every non-GET
// operation answers 409 manager_moved (audited like any refusal) except
// sign-in, sign-out and the move routes; reads keep working.
func TestMoveLockRefusesMutations(t *testing.T) {
	h, _ := newTestAPI(t, Deps{MoveLock: fakeMoveLock(true)})
	decodeError(t, do(t, h, http.MethodPost, "/api/v1/test/echo", `{"name":"x"}`), http.StatusConflict, CodeManagerMoved)
	if rec := do(t, h, http.MethodGet, "/api/v1/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("health while moving: %d", rec.Code)
	}
	for _, path := range []string{"/api/v1/auth/session", "/api/v1/manager/move/handoff", "/api/v1/manager/move/confirm"} {
		rec := do(t, h, http.MethodPost, path, `{"username":"a","password":"b"}`)
		if rec.Code == http.StatusConflict {
			t.Errorf("POST %s refused while moving: %s", path, rec.Body)
		}
	}
	open, _ := newTestAPI(t, Deps{MoveLock: fakeMoveLock(false)})
	if rec := do(t, open, http.MethodPost, "/api/v1/test/echo", `{"name":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("echo without the lock: %d %s", rec.Code, rec.Body)
	}
}

// TestAllowedWhileMovedOperationsExist keeps the allowlist in step with
// the catalog: every listed operation is registered and not a GET.
func TestAllowedWhileMovedOperationsExist(t *testing.T) {
	oapi := New(http.NewServeMux(), Deps{}).OpenAPI()
	found := map[string]bool{}
	for _, item := range oapi.Paths {
		for _, op := range []*huma.Operation{item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil && AllowedWhileMoved(op.OperationID) {
				found[op.OperationID] = true
			}
		}
	}
	for id := range allowedWhileMoved {
		if !found[id] {
			t.Errorf("%s is allowed while moving but is not a registered mutating operation", id)
		}
	}
}

// fakeMoves records the calls of the move routes.
type fakeMoves struct {
	mu        sync.Mutex
	codes     []string
	handoff   error
	createErr error
}

var testMove = domain.ManagerMove{ID: "move-1", State: domain.MoveOpen, CreatedAt: testutil.Epoch, ExpiresAt: testutil.Epoch.Add(time.Hour)}

func (f *fakeMoves) CreateMove(context.Context) (domain.ManagerMove, string, error) {
	return testMove, "dmm_move-1_secret", f.createErr
}

func (f *fakeMoves) Current(context.Context) (managermove.View, error) {
	return managermove.View{Move: testMove}, nil
}

func (f *fakeMoves) Cancel(context.Context, managermove.CancelRequest) (domain.ManagerMove, error) {
	m := testMove
	m.State = domain.MoveCancelled
	return m, nil
}

func (f *fakeMoves) Handoff(_ context.Context, code string) (*managermove.Package, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes = append(f.codes, code)
	if code == "" {
		return nil, domain.ErrMoveCodeInvalid
	}
	return nil, f.handoff
}

func (f *fakeMoves) Confirm(_ context.Context, code string) (domain.ManagerMove, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes = append(f.codes, code)
	m := testMove
	m.State = domain.MoveConfirmed
	return m, nil
}

func (f *fakeMoves) StartReceive(context.Context, string, string) (domain.Job, error) {
	return domain.Job{}, domain.ErrManagerMoveInProgress
}

func (f *fakeMoves) LatestReceive(context.Context) (*managermove.ReceiveStatus, error) {
	return nil, nil
}

func moveHandler(t *testing.T, svc ManagerMoveService) http.Handler {
	t.Helper()
	pol := authztest.New().Owner("olga").Member("rita", "admins").Group("admins", "allow settings.manage @all")
	mux := http.NewServeMux()
	New(mux, Deps{Authorizer: pol, ManagerMove: svc})
	return authztest.Authenticate(withTestContext(t, mux, ""))
}

// TestManagerMoveOwnerRoutes: creating, reading and cancelling a move is
// the owner's only (403 for everyone else, 401 anonymous), checked before
// availability (503 only for the owner when the service is missing); the
// code is returned once, on creation.
func TestManagerMoveOwnerRoutes(t *testing.T) {
	calls := []authztest.Call{
		{Method: http.MethodPost, Path: "/api/v1/manager/moves"},
		{Method: http.MethodGet, Path: "/api/v1/manager/move"},
		{Method: http.MethodPost, Path: "/api/v1/manager/move/cancellations", Body: map[string]any{}},
	}
	h := moveHandler(t, &fakeMoves{})
	authztest.AssertOnly(t, h, "olga", calls, nil)
	authztest.AssertOnly(t, h, "rita", nil, calls)
	for _, c := range calls {
		if r := authztest.Do(t, h, "", c); r.Status != http.StatusUnauthorized {
			t.Errorf("%s anonymous: %d", c, r.Status)
		}
	}
	r := authztest.Do(t, h, "olga", calls[0])
	var created CreatedManagerMove
	if r.Status != http.StatusCreated || json.Unmarshal(r.Body, &created) != nil || created.Code != "dmm_move-1_secret" || created.Move.State != "open" {
		t.Fatalf("create: %d %s", r.Status, r.Body)
	}
	if r := authztest.Do(t, h, "olga", calls[1]); r.Status != http.StatusOK || json.Unmarshal(r.Body, &created.Move) != nil || created.Move.ID != "move-1" {
		t.Fatalf("get: %d %s", r.Status, r.Body)
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

// TestManagerMoveCodeRoutes: the handoff and the confirmation take the
// move code from Authorization: Bearer (no session needed); a handoff
// waiting for jobs answers 409 jobs_running with Retry-After and the
// count; a missing code is refused with 401.
func TestManagerMoveCodeRoutes(t *testing.T) {
	svc := &fakeMoves{handoff: &domain.JobsRunningError{Count: 3, RetryAfter: 10 * time.Second}}
	h := moveHandler(t, svc)
	bearer := map[string]string{"Authorization": "Bearer dmm_move-1_secret"}
	r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/handoff", Headers: bearer})
	if r.Status != http.StatusConflict || r.Header.Get("Retry-After") != "10" || r.Header.Get(managermove.JobsRunningHeader) != "3" {
		t.Fatalf("handoff while jobs run: %d %v %s", r.Status, r.Header, r.Body)
	}
	var e Error
	if json.Unmarshal(r.Body, &e) != nil || e.Code != CodeJobsRunning {
		t.Fatalf("error %s", r.Body)
	}
	if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/handoff"}); r.Status != http.StatusUnauthorized {
		t.Fatalf("handoff without a code: %d %s", r.Status, r.Body)
	}
	other := map[string]string{"Authorization": "Bearer dy_token_x"}
	if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/handoff", Headers: other}); r.Status != http.StatusUnauthorized {
		t.Fatalf("handoff with an API token: %d", r.Status)
	}
	if r := authztest.Do(t, h, "", authztest.Call{Method: http.MethodPost, Path: "/api/v1/manager/move/confirm", Headers: bearer}); r.Status != http.StatusOK {
		t.Fatalf("confirm: %d %s", r.Status, r.Body)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	want := []string{"dmm_move-1_secret", "", "", "dmm_move-1_secret"}
	if len(svc.codes) != len(want) {
		t.Fatalf("codes %q", svc.codes)
	}
	for i := range want {
		if svc.codes[i] != want[i] {
			t.Fatalf("codes %q, want %q", svc.codes, want)
		}
	}
}
