package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/uptrace/bun/migrate"

	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/audit"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/config"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil/migrationtest"
)

var testUI = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Docker Manager</title>")}}

func testConfig(dataDir string) config.Config {
	return config.Config{
		PublicURL:        &url.URL{Scheme: "http", Host: "localhost:8080"},
		LocalDevelopment: true,
		ListenAddr:       "127.0.0.1:0",
		DataDir:          dataDir,
		SecretKeyFile:    filepath.Join(dataDir, config.SecretKeyFileName),
	}
}

type runResult struct {
	base string
	stop func() error
}

// runManager runs the manager in the background and returns its base URL.
func runManager(t *testing.T, dataDir string, set *migrate.Migrations) runResult {
	t.Helper()
	ctx, cancel := context.WithCancel(testutil.Context(t))
	addrCh := make(chan net.Addr, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			Config:      testConfig(dataDir),
			Logger:      testutil.Logger(t),
			UI:          testUI,
			Clock:       testutil.FakeClock(),
			Migrations:  set,
			OnListening: func(a net.Addr) { addrCh <- a },
		})
	}()
	select {
	case a := <-addrCh:
		stop := func() error { cancel(); return <-errCh }
		t.Cleanup(func() { cancel() })
		return runResult{base: "http://" + a.String(), stop: stop}
	case err := <-errCh:
		cancel()
		t.Fatalf("manager did not start: %v", err)
	}
	return runResult{}
}

