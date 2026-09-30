package managermove

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authsep"
	"github.com/neurekadev/docker-manager/internal/manager/backups"
	envmigrations "github.com/neurekadev/docker-manager/internal/manager/migrations"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// startWaiting starts a new manager in waiting mode against the old
// manager at srv with code and runs its loop until the test ends.
func startWaiting(t *testing.T, clk *clock.Fake, srv *httptest.Server, code string) *fixture {
	t.Helper()
	from, _ := url.Parse(srv.URL)
	nw := newFixtureWith(t, clk, srv.Client(), &WaitingConfig{From: from, Code: code})
	ctx, cancel := context.WithCancel(nw.ctx)
	var wg sync.WaitGroup
	wg.Go(func() { nw.svc.Run(ctx) })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	return nw
}

func (f *fixture) status() WaitStatus {
	f.t.Helper()
	st, err := f.svc.WaitStatus(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// TestWaitingModeMove is a whole move between two managers over plain
// HTTP: the new manager in waiting mode checks in (not_ready while the
// move is open, then with the apps' progress), waits while a job runs,
// receives the encrypted copy, stages it and asks for a restart; after the
// restart the copy runs with the generation raised, the old sessions
// kept, system.move audited, the confirmation signed and retried until
// the old manager confirmed, and "Move complete" lists the redirects.
func TestWaitingModeMove(t *testing.T) {
	clk := testutil.FakeClock()
	old := newFixture(t, clk, nil)
	old.colocate()
	if _, err := old.db.ExecContext(old.ctx, `INSERT INTO sessions (token, data, expiry) VALUES ('kept-session', x'00', '2030-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	m, code := old.create()
	old.enrollNewServer(m)
	// The old manager's page records the new server's environment.
	if v, err := old.svc.Current(old.ctx); err != nil || v.NewServer == nil || v.NewServer.EnvironmentID != "env-new" {
		t.Fatalf("current %+v %v", v, err)
	}
	var confirmFailures atomic.Int32
	confirmFailures.Store(1)
	srv := httptest.NewServer(oldManagerHandler(old, &confirmFailures))
	t.Cleanup(srv.Close)
	nw := startWaiting(t, clk, srv, code)
	if st := nw.status(); st.OldManager != srv.URL || st.PublicURL != publicURL {
		t.Fatalf("status %+v", st)
	}

	waitUntil(t, nw, true, func() bool { st := nw.status(); return st.Phase == WaitWaiting && st.OldState == "open" })
	if old.move(m.ID).CheckedInAt == nil {
		t.Fatal("the waiting manager's request did not check in")
	}
	// The apps move (manager.move is tested on its own).
	mig, _, err := old.migr.StartEnvironment(old.ctx, old.migr.principal, "env-old",
		envmigrations.EnvironmentRequest{TargetEnvironmentID: "env-new", IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(old.ctx, "UPDATE manager_moves SET state = 'moving', migration_id = ? WHERE id = ?", mig.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, nw, true, func() bool {
		st := nw.status()
		return st.OldState == "moving" && st.StacksMoved == 1 && st.StacksTotal == 2 && st.CurrentStack == "web"
	})

	old.finishJob(mig.ID, domain.JobSucceeded)
	job := old.runningJob()
	old.setState(m.ID, domain.MoveReady)
	waitUntil(t, nw, true, func() bool { st := nw.status(); return st.Phase == WaitFinishingJobs && st.JobsRunning == 1 })
	if old.move(m.ID).State != domain.MoveDraining {
		t.Fatal("the old manager is not draining")
	}
	old.finishJob(job, domain.JobSucceeded)
	waitUntil(t, nw, true, func() bool { return nw.restarts.Load() == 1 })
	if st := nw.status(); st.Phase != WaitRestarting || st.Bytes == 0 || st.ErrorCode != "" {
		t.Fatalf("after the receive %+v", st)
	}
	if !backups.RestorePending(nw.dataDir) || old.move(m.ID).State != domain.MoveHandedOff {
		t.Fatalf("pending %v, old %s", backups.RestorePending(nw.dataDir), old.move(m.ID).State)
	}
	if _, err := os.Stat(nw.svc.receiveDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the incoming directory is left over")
	}

	// The restart applies the copy (app.Start: ApplyPendingRestore, then
	// FinishArrival); the variables are still set.
	nw.close()
	mk, err := backups.ApplyPendingRestore(nw.dataDir, nw.dbPath, nw.keyFile, clk.Now())
	if err != nil || mk == nil || mk.Kind != backups.RestoreKindMove || mk.MoveID != m.ID || bytes.Contains([]byte(mk.SealedMoveCode), []byte(code)) {
		t.Fatalf("applied %+v %v", mk, err)
	}
	nw.waiting = nil
	nw.open()
	if nw.inst.ID != old.inst.ID || nw.inst.Generation != old.inst.Generation+1 || nw.keyring.Primary().ID() != old.keyring.Primary().ID() {
		t.Fatalf("new instance %+v key %s", nw.inst, nw.keyring.Primary().ID())
	}
	applied, err := backups.AppliedRestore(nw.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := nw.svc.FinishArrival(nw.ctx, applied); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := nw.db.NewRaw("SELECT count(*) FROM sessions WHERE token = 'kept-session'").Scan(nw.ctx, &sessions); err != nil || sessions != 1 {
		t.Fatalf("the moved session was not kept: %d %v", sessions, err)
	}
	if !slices.Contains(nw.audit.actions(), "system.move") {
		t.Fatalf("audit %v", nw.audit.actions())
	}
	arrived := nw.move(m.ID)
	if arrived.State != domain.MoveArrived || arrived.SourceURL != srv.URL || arrived.ArrivedAt == nil || arrived.ConfirmedAt != nil {
		t.Fatalf("arrived %+v", arrived)
	}
	if st := nw.status(); st.Phase != WaitComplete || st.OldManagerConfirmed {
		t.Fatalf("status after the move %+v", st)
	}

	// The first confirmation fails (503) and is retried.
	if done, err := nw.svc.ConfirmOnce(nw.ctx); err != nil || done {
		t.Fatalf("first confirmation: done %v %v", done, err)
	}
	if a := nw.move(m.ID); a.ConfirmAttempts != 1 || a.ConfirmError != "http_503" {
		t.Fatalf("after a failed confirmation %+v", a)
	}
	if done, err := nw.svc.ConfirmOnce(nw.ctx); err != nil || !done {
		t.Fatalf("second confirmation: done %v %v", done, err)
	}
	a := nw.move(m.ID)
	if a.ConfirmedAt == nil || a.ConfirmError != "" || old.move(m.ID).State != domain.MoveConfirmed {
		t.Fatalf("confirmed: new %+v old %s", a, old.move(m.ID).State)
	}
	if sealed, _ := store.ManagerMoveSealedCode(nw.ctx, nw.db, m.ID); sealed != "" {
		t.Fatal("the sealed move code outlived the confirmation")
	}
	if st := nw.status(); !st.OldManagerConfirmed {
		t.Fatalf("status after the confirmation %+v", st)
	}
	v, err := nw.svc.Current(nw.ctx)
	if err != nil || v.Complete == nil || v.Move.State != domain.MoveArrived || len(v.Complete.Redirects) != 2 {
		t.Fatalf("current on the new manager: %+v %v", v, err)
	}
	for _, r := range v.Complete.Redirects {
		if !r.Sent || r.NeedsFix {
			t.Errorf("redirect %+v", r)
		}
	}
	if o := v.Complete.OldEnvironment; o == nil || o.EnvironmentID != "env-old" || o.MigrationID != mig.ID {
		t.Fatalf("old environment %+v", o)
	}
}

// TestWaitingModeRefusals: a wrong code keeps asking with the class and
// what to fix (the old move is unchanged); clocks five minutes apart
// answer clock skew; an old manager that cannot be reached keeps being
// asked; an unusable code stops waiting mode at once.
func TestWaitingModeRefusals(t *testing.T) {
	clk := testutil.FakeClock()
	old := newFixture(t, clk, nil)
	m, code := old.create()
	srv := httptest.NewServer(oldManagerHandler(old, nil))
	t.Cleanup(srv.Close)

	wrong, _ := authsep.MintMoveCode(m.ID)
	nw := startWaiting(t, clk, srv, wrong.Token)
	waitUntil(t, nw, true, func() bool { return nw.status().ErrorCode == ClassCodeInvalid })
	if st := nw.status(); st.Phase != WaitConnecting || st.Recovery == "" {
		t.Fatalf("wrong code %+v", st)
	}
	if got := old.move(m.ID); got.State != domain.MoveOpen || got.CheckedInAt != nil {
		t.Fatalf("a wrong code changed the old move %+v", got)
	}

	sk := startWaiting(t, clock.NewFake(clk.Now().Add(10*time.Minute)), srv, code)
	waitUntil(t, sk, true, func() bool { return sk.status().ErrorCode == ClassClockSkew })

	gone := httptest.NewServer(oldManagerHandler(old, nil))
	gone.Close()
	un := startWaiting(t, clk, gone, code)
	waitUntil(t, un, true, func() bool { return un.status().ErrorCode == ClassUnreachable })
	if st := un.status(); st.Phase != WaitConnecting || st.LastContactAt != nil {
		t.Fatalf("unreachable %+v", st)
	}

	bad := startWaiting(t, clk, srv, "not-a-code")
	waitUntil(t, bad, false, func() bool { return bad.status().Phase == WaitFailed })
	if st := bad.status(); st.ErrorCode != ClassCodeInvalid {
		t.Fatalf("unusable code %+v", st)
	}
}

// TestWaitStatusElsewhere: a manager that neither waits nor arrived with
// the variables set reports none.
func TestWaitStatusElsewhere(t *testing.T) {
	f := newFixture(t, testutil.FakeClock(), nil)
	if st := f.status(); st.Phase != WaitNone {
		t.Fatalf("status %+v", st)
	}
}

// receivedPackage hands off old's (ready) state into a directory like
// waiting mode does.
func receivedPackage(t *testing.T, old *fixture) (dir, moveID, code string) {
	t.Helper()
	m, code := old.create()
	old.setState(m.ID, domain.MoveReady)
	pkg, err := old.handoff(code)
	if err != nil {
		t.Fatal(err)
	}
	dir, _, _ = openPackage(t, pkg, code, m.ID)
	return dir, m.ID, code
}

func editCopy(t *testing.T, f *fixture, dir string, fn func(db *bun.DB)) {
	t.Helper()
	db, err := store.Open(f.ctx, filepath.Join(dir, PartDatabase))
	if err != nil {
		t.Fatal(err)
	}
	fn(db)
	if _, err := db.ExecContext(f.ctx, "PRAGMA journal_mode=DELETE"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

func wantClass(t *testing.T, err error, class string) {
	t.Helper()
	var r *refusal
	if !errors.As(err, &r) || r.class != class {
		t.Fatalf("got %v, want class %s", err, class)
	}
}

// resumeAndReceive resumes old (cancelling its handed-off move) and hands
// off again.
func resumeAndReceive(t *testing.T, old *fixture) (string, string, string) {
	t.Helper()
	set, _ := store.GetInstanceSettings(old.ctx, old.db)
	if _, err := old.svc.Cancel(old.ctx, CancelRequest{ResumeHere: true, InstanceName: set.Name}); err != nil {
		t.Fatal(err)
	}
	return receivedPackage(t, old)
}

// TestVerifyRefusals: the checks refuse a copy that does not open with the
// code, has a newer schema, belongs to another instance or was not handed
// off.
func TestVerifyRefusals(t *testing.T) {
	old := newFixture(t, testutil.FakeClock(), nil)
	dir, moveID, code := receivedPackage(t, old)
	in := receiveTarget{MoveID: moveID}
	if _, _, err := old.svc.verifyPackage(old.ctx, dir, in, code); err != nil {
		t.Fatalf("intact copy: %v", err)
	}

	other, _ := authsep.MintMoveCode(moveID)
	_, _, err := old.svc.verifyPackage(old.ctx, dir, in, other.Token)
	wantClass(t, err, ClassCodeInvalid)
	_, _, err = old.svc.verifyPackage(old.ctx, dir, receiveTarget{MoveID: "another-move"}, code)
	wantClass(t, err, ClassCodeInvalid)

	editCopy(t, old, dir, func(db *bun.DB) {
		if _, err := db.NewRaw("INSERT INTO ? (name, group_id, migrated_at) VALUES ('29990101000000_future', 99, '2999-01-01T00:00:00Z')",
			bun.Ident(store.MigrationsTable)).Exec(old.ctx); err != nil {
			t.Fatal(err)
		}
	})
	_, _, err = old.svc.verifyPackage(old.ctx, dir, in, code)
	wantClass(t, err, ClassSchemaIncompatible)

	dir, moveID, code = resumeAndReceive(t, old)
	editCopy(t, old, dir, func(db *bun.DB) {
		if _, err := db.ExecContext(old.ctx, "UPDATE instance SET id = 'another-instance'"); err != nil {
			t.Fatal(err)
		}
	})
	_, _, err = old.svc.verifyPackage(old.ctx, dir, receiveTarget{MoveID: moveID}, code)
	wantClass(t, err, ClassStateInvalid)

	dir, moveID, code = resumeAndReceive(t, old)
	editCopy(t, old, dir, func(db *bun.DB) {
		if _, err := db.ExecContext(old.ctx, "UPDATE manager_moves SET state = 'draining' WHERE id = ?", moveID); err != nil {
			t.Fatal(err)
		}
	})
	_, _, err = old.svc.verifyPackage(old.ctx, dir, receiveTarget{MoveID: moveID}, code)
	wantClass(t, err, ClassMoveNotHandedOff)
}

// TestReadPackageRefusals: a changed part (SHA-256), a missing manifest,
// an unknown part and a part after the manifest are refused.
func TestReadPackageRefusals(t *testing.T) {
	old := newFixture(t, testutil.FakeClock(), nil)
	m, code := old.create()
	old.setState(m.ID, domain.MoveReady)
	pkg, err := old.handoff(code)
	if err != nil {
		t.Fatal(err)
	}
	// A byte of the database changed after the manifest was written.
	tampered := t.TempDir()
	for _, p := range pkg.Manifest.Parts {
		b, err := os.ReadFile(filepath.Join(pkg.dir, p.Name))
		if err != nil {
			t.Fatal(err)
		}
		if p.Name == PartDatabase {
			b[len(b)/2] ^= 0xff
		}
		if err := os.WriteFile(filepath.Join(tampered, p.Name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := writePackage(&buf, tampered, pkg.Manifest, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := readPackage(&buf, t.TempDir(), nil); !errors.Is(err, errPackage) {
		t.Fatalf("tampered part: %v", err)
	}

	tarOf := func(entries ...[2]string) *bytes.Buffer {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		for _, e := range entries {
			_ = tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o600, Size: int64(len(e[1])), Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte(e[1]))
		}
		_ = tw.Close()
		return &b
	}
	manifest, _ := json.Marshal(Manifest{Format: PackageFormat, Version: PackageVersion})
	for name, stream := range map[string]*bytes.Buffer{
		"no manifest":         tarOf([2]string{PartState, "{}"}),
		"unknown part":        tarOf([2]string{"../../etc/passwd", "x"}, [2]string{PartManifest, string(manifest)}),
		"part after manifest": tarOf([2]string{PartManifest, string(manifest)}, [2]string{PartState, "{}"}),
		"missing parts":       tarOf([2]string{PartManifest, string(manifest)}),
	} {
		if _, err := readPackage(stream, t.TempDir(), nil); !errors.Is(err, errPackage) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
