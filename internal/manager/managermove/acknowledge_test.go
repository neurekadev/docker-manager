package managermove

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// arrive stores an arrived move from source whose code is sealed for the
// confirmation, like FinishArrival leaves it.
func arrive(t *testing.T, f *fixture, source string) domain.ManagerMove {
	t.Helper()
	now := f.clk.Now().UTC()
	m := domain.ManagerMove{ID: "move-arrived", State: domain.MoveArrived, CreatedAt: now.Add(-time.Hour), ExpiresAt: now, ArrivedAt: &now,
		SourceURL: source, UpdatedAt: now}
	minted, err := authsep.MintMoveCode(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := f.keyring.Seal([]byte(minted.Token), SealContext(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertManagerMove(f.ctx, f.db, &m, sealed); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestAcknowledgeConfirmation: when the old manager refused (or cannot be
// reached for) the confirmation, the owner (step-up) states that it no
// longer runs the instance: the confirmation stops, the sealed code is
// forgotten and the move shows the acknowledgement. Not before a failed
// attempt, not without an arrived move, and only for the owner.
func TestAcknowledgeConfirmation(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"code":"manager_move_state"}`))
	}))
	t.Cleanup(srv.Close)
	f := newFixture(t, testutil.FakeClock(), srv.Client())
	if _, err := f.svc.AcknowledgeConfirmation(f.ctx); !errors.Is(err, domain.ErrManagerMoveNotFound) {
		t.Fatalf("without an arrived move: %v", err)
	}
	m := arrive(t, f, srv.URL)
	if _, err := f.svc.AcknowledgeConfirmation(f.ctx); !errors.Is(err, domain.ErrManagerMoveState) {
		t.Fatalf("before any attempt: %v", err)
	}

	// The old manager answers 503: retried; then it refuses (409): stopped.
	if done, err := f.svc.ConfirmOnce(f.ctx); err != nil || done {
		t.Fatalf("503: done %v %v", done, err)
	}
	status.Store(http.StatusConflict)
	if done, err := f.svc.ConfirmOnce(f.ctx); err != nil || !done {
		t.Fatalf("409: done %v %v", done, err)
	}
	if a := f.move(m.ID); a.ConfirmError != ConfirmStateRefused || a.ConfirmedAt != nil {
		t.Fatalf("refused %+v", a)
	}

	f.guard.err = domain.ErrStepUpRequired
	if _, err := f.svc.AcknowledgeConfirmation(f.ctx); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Fatalf("without a step-up: %v", err)
	}
	if f.move(m.ID).ConfirmAcknowledgedAt != nil {
		t.Fatal("acknowledged without the owner's step-up")
	}
	f.guard.err = nil
	v, err := f.svc.AcknowledgeConfirmation(f.ctx)
	if err != nil || v.Move.ID != m.ID || v.Move.ConfirmAcknowledgedAt == nil || v.Complete == nil {
		t.Fatalf("acknowledge: %+v %v", v, err)
	}
	if sealed, _ := store.ManagerMoveSealedCode(f.ctx, f.db, m.ID); sealed != "" {
		t.Fatal("the sealed move code outlived the acknowledgement")
	}
	first := *f.move(m.ID).ConfirmAcknowledgedAt
	f.clk.Advance(time.Minute)
	if v, err := f.svc.AcknowledgeConfirmation(f.ctx); err != nil || !v.Move.ConfirmAcknowledgedAt.Equal(first) {
		t.Fatalf("repeated: %+v %v", v.Move, err)
	}
	before := calls.Load()
	if done, err := f.svc.ConfirmOnce(f.ctx); err != nil || !done || calls.Load() != before {
		t.Fatalf("confirmation after the acknowledgement: done %v %v, calls %d → %d", done, err, before, calls.Load())
	}
}

// TestAcknowledgeWhileUnreachable: an old manager that never answers
// (stopped too early) can be acknowledged too; the retries stop.
func TestAcknowledgeWhileUnreachable(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	client := srv.Client()
	srv.Close()
	f := newFixture(t, testutil.FakeClock(), client)
	m := arrive(t, f, srv.URL)
	if done, err := f.svc.ConfirmOnce(f.ctx); err != nil || done {
		t.Fatalf("unreachable: done %v %v", done, err)
	}
	if a := f.move(m.ID); a.ConfirmError != ConfirmUnreachable {
		t.Fatalf("after an unreachable attempt %+v", a)
	}
	if _, err := f.svc.AcknowledgeConfirmation(f.ctx); err != nil {
		t.Fatal(err)
	}
	if done, err := f.svc.ConfirmOnce(f.ctx); err != nil || !done {
		t.Fatalf("after the acknowledgement: done %v %v", done, err)
	}
}

// TestLockStatus: the lock every signed-in user sees: none while the move
// is open or ready, moving while it hands over (with the shared address),
// moved once confirmed; an expired draining move no longer locks.
func TestLockStatus(t *testing.T) {
	clk := testutil.FakeClock()
	f := newFixture(t, clk, nil)
	lock := func() LockStatus {
		t.Helper()
		st, err := f.svc.LockStatus(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := lock(); st != (LockStatus{State: LockNone}) {
		t.Fatalf("no move: %+v", st)
	}
	m, code := f.create()
	f.setState(m.ID, domain.MoveReady)
	if st := lock(); st.State != LockNone || st.Address != "" {
		t.Fatalf("ready: %+v", st)
	}
	job := f.runningJob()
	var jr *domain.JobsRunningError
	if _, err := f.handoff(code); !errors.As(err, &jr) {
		t.Fatalf("handoff while a job runs: %v", err)
	}
	if st := lock(); st != (LockStatus{State: LockMoving, Address: publicURL}) {
		t.Fatalf("draining: %+v", st)
	}
	f.finishJob(job, domain.JobSucceeded)
	if _, err := f.handoff(code); err != nil {
		t.Fatal(err)
	}
	if st := lock(); st.State != LockMoving {
		t.Fatalf("handed off: %+v", st)
	}
	if _, err := f.confirm(code); err != nil {
		t.Fatal(err)
	}
	if st := lock(); st != (LockStatus{State: LockMoved, Address: publicURL}) {
		t.Fatalf("confirmed: %+v", st)
	}

	g := newFixture(t, clk, nil)
	m, code = g.create()
	g.setState(m.ID, domain.MoveReady)
	g.runningJob()
	_, _ = g.handoff(code)
	if st, _ := g.svc.LockStatus(g.ctx); st.State != LockMoving {
		t.Fatalf("draining: %+v", st)
	}
	clk.Advance(CodeLifetime)
	if st, _ := g.svc.LockStatus(g.ctx); st.State != LockNone {
		t.Fatalf("expired: %+v", st)
	}
}
