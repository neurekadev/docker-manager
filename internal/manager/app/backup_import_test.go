package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/backup"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/backups"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/restic"
	"github.com/neurekadev/docker-manager/internal/testutil"
	"github.com/neurekadev/docker-manager/internal/testutil/canary"
)

// The recovery proof of #24 in process: a clean manager (new data
// directory, no owner, no database of the old one) is given only the
// destination, a newly entered S3 secret and the saved Recovery Keys; it
// finds the backup set through the portable manifests across the manager
// repository and two host repositories, reports what is missing, refuses
// what it cannot import with explicit errors, imports the manager state,
// restarts, revokes every session, API token and agent credential of the
// snapshot, and restores a volume once a host re-attached.

// restartManager closes the manager and starts it again on the same data
// directory (the controlled restart of a staged restore).
func (e *env) restartManager() {
	t := e.t
	t.Helper()
	opts := e.m.opts
	e.srv.Close()
	_ = e.m.Close()
	m, err := Start(testutil.Context(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	e.m = m
	e.srv = httptest.NewServer(m.Handler()) // closed by newEnv's cleanup
}

func (b *backupEnv) insertStack(envID, id string) {
	now := b.clk.Now().UTC()
	st := domain.Stack{ID: id, EnvironmentID: envID, Name: "app", Root: protocol.RootStacks, Dir: "app", Origin: "imported",
		Status: domain.StackDeployed, CreatedAt: now, UpdatedAt: now, Revision: 1}
	if err := store.InsertStack(testutil.Context(b.t), b.m.DB(), &st); err != nil {
		b.t.Fatal(err)
	}
}

type importTest struct {
	OK             bool   `json:"ok"`
	KeyFingerprint string `json:"keyFingerprint"`
	Manager        struct {
		Found bool   `json:"found"`
		Key   string `json:"key"`
	} `json:"manager"`
	Locations []struct {
		Scope      string `json:"scope"`
		Reachable  bool   `json:"reachable"`
		Found      bool   `json:"found"`
		Key        string `json:"key"`
		ErrorClass string `json:"errorClass"`
	} `json:"locations"`
	Sets     int      `json:"sets"`
	Problems []string `json:"problems"`
}

type importPreview struct {
	Sets []struct {
		SetID            string `json:"setId"`
		Completeness     string `json:"completeness"`
		SchemaCompatible bool   `json:"schemaCompatible"`
		KeyBundle        string `json:"keyBundle"`
		Importable       bool   `json:"importable"`
		BlockerCode      string `json:"blockerCode"`
		HostOnly         bool   `json:"hostOnly"`
		Problems         []string
		Members          []struct {
			Kind          string `json:"kind"`
			Scope         string `json:"scope"`
			EnvironmentID string `json:"environmentId"`
			Volume        string `json:"volume"`
			SnapshotID    string `json:"snapshotId"`
			Located       string `json:"located"`
		} `json:"members"`
	} `json:"sets"`
	Problems []string `json:"problems"`
}

func TestBackupImportIntoAFreshManager(t *testing.T) {
	old := newBackupEnv(t)
	owner, password := old.setupOwner()
	// An encrypted setting that must decrypt after the import: the owner's
	// TOTP seed (sealed with the secret-protection key).
	seed, _ := owner.enrollTOTP()
	hostDir := func(name, kind string) string { return filepath.Join(old.root, name, kind) }
	prod := old.connectHost(hostOpts{name: "prod", stacks: hostDir("prod", "stacks"), volumes: hostDir("prod", "volumes")})
	edge := old.connectHost(hostOpts{name: "edge", stacks: hostDir("edge", "stacks"), volumes: hostDir("edge", "volumes")})
	stackProd, stackEdge := "0190a6e0-0000-7000-8000-00000000b001", "0190a6e0-0000-7000-8000-00000000b002"
	old.insertStack(prod.agent.env, stackProd)
	old.insertStack(edge.agent.env, stackEdge)

	repo := old.createS3Repo(owner, "Offsite")
	id := repo.Repository.ID
	key1 := repo.RecoveryKey.Key
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": key1, "backedUp": true})
	_, token := owner.createToken("ci", "allow backup.read @all")
	var pol struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies", map[string]any{"name": "Nightly", "scope": "all", "repositoryId": id,
		"includeManagerState": true,
	}).json(t, &pol)
	if jobs := old.runJobs(owner.must(http.StatusCreated, http.MethodPost, "/api/v1/backup-policies/"+pol.ID+"/runs", nil)); len(jobs) != 3 {
		t.Fatalf("jobs %v", jobs)
	}

	// A rotation after the backup that reached the manager repository
	// only: the host repositories still use key1 (partially rotated), and
	// the set's secret-key bundle is sealed under key1.
	old.clk.Advance(11 * time.Minute)
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/auth/step-ups", map[string]string{"password": password, "totpCode": old.totpCode(seed)})
	var rot struct {
		RecoveryKey struct {
			Key string `json:"key"`
		} `json:"recoveryKey"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/key-rotations", nil, secretOK).json(t, &rot)
	key2 := rot.RecoveryKey.Key
	old.secrets.Register(canary.RecoveryKey, "rotated key", key2)
	var conf struct {
		Jobs []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"jobs"`
	}
	owner.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/recovery-confirmations",
		map[string]any{"recoveryKey": key2, "backedUp": true}).json(t, &conf)
	for _, j := range conf.Jobs {
		if j.Kind != string(jobspec.ManagerVerify) {
			if _, err := old.m.Jobs().Cancel(testutil.Context(t), j.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, j := range conf.Jobs {
		if j.Kind == string(jobspec.ManagerVerify) {
			if got := old.runJob(j.ID); got.State != domain.JobSucceeded {
				t.Fatalf("manager key migration: %s %s", got.State, got.ErrorMessage)
			}
		}
	}
	managerRepo := "s3:" + old.s3.URL + "/backups/docker-manager/docker-manager"
	if pw := old.store.Passwords(managerRepo); len(pw) != 1 || pw[0] != key2 {
		t.Fatalf("manager repository keys %d", len(pw))
	}

	// Honest reporting: one host snapshot disappears from its repository.
	edgeRepo := "s3:" + old.s3.URL + "/backups/docker-manager/docker-manager-env-" + edge.agent.env
	edgeSnaps := old.store.Snapshots(edgeRepo)
	var lost string
	for _, sn := range edgeSnaps {
		if sn.HasTag(backup.ItemTag(backup.VolumeItem("uploads"))) {
			lost = sn.ID
		}
	}
	if lost == "" {
		t.Fatalf("edge snapshots %+v", edgeSnaps)
	}
	if err := old.store.Open(restic.Location{Repository: edgeRepo}, key1).Forget(testutil.Context(t), []string{lost}); err != nil {
		t.Fatal(err)
	}

	// --- a clean manager: no owner, its own data directory ---
	fresh := newBackupEnvWith(t, old.store, old.s3)
	newSecret := fresh.secrets.New(canary.S3SecretKey, "newly issued s3 secret")
	fresh.secrets.Register(canary.RecoveryKey, "key1", key1)
	fresh.secrets.Register(canary.RecoveryKey, "key2", key2)
	anon := fresh.client()
	src := func(extra map[string]any) map[string]any {
		m := map[string]any{"endpoint": old.s3.URL, "bucket": "backups", "prefix": "docker-manager", "pathStyle": true,
			"accessKeyId": old.s3.AccessKey, "secretAccessKey": newSecret, "recoveryKey": key2}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	const (
		testPath    = "/api/v1/setup/backup-imports/connection-tests"
		previewPath = "/api/v1/setup/backup-imports/previews"
		restorePath = "/api/v1/setup/backup-imports/restores"
	)

	// Key loss, a malformed key, a missing repository.
	lostKey, _ := backups.GenerateRecoveryKey(nil)
	var ct importTest
	anon.must(http.StatusOK, http.MethodPost, testPath, src(map[string]any{"recoveryKey": lostKey.String()})).json(t, &ct)
	if ct.OK || ct.Manager.Found && ct.Manager.Key != "" || len(ct.Problems) == 0 || !strings.Contains(strings.Join(ct.Problems, " "), "nobody can decrypt") {
		t.Fatalf("lost key test %+v", ct)
	}
	anon.fail(http.StatusUnprocessableEntity, "backup_import_key_rejected", http.MethodPost, previewPath, src(map[string]any{"recoveryKey": lostKey.String()}))
	// Change the last two characters (to a different pair, so the checksum
	// always fails: the key may already end in "QQ").
	typo := key2[:len(key2)-2] + "QQ"
	if typo == key2 {
		typo = key2[:len(key2)-2] + "AA"
	}
	anon.fail(http.StatusUnprocessableEntity, "recovery_key_malformed", http.MethodPost, previewPath, src(map[string]any{"recoveryKey": typo}))
	anon.must(http.StatusOK, http.MethodPost, testPath, src(map[string]any{"prefix": "elsewhere"})).json(t, &ct)
	if ct.OK || ct.Manager.Found {
		t.Fatalf("missing repository test %+v", ct)
	}

	// The newest key alone: the manager repository opens, the host
	// repositories (still on key1) do not.
	anon.must(http.StatusOK, http.MethodPost, testPath, src(nil)).json(t, &ct)
	if !ct.OK || ct.Manager.Key != "current" || ct.Sets != 1 || len(ct.Locations) != 2 {
		t.Fatalf("connection test %+v", ct)
	}
	for _, l := range ct.Locations {
		if !l.Reachable || l.ErrorClass != restic.CodeKeyRejected {
			t.Errorf("host location without the previous key %+v", l)
		}
	}
	// With the previous key too: partially rotated keys are explained.
	withPrev := map[string]any{"previousRecoveryKey": key1}
	anon.must(http.StatusOK, http.MethodPost, testPath, src(withPrev)).json(t, &ct)
	if !ct.OK || len(ct.Locations) != 2 || !strings.Contains(strings.Join(ct.Problems, " "), "partially rotated") {
		t.Fatalf("connection test with the previous key %+v", ct)
	}
	for _, l := range ct.Locations {
		if !l.Found || l.Key != "previous" {
			t.Errorf("host location %+v", l)
		}
	}

	// A damaged manifest and a set written by a newer Docker Manager.
	mgr := old.store.Open(restic.Location{Repository: managerRepo}, key2)
	ctx := testutil.Context(t)
	if _, err := mgr.Backup(ctx, restic.BackupRequest{Stdin: strings.NewReader("DOCKER-MANAGER-MANIFEST v1 length=999 sha256=00\n{}"),
		StdinFilename: backup.ManifestFile, Tags: []string{backup.TagManifest, backup.SetTag("broken-set")}, Host: backups.ManagerHost}); err != nil {
		t.Fatal(err)
	}
	var pv importPreview
	anon.must(http.StatusOK, http.MethodPost, previewPath, src(withPrev)).json(t, &pv)
	var setID string
	var manager struct{ snapshot string }
	for _, s := range pv.Sets {
		switch s.SetID {
		case "broken-set":
			if s.Importable || s.BlockerCode != "backup_import_manifest_corrupt" {
				t.Errorf("broken set %+v", s)
			}
		default:
			setID = s.SetID
			for _, m := range s.Members {
				if m.Kind == backup.MemberManagerState {
					manager.snapshot = m.SnapshotID
				}
			}
		}
	}
	future := backup.Manifest{Kind: backup.ManifestSet, SetID: "future-set", InstanceID: "x", CreatedAt: old.clk.Now().Add(time.Hour),
		App: backup.AppInfo{Version: "9.9.9"}, Schema: &backup.SchemaInfo{Migrations: []string{"99990101000000_from_the_future"}},
		Members: []backup.Member{{Item: backup.ItemManagerState, Kind: backup.MemberManagerState, Scope: backup.ScopeManager, RepositoryID: id,
			SnapshotID: manager.snapshot, State: backup.StateComplete}}}
	enc, err := backup.EncodeManifest(future)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Backup(ctx, restic.BackupRequest{Stdin: bytes.NewReader(enc), StdinFilename: backup.ManifestFile,
		Tags: []string{backup.TagManifest, backup.SetTag("future-set")}, Host: backups.ManagerHost}); err != nil {
		t.Fatal(err)
	}
	anon.must(http.StatusOK, http.MethodPost, previewPath, src(withPrev)).json(t, &pv)
	if len(pv.Sets) != 3 || pv.Sets[0].SetID != "future-set" || pv.Sets[0].SchemaCompatible || pv.Sets[0].BlockerCode != "backup_import_schema_incompatible" {
		t.Fatalf("preview %+v", pv)
	}
	anon.fail(http.StatusConflict, "backup_import_schema_incompatible", http.MethodPost, restorePath,
		src(map[string]any{"previousRecoveryKey": key1, "setId": "future-set", "confirm": true}))

	// The real set: located across the manager and both host repositories,
	// with the lost snapshot reported missing.
	var set = pv.Sets[1]
	if set.SetID == "broken-set" {
		set = pv.Sets[2]
	}
	if set.SetID != setID || !set.Importable || !set.SchemaCompatible || set.Completeness != backup.StateComplete || len(set.Members) != 5 {
		t.Fatalf("set %+v", set)
	}
	located := map[string]string{}
	for _, m := range set.Members {
		located[m.Kind+"@"+m.EnvironmentID+"/"+m.Volume] = m.Located
	}
	if located["manager_state@/"] != "found" || located["stack@"+prod.agent.env+"/"] != "found" || located["stack@"+edge.agent.env+"/"] != "found" ||
		located["volume@"+prod.agent.env+"/uploads"] != "found" || located["volume@"+edge.agent.env+"/uploads"] != "missing" {
		t.Fatalf("located %v", located)
	}
	// The set's secret key is sealed under key1: without it the import is
	// refused (partially rotated keys), with it the bundle opens.
	anon.must(http.StatusOK, http.MethodPost, previewPath, src(map[string]any{"setId": setID})).json(t, &pv)
	for _, s := range pv.Sets {
		if s.SetID == setID && (s.KeyBundle != "mismatch" || s.Importable || s.BlockerCode != "backup_import_key_rotated") {
			t.Fatalf("bundle without key1 %+v", s)
		}
	}
	anon.fail(http.StatusUnprocessableEntity, "backup_import_key_rotated", http.MethodPost, restorePath, src(map[string]any{"setId": setID, "confirm": true}))
	anon.fail(http.StatusUnprocessableEntity, "validation_failed", http.MethodPost, restorePath, src(map[string]any{"previousRecoveryKey": key1, "setId": setID}))

	// Import.
	j := jobOf(t, anon.must(http.StatusAccepted, http.MethodPost, restorePath,
		src(map[string]any{"previousRecoveryKey": key1, "setId": setID, "confirm": true})))
	if got := fresh.runJob(j); got.State != domain.JobSucceeded {
		t.Fatalf("import: %s %s %s", got.State, got.ErrorClass, got.ErrorMessage)
	}
	if !fresh.m.RestartRequested() {
		t.Fatal("the import did not request the controlled restart")
	}
	var status struct {
		SetupComplete bool `json:"setupComplete"`
		BackupImport  *struct {
			State          string `json:"state"`
			RestartPending bool   `json:"restartPending"`
		} `json:"backupImport"`
	}
	anon.must(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil).json(t, &status)
	if status.SetupComplete || status.BackupImport == nil || status.BackupImport.State != "succeeded" || !status.BackupImport.RestartPending {
		t.Fatalf("setup status %+v", status.BackupImport)
	}

	// The controlled restart applies it.
	fresh.restartManager()
	anon = fresh.client()
	anon.must(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil).json(t, &status)
	if !status.SetupComplete {
		t.Fatal("setup is not complete after the import")
	}
	anon.fail(http.StatusConflict, "setup_complete", http.MethodPost, testPath, src(withPrev))
	if _, err := os.Stat(filepath.Join(fresh.m.opts.Config.DataDir, backups.AppliedRestoreFile)); err == nil {
		t.Error("the restore was not completed")
	}
	pre, _ := filepath.Glob(filepath.Join(fresh.m.opts.Config.DataDir, "pre-restore-*", "docker-manager.db"))
	if len(pre) != 1 {
		t.Errorf("the replaced database was not kept: %v", pre)
	}

	// No session is revived, no API token survives.
	stale := fresh.client()
	stale.cookie = owner.cookie
	stale.fail(http.StatusUnauthorized, "unauthenticated", http.MethodGet, "/api/v1/auth/session", nil)
	fresh.secrets.Register(canary.APIToken, "old token", token)
	fresh.bot(token).refused("a restored API token")
	fresh.secrets.Register(canary.Password, "owner password", password)
	fresh.secrets.Register(canary.TOTPSeed, "owner totp seed", seed)
	fresh.clk.Advance(time.Hour) // past the TOTP steps the old manager used
	restored := fresh.client()
	if s := restored.signIn("owner", password); s.State != "second_factor_required" {
		t.Fatalf("owner sign-in after the import %+v", s)
	}
	s := restored.must(http.StatusOK, http.MethodPost, "/api/v1/auth/session", map[string]string{"totpCode": fresh.totpCode(seed)}).session(t)
	if s.State != "authenticated" || s.User == nil || !s.User.Owner {
		t.Fatalf("owner second factor after the import %+v", s)
	}

	// Agents were revoked: both environments wait for a re-attach.
	var envs struct {
		Items []struct {
			ID      string `json:"id"`
			Online  bool   `json:"online"`
			AgentID string `json:"agentId"`
		} `json:"items"`
	}
	restored.must(http.StatusOK, http.MethodGet, "/api/v1/environments", nil).json(t, &envs)
	if len(envs.Items) != 2 {
		t.Fatalf("environments %+v", envs)
	}
	for _, e := range envs.Items {
		if e.Online || e.AgentID != "" {
			t.Errorf("environment still attached %+v", e)
		}
	}

	// The repository uses the newly entered secret (it decrypts and works),
	// and the newest key became current with key1 as previous.
	var test struct {
		OK bool `json:"ok"`
	}
	r := restored.must(http.StatusOK, http.MethodPost, "/api/v1/backup-repositories/"+id+"/connection-tests", nil)
	r.json(t, &test)
	if !test.OK {
		t.Errorf("the restored repository's connection test failed: %s", r.body)
	}
	var h struct {
		KeyState struct {
			RotationInProgress bool   `json:"rotationInProgress"`
			Generation         int    `json:"generation"`
			Fingerprint        string `json:"fingerprint"`
		} `json:"keyState"`
	}
	restored.must(http.StatusOK, http.MethodGet, "/api/v1/backup-repositories/"+id+"/health", nil).json(t, &h)
	if h.KeyState.Generation != 2 || h.KeyState.Fingerprint != ct.KeyFingerprint || !h.KeyState.RotationInProgress {
		t.Fatalf("key state after the import %+v", h.KeyState)
	}
	var audited bool
	for _, row := range fresh.auditRows() {
		if row.Action == "system.restore" && strings.Contains(row.Details, `"apiTokenCount":1`) && strings.Contains(row.Details, `"agentsRevoked":2`) {
			audited = true
		}
	}
	if !audited {
		for _, row := range fresh.auditRows() {
			if row.Action == "system.restore" {
				t.Logf("system.restore details %s", row.Details)
			}
		}
		t.Error("no system.restore audit record")
	}

	// The index knows the host snapshots; after prod re-attaches its
	// volume is restored (the location still on key1 moves to key2).
	var page struct {
		Items []struct {
			ID            string `json:"id"`
			Kind          string `json:"kind"`
			EnvironmentID string `json:"environmentId"`
		} `json:"items"`
	}
	restored.must(http.StatusOK, http.MethodGet, "/api/v1/backups", nil).json(t, &page)
	var prodVolume string
	for _, it := range page.Items {
		if it.Kind == "volume" && it.EnvironmentID == prod.agent.env {
			prodVolume = it.ID
		}
	}
	if prodVolume == "" {
		t.Fatalf("backups %+v", page.Items)
	}
	prod.agent.stop()
	fresh.connectHost(hostOpts{name: "prod", stacks: hostDir("prod", "stacks"), volumes: hostDir("prod", "volumes"), reattach: prod.agent.env, fe: prod.fe})
	photo := filepath.Join(hostDir("prod", "volumes"), "uploads", "_data", "photo.jpg")
	writeFile(t, photo, "lost in the disaster")
	rj := jobOf(t, restored.must(http.StatusAccepted, http.MethodPost, "/api/v1/backups/"+prodVolume+"/restores",
		map[string]any{"scope": "volume", "confirm": true}))
	if got := fresh.runJob(rj); got.State != domain.JobSucceeded {
		t.Fatalf("volume restore after the import: %s %s %s", got.State, got.ErrorClass, got.ErrorMessage)
	}
	if b, _ := os.ReadFile(photo); string(b) != "jpeg bytes" {
		t.Errorf("photo after the restore %q", b)
	}
	prodRepo := "s3:" + old.s3.URL + "/backups/docker-manager/docker-manager-env-" + prod.agent.env
	if pw := old.store.Passwords(prodRepo); len(pw) != 1 || pw[0] != key2 {
		t.Errorf("prod location keys after use: %d", len(pw))
	}
}
