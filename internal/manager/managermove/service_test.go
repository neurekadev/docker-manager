package managermove

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/movelock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestMoveStateMachine walks a move through every state of the old
// manager: open (nothing locked), draining (read-only while a job runs),
// handed_off (agents refused, the copy streamed; repeated with the same
// copy), confirmed (never cancelled); a restarted manager stays locked.
func TestMoveStateMachine(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	m, code, err := f.svc.CreateMove(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != domain.MoveOpen || !m.ExpiresAt.Equal(m.CreatedAt.Add(time.Hour)) || !strings.HasPrefix(code, "dmm_"+m.ID+"_") {
		t.Fatalf("created %+v (code prefix %q)", m, code[:4])
	}
	if f.lock.Level() != movelock.Open {
		t.Fatal("an open move must not lock anything")
	}
	if _, _, err := f.svc.CreateMove(f.ctx); !errors.Is(err, domain.ErrManagerMoveExists) {
		t.Fatalf("second move: %v", err)
	}

	// The handoff needs the code and a secure origin.
	other, _ := authsep.MintMoveCode(m.ID)
	if _, err := f.svc.Handoff(f.secure(), other.Token); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("wrong secret: %v", err)
	}
	var ie *domain.InsecureOriginError
	if _, err := f.svc.Handoff(f.ctx, code); !errors.As(err, &ie) {
		t.Fatalf("insecure origin: %v", err)
	}
	if f.move(m.ID).State != domain.MoveOpen {
		t.Fatal("a refused handoff changed the move")
	}

	// Draining while a job runs.
	job := f.runningJob()
	var jr *domain.JobsRunningError
	if _, err := f.svc.Handoff(f.secure(), code); !errors.As(err, &jr) || jr.Count != 1 || jr.RetryAfter != HandoffRetryAfter {
		t.Fatalf("handoff with a running job: %v", err)
	}
	if f.move(m.ID).State != domain.MoveDraining || f.lock.Level() != movelock.ReadOnly {
		t.Fatalf("draining: state %s lock %s", f.move(m.ID).State, f.lock.Level())
	}
	if v, err := f.svc.Current(f.ctx); err != nil || v.JobsRunning != 1 || v.Move.State != domain.MoveDraining {
		t.Fatalf("current while draining: %+v %v", v, err)
	}

	// Handed off once the job finished.
	f.finishJob(job)
	pkg, err := f.svc.Handoff(f.secure(), code)
	if err != nil {
		t.Fatal(err)
	}
	got := f.move(m.ID)
	if got.State != domain.MoveHandedOff || got.HandedOffAt == nil || got.HandoffAddress != "203.0.113.7" || f.lock.Level() != movelock.AgentsRefused {
		t.Fatalf("handed off: %+v lock %s", got, f.lock.Level())
	}
	var first bytes.Buffer
	if err := pkg.Write(&first); err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.Handoff(f.secure(), code)
	if err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := again.Write(&second); err != nil {
		t.Fatal(err)
	}
	if !equalManifests(pkg.Manifest, again.Manifest) {
		t.Fatalf("a repeated handoff streamed another copy: %+v vs %+v", pkg.Manifest, again.Manifest)
	}

	// Cancel after the handoff needs resumeHere and the typed name.
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); !errors.Is(err, ErrResumeRequired) {
		t.Fatalf("cancel without resumeHere: %v", err)
	}
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{ResumeHere: true, InstanceName: "not it"}); !errors.Is(err, ErrInstanceNameMismatch) {
		t.Fatalf("cancel with a wrong name: %v", err)
	}

	// Confirmed: repeatable, never cancelled, still locked after a restart.
	if c, err := f.svc.Confirm(f.secure(), code); err != nil || c.State != domain.MoveConfirmed || c.ConfirmedAt == nil {
		t.Fatalf("confirm: %+v %v", c, err)
	}
	if _, err := f.svc.Confirm(f.secure(), code); err != nil {
		t.Fatalf("repeated confirmation: %v", err)
	}
	if _, err := os.Stat(f.svc.outgoingDir(m.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the handoff package outlived the confirmation")
	}
	set, _ := store.GetInstanceSettings(f.ctx, f.db)
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{ResumeHere: true, InstanceName: set.Name}); !errors.Is(err, domain.ErrManagerMoveState) {
		t.Fatalf("cancel a confirmed move: %v", err)
	}
	if _, err := f.svc.Handoff(f.secure(), code); !errors.Is(err, domain.ErrManagerMoveState) {
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

// TestMoveExpiry: a code runs out after an hour, also while draining: the
// move expires, nothing stays locked and the code is refused.
func TestMoveExpiry(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	m, code, err := f.svc.CreateMove(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(59 * time.Minute)
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
	if _, err := f.svc.Handoff(f.secure(), code); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("handoff with an expired code: %v", err)
	}

	// Draining expires too, and the lock opens.
	m2, code2, err := f.svc.CreateMove(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.runningJob()
	var jr *domain.JobsRunningError
	if _, err := f.svc.Handoff(f.secure(), code2); !errors.As(err, &jr) {
		t.Fatalf("handoff: %v", err)
	}
	if lvl, _ := LockLevel(f.ctx, f.db, f.clk.Now().Add(time.Hour)); lvl != movelock.Open {
		t.Fatal("a restart after the expiry must not lock")
	}
	f.clk.Advance(time.Hour)
	if err := f.svc.Expire(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.move(m2.ID).State != domain.MoveExpired || f.lock.Level() != movelock.Open {
		t.Fatalf("draining after expiry: %s, lock %s", f.move(m2.ID).State, f.lock.Level())
	}
}

// TestCancelRules: open and draining moves cancel (the lock opens); a
// handed-off one only with resumeHere and the instance name; the owner
// guard (step-up) runs first.
func TestCancelRules(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); !errors.Is(err, domain.ErrManagerMoveNotFound) {
		t.Fatalf("cancel without a move: %v", err)
	}
	m, _, _ := f.svc.CreateMove(f.ctx)
	if c, err := f.svc.Cancel(f.ctx, CancelRequest{}); err != nil || c.State != domain.MoveCancelled || c.ID != m.ID {
		t.Fatalf("cancel open: %+v %v", c, err)
	}

	_, code, _ := f.svc.CreateMove(f.ctx)
	job := f.runningJob()
	_, _ = f.svc.Handoff(f.secure(), code)
	if f.lock.Level() != movelock.ReadOnly {
		t.Fatal("not draining")
	}
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{}); err != nil || f.lock.Level() != movelock.Open {
		t.Fatalf("cancel draining: %v, lock %s", err, f.lock.Level())
	}
	f.finishJob(job)

	_, code, _ = f.svc.CreateMove(f.ctx)
	if _, err := f.svc.Handoff(f.secure(), code); err != nil {
		t.Fatal(err)
	}
	f.guard.err = domain.ErrStepUpRequired
	if _, err := f.svc.Cancel(f.ctx, CancelRequest{ResumeHere: true}); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Fatalf("cancel without step-up: %v", err)
	}
	if _, _, err := f.svc.CreateMove(f.ctx); !errors.Is(err, domain.ErrStepUpRequired) {
		t.Fatalf("create without step-up: %v", err)
	}
	f.guard.err = nil
	set, _ := store.GetInstanceSettings(f.ctx, f.db)
	c, err := f.svc.Cancel(f.ctx, CancelRequest{ResumeHere: true, InstanceName: " " + set.Name + " "})
	if err != nil || c.State != domain.MoveCancelled || f.lock.Level() != movelock.Open {
		t.Fatalf("resume here: %+v %v lock %s", c, err, f.lock.Level())
	}
	if _, err := f.svc.Handoff(f.secure(), code); !errors.Is(err, domain.ErrMoveCodeInvalid) {
		t.Fatalf("handoff of a cancelled move: %v", err)
	}
	if _, err := f.svc.Confirm(f.secure(), code); !errors.Is(err, domain.ErrManagerMoveState) {
		t.Fatalf("confirm of a resumed move: %v", err)
	}
}

// TestHandoffPackage: the stream carries the parts and a manifest whose
// lengths and SHA-256 match; the copy has the generation raised by one and
// the move arrived (the live database is unchanged); the sealed secret
// key opens only with the move's code.
func TestHandoffPackage(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	m, code, _ := f.svc.CreateMove(f.ctx)
	pkg, err := f.svc.Handoff(f.secure(), code)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := pkg.Write(&buf); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var seen int64
	man, err := readPackage(&buf, dir, func(n int64) { seen = n })
	if err != nil {
		t.Fatal(err)
	}
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
	info, key, err := f.svc.verifyPackage(f.ctx, dir, receiveInput{MoveID: m.ID}, code)
	if err != nil {
		t.Fatal(err)
	}
	if info.Generation != f.inst.Generation+1 || info.InstanceID != f.inst.ID || key.ID() != f.keyring.Primary().ID() {
		t.Fatalf("state %+v, key %s", info, key.ID())
	}
	if live, _, _ := store.GetInstance(f.ctx, f.db); live.Generation != f.inst.Generation {
		t.Fatalf("the live generation changed to %d", live.Generation)
	}
	if f.move(m.ID).State != domain.MoveHandedOff {
		t.Fatal("the live move is not handed off")
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
}
