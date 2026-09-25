//go:build integration

package app

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/restic"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// The recovery proof of #24 with the pinned restic (testharness.FetchRestic;
// DOCKYARD_TEST_RESTIC may name a local restic 0.19.1): a clean manager
// imports a local repository mounted at a new path, and an S3 (MinIO)
// destination with two host repositories, then restores a stack, a volume
// and a file after the hosts re-attach. The Engine is the in-memory fake;
// only MinIO needs Docker.

func realResticRunner(t *testing.T) *restic.Runner {
	t.Helper()
	bin := os.Getenv("DOCKYARD_TEST_RESTIC")
	if bin == "" {
		var err error
		if bin, err = testharness.FetchRestic(testutil.Context(t)); err != nil {
			t.Fatal(err)
		}
	}
	tmp := t.TempDir()
	return &restic.Runner{Binary: bin, TempDir: tmp, CacheDir: filepath.Join(tmp, "cache"), Logger: testutil.Logger(t), RetryLock: 10 * time.Second}
}

// withWallClockStart starts the manager's fake clock at the wall-clock
// time. Tests against a real S3 server need it: the manager signs S3
// requests (s3probe, SigV4) with its clock, and the server refuses
// signatures more than 15 minutes off its own time (403
// RequestTimeTooSkewed). The fake clock still only moves when a test
// advances it.
func withWallClockStart(o *Options) { o.Clock = clock.NewFake(time.Now().UTC().Truncate(time.Second)) }

// importThroughSetup runs the setup import of the newest importable set
// and restarts the manager; it returns the imported set ID.
func importThroughSetup(t *testing.T, fresh *backupEnv, src map[string]any) string {
	t.Helper()
	anon := fresh.client()
	var ct importTest
	anon.must(http.StatusOK, http.MethodPost, "/api/v1/setup/backup-imports/connection-tests", src).json(t, &ct)
	if !ct.OK || !ct.Manager.Found || ct.Sets == 0 {
		t.Fatalf("connection test %+v", ct)
	}
	var pv importPreview
	anon.must(http.StatusOK, http.MethodPost, "/api/v1/setup/backup-imports/previews", src).json(t, &pv)
	if len(pv.Sets) == 0 || !pv.Sets[0].Importable {
		t.Fatalf("preview %+v", pv)
	}
	setID := pv.Sets[0].SetID
	for _, m := range pv.Sets[0].Members {
		if m.Located != "found" {
			t.Errorf("member %+v not found", m)
		}
	}
	body := map[string]any{"setId": setID, "confirm": true}
	for k, v := range src {
		body[k] = v
	}
	anon.must(http.StatusOK, http.MethodPost, "/api/v1/setup/backup-imports/previews", body).json(t, &pv)
	j := jobOf(t, anon.must(http.StatusAccepted, http.MethodPost, "/api/v1/setup/backup-imports/restores", body))
	if got := fresh.runJob(j); got.State != domain.JobSucceeded {
		t.Fatalf("import: %s %s %s", got.State, got.ErrorClass, got.ErrorMessage)
	}
	if !fresh.m.RestartRequested() {
		t.Fatal("no controlled restart was requested")
	}
	fresh.restartManager()
	return setID
}

