package managermove

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	envmigrations "code.neureka.dev/docker-manager/docker-manager/internal/manager/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// openPackage decrypts a package's stream with code and stores it in a
// directory, checked against the manifest.
func openPackage(t *testing.T, pkg *Package, code, moveID string) (string, Manifest, int64) {
	t.Helper()
	var buf bytes.Buffer
	if err := pkg.Write(&buf); err != nil {
		t.Fatal(err)
	}
	key, err := packageKey(code, moveID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newOpenReader(&buf, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var seen int64
	man, err := readPackage(r, dir, func(n int64) { seen = n })
	if err != nil {
		t.Fatal(err)
	}
	return dir, man, seen
}

// TestCreateMove: the move records both addresses (port 8080 added), the
// environment next to the manager and a 24-hour enrollment token for the
// new server (named after it); the code is valid for seven days, kept
// sealed (never in clear) and appears only in the rendered .env. Nothing
// is locked; a second move is refused; the owner's step-up comes first.
func TestCreateMove(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	f.guard.err = domain.ErrStepUpRequired
	if _, err := f.svc.CreateMove(f.ctx, CreateRequest{ThisServerAddress: "192.168.1.10", NewServerAddress: "192.168.1.20"}); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Fatalf("without a step-up: %v", err)
	}
	f.guard.err = nil
	var fe *domain.FieldError
	for _, req := range []CreateRequest{
		{ThisServerAddress: "", NewServerAddress: "192.168.1.20"},
		{ThisServerAddress: "192.168.1.10", NewServerAddress: "192.168.1.10:8080"},
		{ThisServerAddress: "192.168.1.10", NewServerAddress: "https://192.168.1.20"},
	} {
		if _, err := f.svc.CreateMove(f.ctx, req); !errors.As(err, &fe) {
			t.Errorf("%+v: %v", req, err)
		}
	}
	c, err := f.svc.CreateMove(f.ctx, CreateRequest{ThisServerAddress: "192.168.1.10", NewServerAddress: "192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	m := c.Move
	if m.State != domain.MoveOpen || !m.ExpiresAt.Equal(m.CreatedAt.Add(7*24*time.Hour)) || m.ThisServerAddress != "192.168.1.10:8080" ||
		m.NewServerAddress != "192.168.1.20:8080" || m.SourceEnvironmentID != "env-old" || m.EnrollmentID == "" || c.StatusURL != "http://192.168.1.20:8080" {
		t.Fatalf("created %+v (%s)", m, c.StatusURL)
	}
	code := envValue(c.Files.Env, "DOCKER_MANAGER_MOVE_CODE")
	if !strings.HasPrefix(code, "dmm_"+m.ID+"_") || envValue(c.Files.Env, "DOCKER_MANAGER_MOVE_FROM") != "http://192.168.1.10:8080" ||
		!strings.HasPrefix(envValue(c.Files.Env, "DOCKER_AGENT_ENROLLMENT_TOKEN"), "dye_") {
		t.Fatalf("env %q", c.Files.Env)
	}
	spec := f.enroll.specs[0]
	if spec.Intent != domain.IntentNew || spec.EnvironmentName != "192.168.1.20" || spec.TTL != 24*time.Hour || spec.CreatedBy != ownerID {
		t.Fatalf("enrollment %+v", spec)
	}
	sealed, _ := store.ManagerMoveSealedCode(f.ctx, f.db, m.ID)
	if sealed == "" || strings.Contains(sealed, code) {
		t.Fatal("the move code is not kept sealed")
	}
	if open, err := f.keyring.Open(sealed, SealContext(m.ID)); err != nil || string(open) != code {
		t.Fatalf("the sealed code does not open: %v", err)
	}
	if f.lock.Level() != movelock.Open {
		t.Fatal("a new move must not lock anything")
	}
	if _, err := f.svc.CreateMove(f.ctx, CreateRequest{ThisServerAddress: "192.168.1.10", NewServerAddress: "192.168.1.30"}); !errors.Is(err, domain.ErrManagerMoveExists) {
		t.Fatalf("second move: %v", err)
	}
	if d, err := f.svc.Defaults(f.ctx); err != nil || d.ThisServerAddress != "192.168.1.10" || d.Source == nil || d.Source.StackCount != 1 {
		t.Fatalf("defaults %+v %v", d, err)
	}
}

// TestMoveStateMachine walks a move through the old manager's states:
// open (the waiting manager's requests check in and get not_ready), the
// new server's agent enrolls, ready (the handoff is allowed), draining
// (read-only while a job runs), handed_off (the placed agents heard the
// new address, agents refused, the encrypted copy streamed; repeated with
// the same copy), confirmed (never cancelled); a restarted manager stays
// locked.
func TestMoveStateMachine(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, code := f.create()

	other, _ := authsep.MintMoveCode(m.ID)
	if _, err := f.handoff(other.Token); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("wrong code: %v", err)
	}
	var nr *domain.MoveNotReadyError
	if _, err := f.handoff(code); !errors.As(err, &nr) || nr.State != domain.MoveOpen || nr.RetryAfter != HandoffRetryAfter {
		t.Fatalf("handoff of an open move: %v", err)
	}
	got := f.move(m.ID)
	if got.State != domain.MoveOpen || got.CheckedInAt == nil || got.HandoffAddress != "192.168.1.20" {
		t.Fatalf("after the check-in %+v", got)
	}
	if _, err := f.svc.StartRun(f.owner()); !errors.Is(err, domain.ErrManagerMoveNewServerMissing) {
		t.Fatalf("Move everything before the new server enrolled: %v", err)
	}
	f.enrollNewServer(m)
	v, err := f.svc.Current(f.ctx)
	if err != nil || v.NewServer == nil || !v.NewServer.Online || !v.NewServer.ManagerCheckedIn || v.NewServer.EnvironmentID != "env-new" ||
		v.NewServer.EnrollmentState != string(domain.EnrollmentUsed) || v.Source == nil || v.Source.EnvironmentID != "env-old" || v.Source.StackCount != 1 {
		t.Fatalf("current once the new server is there: %+v %+v %v", v.NewServer, v.Source, err)
	}
	if f.move(m.ID).TargetEnvironmentID != "env-new" {
		t.Fatal("the new server's environment was not recorded")
	}
	f.clk.Advance(CheckInFresh + time.Second)
	if _, err := f.svc.StartRun(f.owner()); !errors.Is(err, domain.ErrManagerMoveNewServerMissing) {
		t.Fatalf("Move everything without a recent check-in: %v", err)
	}

	// Ready (manager.move is tested on its own): draining while a job runs.
	f.setState(m.ID, domain.MoveReady)
	job := f.runningJob()
	var jr *domain.JobsRunningError
	if _, err := f.handoff(code); !errors.As(err, &jr) || jr.Count != 1 || jr.RetryAfter != HandoffRetryAfter {
		t.Fatalf("handoff with a running job: %v", err)
	}
	if f.move(m.ID).State != domain.MoveDraining || f.lock.Level() != movelock.ReadOnly {
		t.Fatalf("draining: state %s lock %s", f.move(m.ID).State, f.lock.Level())
	}
	if v, err := f.svc.Current(f.ctx); err != nil || v.JobsRunning != 1 || v.Move.State != domain.MoveDraining {
		t.Fatalf("current while draining: %+v %v", v, err)
	}
	if len(f.hub.redirects) != 0 {
		t.Fatal("agents heard the new address before the jobs finished")
	}

	// Handed off once the job finished.
	f.finishJob(job, domain.JobSucceeded)
	pkg, err := f.handoff(code)
	if err != nil {
		t.Fatal(err)
	}
	got = f.move(m.ID)
	if got.State != domain.MoveHandedOff || got.HandedOffAt == nil || f.lock.Level() != movelock.AgentsRefused {
		t.Fatalf("handed off: %+v lock %s", got, f.lock.Level())
	}
	gen := f.inst.Generation + 1
	if r := f.hub.redirects["env-new"]; r != (protocol.ManagerRedirectInput{URL: NewServerManagerURL, Generation: gen}) {
		t.Fatalf("new server's redirect %+v", r)
	}
	if r := f.hub.redirects["env-old"]; r != (protocol.ManagerRedirectInput{URL: "http://192.168.1.20:8080", Generation: gen}) {
		t.Fatalf("old server's redirect %+v", r)
	}
	if len(got.Redirects) != 2 || !got.Redirects[0].Sent || !got.Redirects[1].Sent {
		t.Fatalf("recorded redirects %+v", got.Redirects)
	}
	_, first, _ := openPackage(t, pkg, code, m.ID)
	again, err := f.handoff(code)
	if err != nil {
		t.Fatal(err)
	}
	_, second, _ := openPackage(t, again, code, m.ID)
	if !equalManifests(first, second) {
		t.Fatalf("a repeated handoff streamed another copy: %+v vs %+v", first, second)
	}
	if len(f.hub.redirects) != 2 {
		t.Fatal("a repeated handoff redirected again")
	}

	// Cancel after the handoff needs resumeHere and the typed name.
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); !errors.Is(err, ErrResumeRequired) {
		t.Fatalf("cancel without resumeHere: %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{ResumeHere: true, InstanceName: "not it"}); !errors.Is(err, ErrInstanceNameMismatch) {
		t.Fatalf("cancel with a wrong name: %v", err)
	}

	// Confirmed: repeatable, never cancelled, still locked after a restart.
	if c, err := f.confirm(code); err != nil || c.State != domain.MoveConfirmed || c.ConfirmedAt == nil {
		t.Fatalf("confirm: %+v %v", c, err)
	}
	if _, err := f.confirm(code); err != nil {
		t.Fatalf("repeated confirmation: %v", err)
	}
	if _, err := os.Stat(f.svc.outgoingDir(m.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the handoff package outlived the confirmation")
	}
	set, _ := store.GetInstanceSettings(f.ctx, f.db)
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{ResumeHere: true, InstanceName: set.Name}); !errors.Is(err, domain.ErrManagerMoveState) {
		t.Fatalf("cancel a confirmed move: %v", err)
	}
	if _, err := f.handoff(code); !errors.Is(err, domain.ErrManagerMoveState) {
		t.Fatalf("handoff after the confirmation: %v", err)
	}
	f.close()
	f.open()
	if f.lock.Level() != movelock.AgentsRefused {
		t.Fatalf("a restarted old manager is %s, want agents refused", f.lock.Level())
	}
}

func equalManifests(a, b Manifest) bool {
	if len(a.Parts) != len(b.Parts) {
		return false
	}
	for i := range a.Parts {
		if a.Parts[i] != b.Parts[i] {
			return false
		}
	}
	return true
}

// TestMoveExpiry: a move that was not handed off ends after seven days,
// also while draining: the move expires, nothing stays locked, the code
// is forgotten (refused) and the unused enrollment token revoked.
func TestMoveExpiry(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	m, code := f.create()
	f.clk.Advance(CodeLifetime - time.Minute)
	if v, err := f.svc.Current(f.ctx); err != nil || v.Move.State != domain.MoveOpen {
		t.Fatalf("before expiry: %+v %v", v, err)
	}
	f.clk.Advance(time.Minute)
	if _, err := f.svc.Current(f.ctx); !errors.Is(err, domain.ErrManagerMoveNotFound) {
		t.Fatalf("after expiry: %v", err)
	}
	if st := f.move(m.ID); st.State != domain.MoveExpired || st.EndedAt == nil {
		t.Fatalf("expired move %+v", st)
	}
	if sealed, _ := store.ManagerMoveSealedCode(f.ctx, f.db, m.ID); sealed != "" {
		t.Fatal("an expired move kept its code")
	}
	if len(f.enroll.revoked) != 1 || f.enroll.revoked[0] != m.EnrollmentID {
		t.Fatalf("revoked enrollments %v", f.enroll.revoked)
	}
	if _, err := f.handoff(code); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("handoff with an expired code: %v", err)
	}

	// Draining expires too, and the lock opens.
	m2, code2 := f.create()
	f.setState(m2.ID, domain.MoveReady)
	f.runningJob()
	var jr *domain.JobsRunningError
	if _, err := f.handoff(code2); !errors.As(err, &jr) {
		t.Fatalf("handoff: %v", err)
	}
	if lvl, _ := LockLevel(f.ctx, f.db, f.clk.Now().Add(CodeLifetime)); lvl != movelock.Open {
		t.Fatal("a restart after the expiry must not lock")
	}
	f.clk.Advance(CodeLifetime)
	if err := f.svc.Expire(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.move(m2.ID).State != domain.MoveExpired || f.lock.Level() != movelock.Open {
		t.Fatalf("draining after expiry: %s, lock %s", f.move(m2.ID).State, f.lock.Level())
	}
}

// TestCancelRules: open, ready and draining moves cancel (the lock
// opens, the code is forgotten); a handed-off one only with resumeHere
// and the instance name, and then the generation goes up by two (above
// the copy's, which agents that heard the redirect accepted) and the
// manager restarts; a draining move whose agents were already redirected
// does the same. The owner guard (step-up) runs first.
func TestCancelRules(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); !errors.Is(err, domain.ErrManagerMoveNotFound) {
		t.Fatalf("cancel without a move: %v", err)
	}
	m, _ := f.create()
	if c, err := f.svc.Cancel(f.ctx, CancelRequest{}); err != nil || c.State != domain.MoveCancelled || c.ID != m.ID {
		t.Fatalf("cancel open: %+v %v", c, err)
	}
	if f.restarts.Load() != 0 {
		t.Fatal("cancelling an open move restarted the manager")
	}

	m, code := f.create()
	f.enrollNewServer(m)
	f.setState(m.ID, domain.MoveReady)
	job := f.runningJob()
	_, _ = f.handoff(code)
	if f.lock.Level() != movelock.ReadOnly {
		t.Fatal("not draining")
	}
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); err != nil || f.lock.Level() != movelock.Open {
		t.Fatalf("cancel draining: %v, lock %s", err, f.lock.Level())
	}
	f.finishJob(job, domain.JobSucceeded)
	live := func() int64 {
		inst, _, err := store.GetInstance(f.ctx, f.db)
		if err != nil {
			t.Fatal(err)
		}
		return inst.Generation
	}
	if live() != f.inst.Generation || f.restarts.Load() != 0 {
		t.Fatal("a cancel before any redirect changed the generation")
	}

	m, code = f.create()
	f.setState(m.ID, domain.MoveReady)
	if _, err := f.db.ExecContext(f.ctx, "UPDATE manager_moves SET target_environment_id = 'env-new' WHERE id = ?", m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.handoff(code); err != nil {
		t.Fatal(err)
	}
	f.guard.err = domain.ErrStepUpRequired
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{ResumeHere: true}); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Fatalf("cancel without step-up: %v", err)
	}
	f.guard.err = nil
	set, _ := store.GetInstanceSettings(f.ctx, f.db)
	c, err := f.svc.Cancel(f.ctx, CancelRequest{ResumeHere: true, InstanceName: " " + set.Name + " "})
	if err != nil || c.State != domain.MoveCancelled || f.lock.Level() != movelock.Open {
		t.Fatalf("resume here: %+v %v lock %s", c, err, f.lock.Level())
	}
	if live() != f.inst.Generation+2 || f.restarts.Load() != 1 {
		t.Fatalf("resumed: generation %d (was %d), restarts %d", live(), f.inst.Generation, f.restarts.Load())
	}
	if _, err := f.handoff(code); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("handoff of a cancelled move: %v", err)
	}
	if _, err := f.confirm(code); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("confirm of a resumed move: %v", err)
	}
}

