package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/db/migrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/audit"
	"github.com/neurekadev/dockyard/internal/manager/store"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

type fakeEnvs struct {
	envs   []domain.Environment
	agents []domain.Agent
}

func (f fakeEnvs) ListEnvironments(context.Context, domain.EnvironmentFilter) ([]domain.Environment, error) {
	return f.envs, nil
}

func (f fakeEnvs) ListAgents(context.Context, domain.AgentFilter) ([]domain.Agent, error) {
	return f.agents, nil
}

type okAudit struct{}

func (okAudit) Verify(context.Context) (audit.VerifyReport, error) {
	return audit.VerifyReport{OK: true, Checked: 3, HeadSeq: 3}, nil
}

func newService(t *testing.T, ring *logging.Ring, envs fakeEnvs) *Service {
	t.Helper()
	ctx := testutil.Context(t)
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "dockyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: migrations.Migrations, SnapshotDir: filepath.Join(dir, "snapshots"),
		Clock: testutil.FakeClock(), Logger: testutil.Logger(t)}); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{DB: db, Clock: testutil.FakeClock(), Build: buildinfo.Info{Version: "1.4.0", Commit: "abc", GoVersion: "go1.27.1"},
		DatabasePath: filepath.Join(dir, "dockyard.db"), SnapshotDir: filepath.Join(dir, "snapshots"), Migrations: migrations.Migrations,
		Environments: envs, Audit: okAudit{}, Logs: ring, Sessions: func() []string { return []string{"env-1"} }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestBundleLogsAreRedactedAgain: a log line that slipped a secret into a
// sensitive attribute or a credential-shaped value is redacted in the
// bundle, and unparsable lines are omitted.
func TestBundleLogsAreRedactedAgain(t *testing.T) {
	secrets := canary.New()
	pw := secrets.New(canary.Password, "slipped password")
	tok := "dya_" + strings.Repeat("A", 40)
	secrets.Register(canary.APIToken, "slipped agent credential", tok)
	ring := logging.NewRing(10)
	log := slog.New(ring.Handler(slog.LevelInfo))
	log.Info("bad line", "password", pw, "detail", "Authorization: Bearer "+tok, "environment_id", "env-1")
	_, _ = ring.Write([]byte("not json " + pw + "\n"))
	s := newService(t, ring, fakeEnvs{})
	var buf bytes.Buffer
	if err := s.WriteSupportBundle(testutil.Context(t), &buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		for _, l := range secrets.Scan(b) {
			t.Errorf("%s: %s", f.Name, l)
		}
		if f.Name == "logs.ndjson" && (!bytes.Contains(b, []byte(`"environment_id":"env-1"`)) || !bytes.Contains(b, []byte("unparsable log line omitted"))) {
			t.Errorf("logs %s", b)
		}
	}
}

// TestSupportMatrixChecks: the bundle evaluates each active agent against
// the supported host boundary.
func TestSupportMatrixChecks(t *testing.T) {
	caps := `{"agentVersion":"1.3.0","protocols":["dockyard.agent/v1"],"os":"linux","arch":"arm64","engine":{"id":"E","version":"24.0.9",` +
		`"apiVersion":"1.43","os":"linux","arch":"arm64","rootless":true},"commands":[],"requests":[],"streams":[],` +
		`"transport":{"managerUrl":"http://dockyard-manager:8080","plainHttp":true},"diagnostics":[{"area":"storage","code":"storage_path_mismatch","message":"m"}]}`
	s := newService(t, nil, fakeEnvs{envs: []domain.Environment{{ID: "env-1", Name: "NAS", Status: domain.EnvironmentActive}},
		agents: []domain.Agent{{ID: "ag-1", EnvironmentID: "env-1", Status: domain.AgentActive, Version: "1.3.0", Capabilities: caps}}})
	a := domain.Agent{ID: "ag-1", EnvironmentID: "env-1", Status: domain.AgentActive, Version: "1.3.0"}
	got := map[string]string{}
	var pc protocol.CapabilitiesPayload
	if err := json.Unmarshal([]byte(caps), &pc); err != nil {
		t.Fatal(err)
	}
	parsed := s.supportChecks(a, &pc, "NAS")
	for _, ch := range parsed.Checks {
		got[ch.Check] = ch.Status
	}
	want := map[string]string{"agent_version": "warning", "engine_api": "unsupported", "platform": "ok", "rootless": "unsupported",
		"transport": "warning", "agent_diagnostics": "unsupported"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (%v)", k, got[k], v, got)
		}
	}
	if !apiAtLeast("1.51", "1.44") || apiAtLeast("1.43", "1.44") || apiAtLeast("x", "1.44") || !apiAtLeast("2.0", "1.44") {
		t.Error("apiAtLeast")
	}
}

// TestMetricsFormat: every sample line is "name{labels} value" with
// escaped label values, and each family has HELP and TYPE.
func TestMetricsFormat(t *testing.T) {
	s := newService(t, nil, fakeEnvs{agents: []domain.Agent{{ID: "a", Status: domain.AgentActive, Version: "1.2.0"}}})
	s.o.Build.Commit = "we\"ird\\commit\n"
	var buf bytes.Buffer
	if err := s.WriteMetrics(testutil.Context(t), &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `commit="we\"ird\\commit\n"`) || !strings.Contains(out, `dockyard_agents{compatibility="unsupported"} 1`) {
		t.Fatalf("metrics:\n%s", out)
	}
	families := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.HasPrefix(line, "# TYPE "):
			families++
		case strings.HasPrefix(line, "# HELP "):
		default:
			i := strings.LastIndexByte(line, ' ')
			if i <= 0 || strings.ContainsAny(line[i+1:], " {}") {
				t.Errorf("sample %q", line)
			}
		}
	}
	if families < 12 {
		t.Errorf("%d families", families)
	}
}