func TestBackupImportWithRealResticLocal(t *testing.T) {
	r := realResticRunner(t)
	old := newBackupEnvOn(t, r, nil, nil)
	owner, password := old.setupOwner()
	oldPath := filepath.ToSlash(filepath.Join(old.root, "manager-backups", "dockyard"))
	var repo createdRepo
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-repositories", map[string]any{"name": "Local", "kind": "local",
		"executor": "manager", "path": oldPath}, secretOK).json(t, &repo)
	key := repo.RecoveryKey.Key
	old.secrets.Register(canary.RecoveryKey, "recovery key", key)
	id := repo.Repository.ID
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": key, "backedUp": true})
	var pol struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "Manager", "repositoryId": id,
		"includeManagerState": true}).json(t, &pol)
	old.runJobs(owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil))

	// The disaster: the old manager is gone; its repository directory is
	// mounted at another path on a clean manager.
	fresh := newBackupEnvOn(t, r, nil, nil)
	fresh.secrets.Register(canary.RecoveryKey, "recovery key", key)
	newPath := filepath.ToSlash(filepath.Join(fresh.root, "manager-backups", "remounted"))
	if err := os.MkdirAll(filepath.Dir(filepath.FromSlash(newPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.FromSlash(oldPath), filepath.FromSlash(newPath)); err != nil {
		t.Fatal(err)
	}
	importThroughSetup(t, fresh, map[string]any{"kind": "local", "path": newPath, "recoveryKey": key})

	fresh.secrets.Register(canary.Password, "owner password", password)
	restored := fresh.client()
	restored.signIn("owner", password)
	var got struct {
		Path string `json:"path"`
	}
	restored.must(http.StatusOK, http.MethodGet, "/api/v1/backup-repositories/"+id, nil).json(t, &got)
	if got.Path != newPath {
		t.Fatalf("repository path %q, want %q", got.Path, newPath)
	}
	fresh.runJobs(restored.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil))
}

