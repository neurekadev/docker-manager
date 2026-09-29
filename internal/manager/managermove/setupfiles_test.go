package managermove

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/events"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// checkIn is the waiting manager's check-in signed with code.
func (f *fixture) checkIn(code string) (CheckIn, error) {
	f.t.Helper()
	h, err := SignRequest(code, http.MethodGet, CheckInPath, f.clk.Now())
	if err != nil {
		f.t.Fatal(err)
	}
	return f.svc.CheckIn(f.from(), MoveAuth{Header: h, Method: http.MethodGet, Path: CheckInPath})
}

// TestNewSetupFiles: new setup files of an open move carry a new code of
// the same move (sealed; the old one is refused at once, also on the
// handoff) and a new 24-hour enrollment token under the same name (the
// old token revoked); the last check-in is forgotten, the expiry kept.
// The owner's step-up comes first.
func TestNewSetupFiles(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	c, err := f.svc.CreateMove(f.ctx, CreateRequest{ThisServerAddress: "192.168.1.10", NewServerAddress: "192.168.1.20",
		NewEnvironmentName: "nas"})
	if err != nil {
		t.Fatal(err)
	}
	m, oldCode := c.Move, envValue(c.Files.Env, "DOCKER_MANAGER_MOVE_CODE")
	if _, err := f.checkIn(oldCode); err != nil {
		t.Fatal(err)
	}
	f.guard.err = domain.ErrStepUpRequired
	if _, err := f.svc.NewSetupFiles(f.ctx); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Fatalf("without a step-up: %v", err)
	}
	f.guard.err = nil
	f.clk.Advance(time.Hour)
	n, err := f.svc.NewSetupFiles(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	code := envValue(n.Files.Env, "DOCKER_MANAGER_MOVE_CODE")
	if code == oldCode || !strings.HasPrefix(code, "dmm_"+m.ID+"_") || n.AgentEnrolled || n.StatusURL != "http://192.168.1.20:8080" ||
		envValue(n.Files.Env, "DOCKER_MANAGER_MOVE_FROM") != "http://192.168.1.10:8080" {
		t.Fatalf("new files %+v", n)
	}
	token := envValue(n.Files.Env, "DOCKER_AGENT_ENROLLMENT_TOKEN")
	if !strings.HasPrefix(token, "dye_") || token == envValue(c.Files.Env, "DOCKER_AGENT_ENROLLMENT_TOKEN") {
		t.Fatalf("new token %q", token)
	}
	if len(f.enroll.specs) != 2 || f.enroll.specs[1].EnvironmentName != "nas" || f.enroll.specs[1].TTL != EnrollmentLifetime {
		t.Fatalf("enrollments %+v", f.enroll.specs)
	}
	if len(f.enroll.revoked) != 1 || f.enroll.revoked[0] != m.EnrollmentID {
		t.Fatalf("revoked %v", f.enroll.revoked)
	}
	got := f.move(m.ID)
	if got.EnrollmentID == m.EnrollmentID || got.EnrollmentID != n.Move.EnrollmentID || got.CheckedInAt != nil || got.HandoffAddress != "" ||
		!got.ExpiresAt.Equal(m.ExpiresAt) || got.State != domain.MoveOpen {
		t.Fatalf("move after new files %+v", got)
	}
	sealed, _ := f.svc.opts.Keyring.Open(mustSealed(t, f, m.ID), SealContext(m.ID))
	if string(sealed) != code {
		t.Fatal("the new code is not the sealed one")
	}
	if _, err := f.checkIn(oldCode); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("check-in with the old code: %v", err)
	}
	if _, err := f.handoff(oldCode); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("handoff with the old code: %v", err)
	}
	if f.svc.sameCode(f.ctx, m.ID, oldCode) || !f.svc.sameCode(f.ctx, m.ID, code) {
		t.Fatal("sameCode does not follow the new code")
	}
	if ci, err := f.checkIn(code); err != nil || ci.State != domain.MoveOpen {
		t.Fatalf("check-in with the new code: %+v %v", ci, err)
	}
}