// TestHandoffPackage: the decrypted stream carries the parts and a
// manifest whose lengths and SHA-256 match; the copy has the generation
// raised by one, the move arrived and the redirects recorded (the live
// database keeps its generation); the sealed secret key opens only with
// the move's code.
func TestHandoffPackage(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, code := f.create()
	f.setState(m.ID, domain.MoveReady)
	pkg, err := f.handoff(code)
	if err != nil {
		t.Fatal(err)
	}
	dir, man, seen := openPackage(t, pkg, code, m.ID)
	for _, name := range []string{PartState, PartDatabase, PartSealedKey, PartTemplates} {
		p, ok := man.Part(name)
		if !ok {
			t.Fatalf("manifest lacks %s", name)
		}
		sum, err := fileSum(filepath.Join(dir, name))
		if err != nil || sum.Size != p.Size || sum.SHA256 != p.SHA256 {
			t.Fatalf("%s: %+v vs %+v (%v)", name, sum, p, err)
		}
	}
	if seen == 0 {
		t.Fatal("no progress reported")
	}
	info, key, err := f.svc.verifyPackage(f.ctx, dir, receiveTarget{MoveID: m.ID}, code)
	if err != nil {
		t.Fatal(err)
	}
	if info.Generation != f.inst.Generation+1 || info.InstanceID != f.inst.ID || key.ID() != f.keyring.Primary().ID() {
		t.Fatalf("state %+v, key %s", info, key.ID())
	}
	if live, _, _ := store.GetInstance(f.ctx, f.db); live.Generation != f.inst.Generation {
		t.Fatalf("the live generation changed to %d", live.Generation)
	}
	cp, err := store.Open(f.ctx, filepath.Join(dir, PartDatabase))
	if err != nil {
		t.Fatal(err)
	}
	arrived, err := store.GetManagerMove(f.ctx, cp, m.ID)
	_ = cp.Close()
	if err != nil || arrived.State != domain.MoveArrived || len(arrived.Redirects) != 1 || arrived.Redirects[0].Role != domain.RedirectOldServer {
		t.Fatalf("the copy's move %+v %v", arrived, err)
	}
	sealed, _ := os.ReadFile(filepath.Join(dir, PartSealedKey))
	other, _ := authsep.MintMoveCode(m.ID)
	if _, err := openSecretKey(sealed, other.Token, f.inst.ID); !errors.Is(err, errSealedKey) {
		t.Fatalf("opened with another code: %v", err)
	}
	if _, err := openSecretKey(sealed, code, "another-instance"); !errors.Is(err, errSealedKey) {
		t.Fatalf("opened for another instance: %v", err)
	}
	if bytes.Contains(sealed, f.keyring.Primary().Bytes()) {
		t.Fatal("the sealed file holds the plain key")
	}
	var raw bytes.Buffer
	if err := pkg.Write(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw.Bytes(), []byte("SQLite format 3")) || bytes.Contains(raw.Bytes(), []byte(PartManifest)) {
		t.Fatal("the stream is not encrypted")
	}
}