func get(t *testing.T, target string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(testutil.Context(t), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func appliedMigrations(t *testing.T, dataDir string) []string {
	t.Helper()
	db, err := store.Open(testutil.Context(t), filepath.Join(dataDir, config.DatabaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	applied, _, err := store.Status(testutil.Context(t), db, migrations.Migrations)
	if err != nil {
		t.Fatal(err)
	}
	return applied
}

// TestRunRestartsInProcessOnRequest: a controlled restart (a staged
// manager-state restore, #24) stops the manager gracefully and Run starts
// a new one on the same data directory.
func TestRunRestartsInProcessOnRequest(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	started := make(chan *Manager, 2)
	addrs := make(chan net.Addr, 2)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{Config: testConfig(dataDir), Logger: testutil.Logger(t), UI: testUI, Clock: testutil.FakeClock(),
			OnStarted: func(m *Manager) { started <- m }, OnListening: func(a net.Addr) { addrs <- a }})
	}()
	first := <-started
	<-addrs
	first.RequestRestart()
	second := <-started
	addr := <-addrs
	if second == first || second.Instance().ID != first.Instance().ID {
		t.Fatalf("restart: %p %p", first, second)
	}
	if code, _ := get(t, "http://"+addr.String()+"/api/v1/health"); code != http.StatusOK {
		t.Errorf("health after the restart: %d", code)
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestFreshStartServesAndRestartDoesNotReplay(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")

	m := runManager(t, dataDir, nil)
	if code, body := get(t, m.base+"/api/v1/health"); code != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("health: %d %s", code, body)
	}
	code, body := get(t, m.base+"/api/v1/health/ready")
	if code != http.StatusOK {
		t.Fatalf("ready: %d %s", code, body)
	}
	var ready struct {
		Status string `json:"status"`
		Checks []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(body), &ready); err != nil || ready.Status != "ready" || len(ready.Checks) != 2 {
		t.Fatalf("ready body %s (%v)", body, err)
	}
	if code, body := get(t, m.base+"/stacks/deep/link"); code != http.StatusOK || !strings.Contains(body, "Docker Manager") {
		t.Fatalf("deep link: %d %s", code, body)
	}
	if err := m.stop(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, config.SecretKeyFileName)); err != nil {
		t.Fatalf("secret key not generated: %v", err)
	}
	first := appliedMigrations(t, dataDir)
	if len(first) != len(migrations.Migrations.Sorted()) {
		t.Fatalf("applied %v", first)
	}
	snaps, _ := store.ListSnapshots(filepath.Join(dataDir, config.SnapshotDirName))
	if len(snaps) != 0 {
		t.Fatalf("fresh start took snapshots: %v", snaps)
	}

	// Second start: nothing replayed, no new snapshot, same instance.
	m = runManager(t, dataDir, nil)
	if code, _ := get(t, m.base+"/api/v1/health/ready"); code != http.StatusOK {
		t.Fatalf("ready after restart: %d", code)
	}
	if err := m.stop(); err != nil {
		t.Fatal(err)
	}
	if again := appliedMigrations(t, dataDir); !slices.Equal(first, again) {
		t.Fatalf("applied set changed on restart: %v -> %v", first, again)
	}
	snaps, _ = store.ListSnapshots(filepath.Join(dataDir, config.SnapshotDirName))
	if len(snaps) != 0 {
		t.Fatalf("restart took snapshots: %v", snaps)
	}
}

func TestInvalidMigrationAbortsBeforeListening(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	m := runManager(t, dataDir, nil)
	if err := m.stop(); err != nil {
		t.Fatal(err)
	}
	before := appliedMigrations(t, dataDir)

	logger, logs := testutil.CaptureLogger()
	err := Run(testutil.Context(t), Options{
		Config:     testConfig(dataDir),
		Logger:     logger,
		UI:         testUI,
		Clock:      testutil.FakeClock(),
		Migrations: migrationtest.WithFailing(migrations.Migrations),
		Listen: func(string, string) (net.Listener, error) {
			t.Error("listener bound despite failed migration")
			return nil, errors.New("must not listen")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "injected migration failure") {
		t.Fatalf("Run = %v, want migration failure", err)
	}
	if strings.Contains(logs.String(), `"msg":"listening"`) {
		t.Fatal("manager logged listening after failed migration")
	}

	if after := appliedMigrations(t, dataDir); !slices.Equal(before, after) {
		t.Fatalf("applied set changed: %v -> %v", before, after)
	}
	db, err := store.Open(testutil.Context(t), filepath.Join(dataDir, config.DatabaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	tables, _ := store.UserTables(testutil.Context(t), db)
	_ = db.Close()
	if slices.Contains(tables, migrationtest.FailingTable) {
		t.Fatalf("failed migration left table behind: %v", tables)
	}
	snaps, _ := store.ListSnapshots(filepath.Join(dataDir, config.SnapshotDirName))
	if len(snaps) != 1 || !strings.Contains(snaps[0], "pre-"+migrationtest.FailingName) {
		t.Fatalf("snapshots = %v, want one pre-migration snapshot", snaps)
	}
}

func TestMissingKeyForExistingInstallFailsClosed(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	m := runManager(t, dataDir, nil)
	if err := m.stop(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dataDir, config.SecretKeyFileName)); err != nil {
		t.Fatal(err)
	}
	_, err := Start(testutil.Context(t), Options{Config: testConfig(dataDir), Logger: testutil.Logger(t), UI: testUI})
	if !errors.Is(err, ErrSecretKeyMissing) {
		t.Fatalf("Start = %v, want ErrSecretKeyMissing", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, config.SecretKeyFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a replacement key was generated")
	}
}

func TestAuditTrailWired(t *testing.T) {
	ctx := testutil.Context(t)
	cfg := testConfig(filepath.Join(t.TempDir(), "data"))
	cfg.Audit = config.AuditConfig{RetentionDays: 30, MaxBytes: 64 << 20, LogMirror: true}
	logger, logs := testutil.CaptureLogger()
	m, err := Start(ctx, Options{Config: cfg, Logger: logger, UI: testUI, Clock: testutil.FakeClock()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	// The job engine records into the manager's audit trail.
	j, _, err := m.Jobs().Enqueue(ctx, jobs.Request{Kind: jobspec.ContainerRestart, Principal: authz.Service(), EnvironmentID: "env-1",
		Targets: []domain.JobTarget{{Type: domain.TargetContainer, ID: "web"}}})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := m.Audit().Records(ctx, domain.AuditFilter{JobID: j.ID})
	if err != nil || len(recs) != 1 || recs[0].Action != audit.ActionJobQueued || recs[0].Actor != audit.ServiceActor() {
		t.Fatalf("records %+v %v", recs, err)
	}
	if rep, err := m.Audit().Verify(ctx); err != nil || !rep.OK || rep.Checked != 1 {
		t.Fatalf("verify %+v %v", rep, err)
	}
	// DOCKER_MANAGER_AUDIT_LOG_MIRROR mirrors records to the structured log.
	if !strings.Contains(logs.String(), `"component":"audit_mirror"`) || !strings.Contains(logs.String(), `"action":"job.queued"`) {
		t.Fatalf("mirror missing: %s", logs)
	}
}