func mustSealed(t *testing.T, f *fixture, id string) string {
	t.Helper()
	sealed, err := store.ManagerMoveSealedCode(f.ctx, f.db, id)
	if err != nil || sealed == "" {
		t.Fatalf("sealed code %q: %v", sealed, err)
	}
	return sealed
}

// TestNewSetupFilesKeepTheEnrolledAgent: once the new server's agent
// enrolled with the move's token, new setup files keep its environment
// and token: the .env has no enrollment token and no environment name.
// An environment archived since gets a new token again.
func TestNewSetupFilesKeepTheEnrolledAgent(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, _ := f.create()
	f.enrollNewServer(m)
	n, err := f.svc.NewSetupFiles(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !n.AgentEnrolled || envValue(n.Files.Env, "DOCKER_AGENT_ENROLLMENT_TOKEN") != "" || len(f.enroll.specs) != 1 {
		t.Fatalf("files of an enrolled agent %+v (enrollments %d)", n, len(f.enroll.specs))
	}
	if got := f.move(m.ID); got.TargetEnvironmentID != "env-new" || got.EnrollmentID != m.EnrollmentID {
		t.Fatalf("move %+v", got)
	}
	if en, _ := f.enroll.GetEnrollment(f.ctx, m.EnrollmentID); en.RevokedAt != nil {
		t.Fatal("a used enrollment was revoked")
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE environments SET status = 'archived' WHERE id = 'env-new'"); err != nil {
		t.Fatal(err)
	}
	n, err = f.svc.NewSetupFiles(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n.AgentEnrolled || !strings.HasPrefix(envValue(n.Files.Env, "DOCKER_AGENT_ENROLLMENT_TOKEN"), "dye_") || len(f.enroll.specs) != 2 ||
		f.enroll.specs[1].EnvironmentName != "192.168.1.20" {
		t.Fatalf("files after the environment was archived %+v", n)
	}
	if got := f.move(m.ID); got.TargetEnvironmentID != "" || got.EnrollmentID == m.EnrollmentID {
		t.Fatalf("move %+v", got)
	}
}

// TestNewSetupFilesStates: allowed while the move is open or ready;
// refused while the apps move (the new server's agent would restart) and
// from the handoff on; no move is not found.
func TestNewSetupFilesStates(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	if _, err := f.svc.NewSetupFiles(f.ctx); !errors.Is(err, domain.ErrManagerMoveNotFound) {
		t.Fatalf("no move: %v", err)
	}
	m, _ := f.create()
	for st, ok := range map[domain.ManagerMoveState]bool{domain.MoveOpen: true, domain.MoveReady: true, domain.MoveMoving: false,
		domain.MoveDraining: false, domain.MoveHandedOff: false} {
		f.setState(m.ID, st)
		_, err := f.svc.NewSetupFiles(f.ctx)
		if ok && err != nil || !ok && !errors.Is(err, domain.ErrManagerMoveState) {
			t.Errorf("%s: %v", st, err)
		}
	}
}

// moveEvents collects the move's live events.
func moveEvents(f *fixture) *events.Subscription {
	return f.bus.Subscribe(256, func(e events.Event) bool {
		return e.Type == events.ManagerMoveUpdated || e.Type == events.ManagerMoveLockChanged
	})
}

// drain returns the events received so far.
func drain(sub *events.Subscription) []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-sub.C():
			out = append(out, e)
		default:
			return out
		}
	}
}

