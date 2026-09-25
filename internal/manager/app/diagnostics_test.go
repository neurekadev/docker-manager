package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/neurekadev/dockyard/internal/buildinfo"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/ids"
	"github.com/neurekadev/dockyard/internal/manager/config"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// unzip returns the files of a zip body.
func unzip(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("support bundle is not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

// TestSupportBundleHasNoSecrets (#34 Done-when 4): with a canary of every
// secret kind in the manager (passwords, TOTP seed, invitation code, API
// token, registry and Git credentials, S3 key pair, Recovery Key, stack
// .env value, enrollment token, agent credential, session cookie and the
// secret-protection key), the owner's support bundle has every section and
// none of the canaries in any encoding; it is owner-only and audited.
func TestSupportBundleHasNoSecrets(t *testing.T) {
	b := newBackupEnv(t)
	owner, _ := b.setupOwner()
	b.connectBackupAgent("prod") // writes a canary .env value into the stack
	ctx := testutil.Context(t)
	extra := canary.New()

	owner.enrollTOTP()
	b.newUser(owner, "sam") // invitation code and password canaries
	_, token := owner.createToken("monitoring", "allow environment.read @all")
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/registries", map[string]any{"name": "Private", "host": "registry.example.com",
		"username": "robot", "secret": b.secrets.New(canary.RegistryCredential, "registry password")})
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/git-credentials", map[string]any{"name": "Git", "host": "git.example.com",
		"username": "builder", "secret": b.secrets.New(canary.RegistryCredential, "git token")})
	repo := b.createS3Repo(owner, "Offsite") // S3 secret key and Recovery Key canaries
	extra.Register(canary.S3AccessKey, "s3 access key", b.s3.AccessKey)
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+repo.Repository.ID+"/recovery-confirmations",
		map[string]any{"recoveryKey": repo.RecoveryKey.Key, "backedUp": true})
	created, err := b.m.Agents().CreateEnrollment(ctx, domain.EnrollmentSpec{EnvironmentName: "spare"})
	if err != nil {
		t.Fatal(err)
	}
	extra.Register(canary.APIToken, "enrollment token", created.Token)
	resp, err := b.m.Agents().Enroll(ctx, created.Token, protocol.EnrollRequest{Protocol: protocol.Version, AgentVersion: buildinfo.Get().Version,
		InstallID: ids.New(), Engine: protocol.EngineInfo{ID: "ENGINE-SPARE", Version: "29.8.1", APIVersion: "1.51", OS: "linux", Arch: "amd64"},
		Hostname: "spare"})
	if err != nil {
		t.Fatal(err)
	}
	extra.Register(canary.APIToken, "agent credential", resp.Credential)
	key, err := os.ReadFile(filepath.Join(b.m.opts.Config.DataDir, config.SecretKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	extra.Register(canary.RecoveryKey, "secret-protection key", strings.TrimSpace(string(key)))
	extra.Register(canary.Password, "session cookie", owner.cookie)
	// Use some of them so the log has lines about them.
	b.bot(token).must(http.StatusOK, http.MethodGet, "/api/v1/environments", nil)
	b.m.opts.Logger.Info("diagnostics test marker")

	r := owner.must(http.StatusOK, http.MethodGet, "/api/v1/support-bundle", nil)
	if r.header.Get("Content-Type") != "application/zip" || !strings.Contains(r.header.Get("Content-Disposition"), "dockyard-support-") {
		t.Fatalf("headers %v", r.header)
	}
	files := unzip(t, r.body)
	for _, name := range []string{"README.txt", "versions.json", "configuration.json", "support-matrix.json", "agents.json",
		"audit-chain.json", "jobs.json", "database.json", "logs.ndjson"} {
		if _, ok := files[name]; !ok {
			t.Errorf("bundle lacks %s", name)
		}
	}
	for name, content := range files {
		for _, set := range []*canary.Set{b.secrets, extra} {
			for _, leak := range set.Scan(content) {
				t.Errorf("%s: %s", name, leak)
			}
		}
	}
	var chain struct {
		OK      bool  `json:"ok"`
		Checked int64 `json:"checked"`
	}
	if err := json.Unmarshal(files["audit-chain.json"], &chain); err != nil || !chain.OK || chain.Checked == 0 {
		t.Errorf("audit chain %s", files["audit-chain.json"])
	}
	var versions struct {
		Manager map[string]any `json:"manager"`
		Agents  []struct {
			EnvironmentName string `json:"environmentName"`
			Compatibility   string `json:"compatibility"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(files["versions.json"], &versions); err != nil || versions.Manager["version"] != buildinfo.Get().Version ||
		len(versions.Agents) != 2 {
		t.Errorf("versions %s", files["versions.json"])
	}
	var cfg struct {
		Settings []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"settings"`
	}
	_ = json.Unmarshal(files["configuration.json"], &cfg)
	if !slices.ContainsFunc(cfg.Settings, func(s struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}) bool {
		return s.Name == config.EnvPublicURL && s.Value == publicOrigin
	}) {
		t.Errorf("configuration %s", files["configuration.json"])
	}
	if !bytes.Contains(files["support-matrix.json"], []byte(`"check": "engine_api"`)) {
		t.Errorf("support matrix %s", files["support-matrix.json"])
	}
	if !bytes.Contains(files["logs.ndjson"], []byte("diagnostics test marker")) || !bytes.Contains(files["logs.ndjson"], []byte(`"request_id"`)) {
		t.Errorf("logs lack recent lines:\n%s", files["logs.ndjson"])
	}

	// Owner only: another user and any API token (even the owner's) are refused.
	sam := b.client()
	sam.signIn("sam", b.passwordOf("sam"))
	sam.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/support-bundle", nil)
	b.bot(token).fail(http.StatusForbidden, "api_token_not_allowed", http.MethodGet, "/api/v1/support-bundle", nil)
	found := false
	for _, row := range b.auditRows() {
		found = found || (row.Action == "system.support_bundle" && row.Outcome == "success")
	}
	if !found {
		t.Error("the download is not audited")
	}
}

// TestMetricsEndpointOffByDefaultAndGated (#34): the Prometheus endpoint
// answers 404 unless DOCKYARD_METRICS_ENABLED; when enabled it needs
// system.metrics.read (a monitoring API token works, one without the grant
// does not) and serves the internal metrics in the text format.
func TestMetricsEndpointOffByDefaultAndGated(t *testing.T) {
	off := newEnv(t)
	owner, _ := off.setupOwner()
	owner.fail(http.StatusNotFound, "not_found", http.MethodGet, "/api/v1/system/metrics", nil)

	e := newEnv(t, func(o *Options) { o.Config.MetricsEnabled = true })
	owner, _ = e.setupOwner()
	e.client().fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/system/metrics", nil)
	_, scrape := owner.createToken("prometheus", "allow system.metrics.read @all")
	_, other := owner.createToken("other", "allow environment.read @all")
	e.bot(other).fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/system/metrics", nil)
	sam, _, _ := e.newUser(owner, "sam")
	sam.fail(http.StatusForbidden, "forbidden", http.MethodGet, "/api/v1/system/metrics", nil)

	r := e.bot(scrape).must(http.StatusOK, http.MethodGet, "/api/v1/system/metrics", nil)
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content type %q", ct)
	}
	body := string(r.body)
	for _, want := range []string{"# TYPE dockyard_job_queue_depth gauge", "dockyard_job_queue_depth 0", "dockyard_agent_sessions 0",
		`dockyard_environments{status="active",online="false"} 0`, "dockyard_sse_streams", "dockyard_event_bus_subscribers",
		`dockyard_database_size_bytes{database="main"}`, "dockyard_audit_chain_records", `dockyard_build_info{version="` + buildinfo.Get().Version} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if !strings.HasPrefix(line, "#") && len(strings.Fields(line)) != 2 && !strings.Contains(line, "{") {
			t.Errorf("malformed sample %q", line)
		}
	}
}