func TestBackupImportWithRealResticMinIO(t *testing.T) {
	r := realResticRunner(t)
	m := testharness.StartMinIO(t, testharness.MinIOOptions{Buckets: []string{"dockyard"}})
	old := newBackupEnvOn(t, r, nil, nil, withWallClockStart)
	old.secrets.Register(canary.S3SecretKey, "minio secret", m.SecretKey)
	owner, password := old.setupOwner()
	hostDir := func(name, kind string) string { return filepath.Join(old.root, name, kind) }
	prod := old.connectHost(hostOpts{name: "prod", stacks: hostDir("prod", "stacks"), volumes: hostDir("prod", "volumes")})
	edge := old.connectHost(hostOpts{name: "edge", stacks: hostDir("edge", "stacks"), volumes: hostDir("edge", "volumes")})
	stackProd, stackEdge := "0190a6e0-0000-7000-8000-00000000c001", "0190a6e0-0000-7000-8000-00000000c002"
	old.insertStack(prod.agent.env, stackProd)
	old.insertStack(edge.agent.env, stackEdge)
	var repo createdRepo
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-repositories", map[string]any{"name": "MinIO", "kind": "s3",
		"endpoint": m.Endpoint, "bucket": "dockyard", "prefix": "site", "region": m.Region, "pathStyle": true,
		"accessKeyId": m.AccessKey, "secretAccessKey": m.SecretKey}, secretOK).json(t, &repo)
	key := repo.RecoveryKey.Key
	old.secrets.Register(canary.RecoveryKey, "recovery key", key)
	id := repo.Repository.ID
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": key, "backedUp": true})
	_, token := owner.createToken("ci", "allow backup.read @all")
	var pol struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "Nightly", "repositoryId": id,
		"includeManagerState": true, "stacks": []map[string]any{{"stackId": stackProd}, {"stackId": stackEdge}},
		"volumes": []map[string]any{{"environmentId": prod.agent.env, "volume": "uploads"}, {"environmentId": edge.agent.env, "volume": "uploads"}},
	}).json(t, &pol)
	if jobs := old.runJobs(owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil)); len(jobs) != 3 {
		t.Fatalf("jobs %v", jobs)
	}

	// A clean manager with only the bucket, the S3 key pair entered anew and
	// the Recovery Key.
	fresh := newBackupEnvOn(t, r, nil, nil, withWallClockStart)
	fresh.secrets.Register(canary.RecoveryKey, "recovery key", key)
	fresh.secrets.Register(canary.S3SecretKey, "minio secret", m.SecretKey)
	src := map[string]any{"kind": "s3", "endpoint": m.Endpoint, "bucket": "dockyard", "prefix": "site", "region": m.Region, "pathStyle": true,
		"accessKeyId": m.AccessKey, "secretAccessKey": m.SecretKey, "recoveryKey": key}
	var ct importTest
	fresh.client().must(http.StatusOK, http.MethodPost, "/api/v1/setup/backup-imports/connection-tests", src).json(t, &ct)
	if len(ct.Locations) != 2 {
		t.Fatalf("host repositories %+v", ct.Locations)
	}
	for _, l := range ct.Locations {
		if !l.Reachable || !l.Found || l.Key != "current" {
			t.Errorf("host repository %+v", l)
		}
	}
	importThroughSetup(t, fresh, src)

	fresh.secrets.Register(canary.Password, "owner password", password)
	fresh.secrets.Register(canary.APIToken, "old token", token)
	fresh.bot(token).refused("a restored API token")
	restored := fresh.client()
	restored.signIn("owner", password)

	// Both hosts re-attach; then a volume, a stack and one file are restored.
	prod.agent.stop()
	edge.agent.stop()
	fresh.connectHost(hostOpts{name: "prod", stacks: hostDir("prod", "stacks"), volumes: hostDir("prod", "volumes"), reattach: prod.agent.env, fe: prod.fe})
	fresh.connectHost(hostOpts{name: "edge", stacks: hostDir("edge", "stacks"), volumes: hostDir("edge", "volumes"), reattach: edge.agent.env, fe: edge.fe})
	if runtime.GOOS != "linux" {
		t.Skip("restoring snapshot paths needs Linux (restic restores the full path's directory metadata)")
	}
	var page struct {
		Items []struct {
			ID            string `json:"id"`
			Kind          string `json:"kind"`
			EnvironmentID string `json:"environmentId"`
		} `json:"items"`
	}
	restored.must(http.StatusOK, http.MethodGet, "/api/v1/backups", nil).json(t, &page)
	ids := map[string]string{}
	for _, it := range page.Items {
		ids[it.Kind+"@"+it.EnvironmentID] = it.ID
	}
	restore := func(backupID string, body map[string]any) {
		t.Helper()
		body["confirm"] = true
		j := jobOf(t, restored.must(http.StatusAccepted, http.MethodPost, "/api/v1/backups/"+backupID+"/restores", body))
		if got := fresh.runJob(j); got.State != domain.JobSucceeded {
			t.Fatalf("restore %v: %s %s %s", body, got.State, got.ErrorClass, got.ErrorMessage)
		}
	}
	photo := filepath.Join(hostDir("prod", "volumes"), "uploads", "_data", "photo.jpg")
	writeFile(t, photo, "lost")
	restore(ids["volume@"+prod.agent.env], map[string]any{"scope": "volume"})
	if b, _ := os.ReadFile(photo); string(b) != "jpeg bytes" {
		t.Errorf("volume after the restore %q", b)
	}
	index := filepath.Join(hostDir("edge", "stacks"), "app", "html", "index.html")
	writeFile(t, index, "defaced")
	restore(ids["stack@"+edge.agent.env], map[string]any{"scope": "stack"})
	if b, _ := os.ReadFile(index); string(b) != "<h1>backup me</h1>" {
		t.Errorf("stack after the restore %q", b)
	}
	writeFile(t, index, "defaced again")
	var contents struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	restored.must(http.StatusOK, http.MethodGet, "/api/v1/backups/"+ids["stack@"+edge.agent.env]+"/contents?recursive=true", nil).json(t, &contents)
	var snapIndex string
	for _, en := range contents.Entries {
		if strings.HasSuffix(en.Path, "/html/index.html") {
			snapIndex = en.Path
		}
	}
	restore(ids["stack@"+edge.agent.env], map[string]any{"scope": "file", "path": snapIndex})
	if b, _ := os.ReadFile(index); string(b) != "<h1>backup me</h1>" {
		t.Errorf("file after the restore %q", b)
	}
}