func count(evs []events.Event, typ string) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// TestMoveLiveEvents: the move is announced (its ID only, owner's event)
// when it is created, when the waiting manager's check-in starts counting
// (not on every check-in), when new setup files replace the code, when it
// starts draining and ends; the lock event (no ID) only when the lock the
// session shows changes. A check-in that stopped counting is announced
// once.
func TestMoveLiveEvents(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	sub := moveEvents(f)
	defer sub.Close()
	m, code := f.create()
	if evs := drain(sub); count(evs, events.ManagerMoveUpdated) != 1 || evs[0].ResourceID != m.ID || count(evs, events.ManagerMoveLockChanged) != 0 {
		t.Fatalf("create: %+v", evs)
	}
	if _, err := f.checkIn(code); err != nil {
		t.Fatal(err)
	}
	if _, err := f.checkIn(code); err != nil {
		t.Fatal(err)
	}
	if evs := drain(sub); count(evs, events.ManagerMoveUpdated) != 1 {
		t.Fatalf("two check-ins: %+v", evs)
	}
	f.clk.Advance(CheckInFresh + time.Second)
	f.svc.announceStaleCheckIn(f.ctx)
	f.svc.announceStaleCheckIn(f.ctx)
	if evs := drain(sub); count(evs, events.ManagerMoveUpdated) != 1 {
		t.Fatalf("stale check-in: %+v", evs)
	}
	if _, err := f.checkIn(code); err != nil {
		t.Fatal(err)
	}
	if evs := drain(sub); count(evs, events.ManagerMoveUpdated) != 1 {
		t.Fatalf("check-in counts again: %+v", evs)
	}
	n, err := f.svc.NewSetupFiles(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	code = envValue(n.Files.Env, "DOCKER_MANAGER_MOVE_CODE")
	if evs := drain(sub); count(evs, events.ManagerMoveUpdated) != 1 || count(evs, events.ManagerMoveLockChanged) != 0 {
		t.Fatalf("new setup files: %+v", evs)
	}
	f.setState(m.ID, domain.MoveReady)
	f.runningJob()
	if _, err := f.handoff(code); err == nil {
		t.Fatal("handoff while a job runs")
	}
	evs := drain(sub)
	if count(evs, events.ManagerMoveLockChanged) != 1 || count(evs, events.ManagerMoveUpdated) < 1 {
		t.Fatalf("draining: %+v", evs)
	}
	for _, e := range evs {
		if e.Type == events.ManagerMoveLockChanged && (e.ResourceID != "instance" || e.ResourceType != events.ResourceManagerMoveLock) {
			t.Fatalf("the lock event names the move: %+v", e)
		}
	}
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); err != nil {
		t.Fatal(err)
	}
	if evs := drain(sub); count(evs, events.ManagerMoveLockChanged) != 1 || count(evs, events.ManagerMoveUpdated) != 1 {
		t.Fatalf("cancel while draining: %+v", evs)
	}
}

// TestMoveFollowsTheBus: the new server's agent going online or a change
// of Move everything (manager.move) republishes the current move; other
// jobs do not.
func TestMoveFollowsTheBus(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, _ := f.create()
	sub := moveEvents(f)
	defer sub.Close()
	go f.svc.followBus(f.ctx)
	waitFor := func(e events.Event) {
		t.Helper()
		for {
			f.bus.Publish(e)
			select {
			case got := <-sub.C():
				if got.Type != events.ManagerMoveUpdated || got.ResourceID != m.ID {
					t.Fatalf("republished %+v", got)
				}
				return
			case <-f.ctx.Done():
				t.Fatal("not republished")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	waitFor(events.Event{Type: events.EnvironmentOnline, ResourceType: events.ResourceEnvironment, ResourceID: "env-new", EnvironmentID: "env-new"})
	waitFor(events.Event{Type: events.JobUpdated, ResourceType: events.ResourceJob, ResourceID: "j1", Job: &domain.Job{ID: "j1", Kind: moveKind}})
	if followed(events.Event{Type: events.JobUpdated, Job: &domain.Job{Kind: "stack.deploy"}}) || followed(events.Event{Type: events.DockerEvent}) {
		t.Fatal("unrelated events are followed")
	}
}
