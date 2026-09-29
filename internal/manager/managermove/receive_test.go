package managermove

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/backups"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

// TestReceiveAndFinishArrival is a whole move between two managers: the
// new one receives the old one's state over HTTPS (waiting while a job
// runs there), stages it and asks for a restart; after the restart the
// copy runs with the generation raised, the old sessions kept, system.move
// audited, and the confirmation retried until the old manager confirmed.
func TestReceiveAndFinishArrival(t *testing.T) {
	clk := testutil.FakeClock()
	old := newFixture(t, clk, nil)
	if _, err := old.db.ExecContext(old.ctx, `INSERT INTO sessions (token, data, expiry) VALUES ('kept-session', x'00', '2030-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	m, code, err := old.svc.CreateMove(old.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var confirmFailures atomic.Int32
	confirmFailures.Store(1)
	srv := httptest.NewTLSServer(oldManagerHandler(old, &confirmFailures))
	t.Cleanup(srv.Close)
	nw := newFixture(t, clk, srv.Client())

	job := old.runningJob()
	j, err := nw.svc.StartReceive(nw.ctx, srv.URL, code)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(j.Input, []byte(code)) {
		t.Fatal("the move code is in the job input")
	}
	if _, err := nw.svc.StartReceive(nw.ctx, srv.URL, code); !errors.Is(err, domain.ErrManagerMoveInProgress) {
		t.Fatalf("second receive: %v", err)
	}
	if err := nw.eng.DispatchPending(nw.ctx); err != nil {
		t.Fatal(err)
	}
	// The old manager answers jobs_running: the receive waits (fake clock).
	waitUntil(t, nw, func() bool {
		st, _ := nw.svc.LatestReceive(nw.ctx)
		return st != nil && st.WaitingJobs == 1
	})
	if old.move(m.ID).State != domain.MoveDraining {
		t.Fatal("the old manager is not draining")
	}
	old.finishJob(job)
	done := advanceUntilDone(t, nw, j.ID)
	if done.State != domain.JobSucceeded {
		t.Fatalf("receive %s: %s %s (%s)", done.State, done.ErrorClass, done.ErrorMessage, done.Recovery)
	}
	if nw.restarts.Load() != 1 || !backups.RestorePending(nw.dataDir) {
		t.Fatalf("restarts %d, pending %v", nw.restarts.Load(), backups.RestorePending(nw.dataDir))
	}
	if old.move(m.ID).State != domain.MoveHandedOff {
		t.Fatal("the old manager is not handed off")
	}

	// The restart applies the copy (app.Start: ApplyPendingRestore, then
	// FinishArrival).
	nw.close()
	mk, err := backups.ApplyPendingRestore(nw.dataDir, nw.dbPath, nw.keyFile, clk.Now())
	if err != nil || mk == nil || mk.Kind != backups.RestoreKindMove || mk.MoveID != m.ID || bytes.Contains([]byte(mk.SealedMoveCode), []byte(code)) {
		t.Fatalf("applied %+v %v", mk, err)
	}
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
	if left, _ := backups.AppliedRestore(nw.dataDir); left != nil {
		t.Fatal("the applied marker is still there")
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
	v, err := nw.svc.Current(nw.ctx)
	if err != nil || v.Checklist == nil || v.Move.State != domain.MoveArrived {
		t.Fatalf("current on the new manager: %+v %v", v, err)
	}
}

// TestReceiveRefusals: a wrong code and an http address fail with clear
// classes; the code never reaches the job.
func TestReceiveRefusals(t *testing.T) {
	clk := testutil.FakeClock()
	old := newFixture(t, clk, nil)
	m, _, _ := old.svc.CreateMove(old.ctx)
	srv := httptest.NewTLSServer(oldManagerHandler(old, nil))
	t.Cleanup(srv.Close)
	nw := newFixture(t, clk, srv.Client())
	var fe *domain.FieldError
	if _, err := nw.svc.StartReceive(nw.ctx, "http://docker.example.com", "dmm_x"); !errors.As(err, &fe) || fe.Field != "sourceUrl" {
		t.Fatalf("http address: %v", err)
	}
	if _, err := nw.svc.StartReceive(nw.ctx, srv.URL, "not-a-code"); !errors.As(err, &fe) || fe.Field != "code" {
		t.Fatalf("malformed code: %v", err)
	}
	wrong, _ := authsep.MintMoveCode(m.ID)
	j, err := nw.svc.StartReceive(nw.ctx, srv.URL, wrong.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := nw.eng.DispatchPending(nw.ctx); err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, nw, j.ID)
	if done.State != domain.JobFailed || done.ErrorClass != ClassCodeInvalid {
		t.Fatalf("wrong code: %s %s", done.State, done.ErrorClass)
	}
	if _, err := os.Stat(nw.svc.incomingDir(j.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed receive left its files")
	}
	if old.move(m.ID).State != domain.MoveOpen {
		t.Fatal("a wrong code changed the old manager's move")
	}
}

// receivedPackage hands off old's state into a directory like the
// handoff step does.
func receivedPackage(t *testing.T, old *fixture) (dir, moveID, code string) {
	t.Helper()
	m, code, err := old.svc.CreateMove(old.ctx)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := old.svc.Handoff(old.secure(), code)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := pkg.Write(&buf); err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	if _, err := readPackage(&buf, dir, nil); err != nil {
		t.Fatal(err)
	}
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

// TestVerifyRefusals: the verify step refuses a copy that does not open
// with the code, has a newer schema, belongs to another instance or was
// not handed off.
func TestVerifyRefusals(t *testing.T) {
	old := newFixture(t, testutil.FakeClock(), nil)
	dir, moveID, code := receivedPackage(t, old)
	in := receiveInput{MoveID: moveID}
	if _, _, err := old.svc.verifyPackage(old.ctx, dir, in, code); err != nil {
		t.Fatalf("intact copy: %v", err)
	}

	other, _ := authsep.MintMoveCode(moveID)
	_, _, err := old.svc.verifyPackage(old.ctx, dir, in, other.Token)
	wantClass(t, err, ClassCodeInvalid)
	_, _, err = old.svc.verifyPackage(old.ctx, dir, receiveInput{MoveID: "another-move"}, code)
	wantClass(t, err, ClassCodeInvalid)

	editCopy(t, old, dir, func(db *bun.DB) {
		if _, err := db.NewRaw("INSERT INTO ? (name, group_id, migrated_at) VALUES ('29990101000000_future', 99, '2999-01-01T00:00:00Z')",
			bun.Ident(store.MigrationsTable)).Exec(old.ctx); err != nil {
			t.Fatal(err)
		}
	})
	_, _, err = old.svc.verifyPackage(old.ctx, dir, in, code)
	wantClass(t, err, ClassSchemaIncompatible)

	dir, moveID, code = receivedPackageAfterCancel(t, old)
	editCopy(t, old, dir, func(db *bun.DB) {
		if _, err := db.ExecContext(old.ctx, "UPDATE instance SET id = 'another-instance'"); err != nil {
			t.Fatal(err)
		}
	})
	_, _, err = old.svc.verifyPackage(old.ctx, dir, receiveInput{MoveID: moveID}, code)
	wantClass(t, err, ClassStateInvalid)

	dir, moveID, code = receivedPackageAfterCancel(t, old)
	editCopy(t, old, dir, func(db *bun.DB) {
		if _, err := db.ExecContext(old.ctx, "UPDATE manager_moves SET state = 'draining' WHERE id = ?", moveID); err != nil {
			t.Fatal(err)
		}
	})
	_, _, err = old.svc.verifyPackage(old.ctx, dir, receiveInput{MoveID: moveID}, code)
	wantClass(t, err, ClassMoveNotHandedOff)
}

// receivedPackageAfterCancel resumes old (cancelling its handed-off move)
// and hands off again.
func receivedPackageAfterCancel(t *testing.T, old *fixture) (string, string, string) {
	t.Helper()
	set, _ := store.GetInstanceSettings(old.ctx, old.db)
	if _, err := old.svc.Cancel(old.ctx, CancelRequest{ResumeHere: true, InstanceName: set.Name}); err != nil {
		t.Fatal(err)
	}
	return receivedPackage(t, old)
}

// TestReadPackageRefusals: a changed part (SHA-256), a missing manifest,
// an unknown part and a part after the manifest are refused.
func TestReadPackageRefusals(t *testing.T) {
	old := newFixture(t, testutil.FakeClock(), nil)
	_, code, _ := old.svc.CreateMove(old.ctx)
	pkg, err := old.svc.Handoff(old.secure(), code)
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

// TestNormalizeSource accepts https origins only.
func TestNormalizeSource(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://docker.example.com":         true,
		"https://Docker.Example.com:8443/":   true,
		"http://docker.example.com":          false,
		"https://user:pw@docker.example.com": false,
		"https://docker.example.com/path":    false,
		"https://docker.example.com/?a=1":    false,
		"docker.example.com":                 false,
	} {
		_, err := NormalizeSource(raw)
		if (err == nil) != ok {
			t.Errorf("%q: %v", raw, err)
		}
	}
}