// TestCheckIn: the waiting manager's check-in (a signed GET) records it
// until the handoff and answers the state, the apps' progress and the jobs
// a draining move waits for; a request signed for another method or with
// another code is refused.
func TestCheckIn(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	f.colocate()
	m, code := f.create()
	get := func() (CheckIn, error) {
		h, err := SignRequest(code, http.MethodGet, CheckInPath, f.clk.Now())
		if err != nil {
			t.Fatal(err)
		}
		return f.svc.CheckIn(f.from(), MoveAuth{Header: h, Method: http.MethodGet, Path: CheckInPath})
	}
	if c, err := get(); err != nil || c.State != domain.MoveOpen || c.StacksTotal != 0 {
		t.Fatalf("open: %+v %v", c, err)
	}
	if got := f.move(m.ID); got.CheckedInAt == nil || got.HandoffAddress != "192.168.1.20" || got.State != domain.MoveOpen {
		t.Fatalf("after the check-in %+v", got)
	}
	if _, err := f.svc.CheckIn(f.from(), f.signed(code, CheckInPath)); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("signed for POST: %v", err)
	}
	mig, _, err := f.migr.StartEnvironment(f.ctx, f.migr.principal, "env-old", envmigrations.EnvironmentRequest{TargetEnvironmentID: "env-new"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(f.ctx, "UPDATE manager_moves SET state = 'moving', migration_id = ? WHERE id = ?", mig.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := get(); err != nil || c.State != domain.MoveMoving || c.StacksMoved != 1 || c.StacksTotal != 2 || c.CurrentStack != "web" {
		t.Fatalf("moving: %+v %v", c, err)
	}
	f.finishJob(mig.ID, domain.JobSucceeded)
	f.setState(m.ID, domain.MoveReady)
	f.runningJob()
	var jr *domain.JobsRunningError
	if _, err := f.handoff(code); !errors.As(err, &jr) {
		t.Fatalf("handoff: %v", err)
	}
	if c, err := get(); err != nil || c.State != domain.MoveDraining || c.JobsRunning != 1 {
		t.Fatalf("draining: %+v %v", c, err)
	}
}
