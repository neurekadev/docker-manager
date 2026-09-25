//go:build integration

package backups

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/backup"
	"github.com/neurekadev/dockyard/internal/jobexec"
	"github.com/neurekadev/dockyard/internal/jobspec"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/restic"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestBackup* run the agent's backup executors with the pinned restic
// (testharness.FetchRestic; DOCKYARD_TEST_RESTIC may name a local restic
// 0.19.1) against a local repository and MinIO, over the fake Engine:
// stack and volume snapshots with container shutdown, host manifests,
// retention and verification that detects a truncated pack.

func realRestic(t *testing.T) *restic.Runner {
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

func runRealBackup(t *testing.T, e *env, ref protocol.BackupRepositoryRef, key string, secrets *protocol.CommandSecrets) protocol.BackupRunOutput {
	t.Helper()
	in := e.runInput(true, stackItem(protocol.BackupRules{}), protocol.BackupItem{Kind: backup.MemberVolume, Volume: "uploads"})
	in.Repository = ref
	res, _, err := e.run(testutil.ContextWithin(t, 5*time.Minute), jobspec.BackupRun, in, secrets, nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("backup: %+v %v", res, err)
	}
	out := outputOf(t, res)
	for _, m := range out.Members {
		if m.State != backup.StateComplete || m.SnapshotID == "" {
			t.Fatalf("member %+v", m)
		}
	}
	return out
}

func TestBackupRunWithRealResticLocal(t *testing.T) {
	e := newEnv(t)
	r := realRestic(t)
	e.svc.opts.Restic = r
	const key = "DYRK-INTEGRATION-LOCAL-KEY"
	ref := e.repoRef()
	out := runRealBackup(t, e, ref, key, e.credential(key))
	loc := ref.Destination.Location(ref.Scope, backup.S3Credentials{})
	repo := r.Open(loc, key)
	// Many restic runs share this context.
	ctx := testutil.ContextWithin(t, 5*time.Minute)
	snaps, err := repo.Snapshots(ctx, restic.SnapshotFilter{Tags: []string{backup.SetTag("set-1")}})
	if err != nil || len(snaps) != 3 {
		t.Fatalf("snapshots %+v %v", snaps, err)
	}
	var buf bytes.Buffer
	if err := repo.Dump(ctx, out.ManifestSnapshotID, "/"+backup.ManifestFile, &buf); err != nil {
		t.Fatal(err)
	}
	if m, err := backup.DecodeManifest(buf.Bytes()); err != nil || len(m.Members) != 2 {
		t.Fatalf("manifest %+v %v", m, err)
	}
	// Retention: a second run, then keep one (floor 1).
	runRealBackup(t, e, ref, key, e.credential(key))
	ret := protocol.BackupRetentionInput{Repository: ref, PolicyID: "pol-1", Rules: backup.RetentionRules{Last: 1, MinKeep: 1}, TimeZone: "UTC"}
	res, _, err := e.run(ctx, jobspec.BackupRetention, ret, e.credential(key), nil)
	if err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("retention: %+v %v", res, err)
	}
	var rout protocol.RetentionOutput
	_ = json.Unmarshal(res.Output, &rout)
	if len(rout.Forgotten) != 2 || rout.Kept != 2 {
		t.Errorf("retention output %+v", rout)
	}
	// Verification passes, then detects a truncated pack file.
	res, _, _ = e.run(ctx, jobspec.BackupVerify, protocol.BackupVerifyInput{Repository: ref, ReadDataSubset: "100%"}, e.credential(key), nil)
	if res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("verify: %+v", res)
	}
	truncatePack(t, filepath.FromSlash(loc.Repository))
	res, _, _ = e.run(ctx, jobspec.BackupVerify, protocol.BackupVerifyInput{Repository: ref, ReadDataSubset: "100%"}, e.credential(key), nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != restic.CodeRepositoryDamaged {
		t.Errorf("truncated pack: %+v", res)
	}
}

// truncatePack cuts the largest pack file of a local repository in half.
func truncatePack(t *testing.T, repo string) {
	t.Helper()
	var biggest string
	var size int64
	_ = filepath.WalkDir(filepath.Join(repo, "data"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil && fi.Size() > size {
				biggest, size = p, fi.Size()
			}
		}
		return nil
	})
	if biggest == "" {
		t.Fatal("no pack file")
	}
	if err := os.Chmod(biggest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(biggest, size/2); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreWithRealRestic restores a volume, a stack definition and one
// file from snapshots written by the real restic (local repository).
func TestRestoreWithRealRestic(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("restoring snapshot paths needs Linux (restic restores the full path's directory metadata)")
	}
	e := newEnv(t)
	r := realRestic(t)
	e.svc.opts.Restic = r
	e.store = nil
	const key = restoreKey
	ref := e.repoRef()
	out := runRealBackup(t, e, ref, key, e.credential(key))
	members := map[string]backup.Member{}
	for _, m := range out.Members {
		members[m.Item] = m
	}
	// Many restic runs share this context.
	ctx := testutil.ContextWithin(t, 5*time.Minute)
	up := filepath.Join(e.volumes, "uploads", "_data")
	write(t, filepath.Join(up, "a.jpg"), "changed")
	write(t, filepath.Join(up, "junk"), "junk")
	vin := e.restoreInput(members[backup.VolumeItem("uploads")], protocol.RestoreScopeVolume)
	if res, _, err := e.run(ctx, jobspec.RestoreRun, vin, e.credential(key), nil); err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("volume restore: %+v %v", res, err)
	}
	if read(t, filepath.Join(up, "a.jpg")) != "jpeg" || read(t, filepath.Join(up, "junk")) != "<missing>" {
		t.Error("volume not restored")
	}
	index := filepath.Join(e.project, "html", "index.html")
	write(t, index, "defaced")
	sin := e.restoreInput(members[backup.StackItem("st-app")], protocol.RestoreScopeStack)
	if res, _, err := e.run(ctx, jobspec.RestoreRun, sin, e.credential(key), nil); err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("stack restore: %+v %v", res, err)
	}
	if read(t, index) != "<h1>hi</h1>" {
		t.Error("stack not restored")
	}
	write(t, index, "again")
	fin := sin
	fin.Scope, fin.File = protocol.RestoreScopeFile, snapPath(index)
	if res, _, err := e.run(ctx, jobspec.RestoreRun, fin, e.credential(key), nil); err != nil || res.Outcome != jobexec.OutcomeSucceeded {
		t.Fatalf("file restore: %+v %v", res, err)
	}
	if read(t, index) != "<h1>hi</h1>" {
		t.Error("file not restored")
	}
}

func TestBackupRunWithRealResticMinIO(t *testing.T) {
	e := newEnv(t)
	r := realRestic(t)
	e.svc.opts.Restic = r
	m := testharness.StartMinIO(t, testharness.MinIOOptions{Buckets: []string{"dockyard"}})
	const key = "DYRK-INTEGRATION-S3-KEY"
	ref := e.repoRef()
	ref.Destination = backup.Destination{Kind: backup.KindS3, Endpoint: m.Endpoint, Bucket: "dockyard", Prefix: "site-a", Region: m.Region, PathStyle: true}
	secrets := &protocol.CommandSecrets{Repositories: []protocol.RepositoryCredential{{RepositoryID: ref.RepositoryID, Password: key,
		AccessKeyID: m.AccessKey, SecretAccessKey: m.SecretKey}}}
	runRealBackup(t, e, ref, key, secrets)
	keys, err := m.S3.ListKeys(testutil.Context(t), "dockyard", "site-a/"+backup.ScopeDir(ref.Scope)+"/")
	if err != nil || len(keys) == 0 {
		t.Fatalf("objects: %v %v", keys, err)
	}
	// Only restic's own layout (config, keys/, index/, data/xx/, snapshots/,
	// locks/ with hex object names) reaches the bucket: no file name of
	// the backed-up tree (e.g. html/index.html) appears in plain text.
	layout := regexp.MustCompile(`^site-a/` + regexp.QuoteMeta(backup.ScopeDir(ref.Scope)) +
		`/(config|(keys|index|snapshots|locks)/[0-9a-f]{64}|data/[0-9a-f]{2}/[0-9a-f]{64})$`)
	for _, k := range keys {
		if !layout.MatchString(k) || strings.Contains(k, "html") {
			t.Errorf("object outside restic's encrypted layout: %s", k)
		}
	}
	// Wrong S3 credentials are an explicit failure, reported at once
	// (Context's 30 s, not restic's 15 minutes of retries).
	bad := &protocol.CommandSecrets{Repositories: []protocol.RepositoryCredential{{RepositoryID: ref.RepositoryID, Password: key,
		AccessKeyID: m.AccessKey, SecretAccessKey: "wrong-secret-access-key-000"}}}
	in := e.runInput(false, stackItem(protocol.BackupRules{}))
	in.Repository = ref
	res, _, _ := e.run(testutil.Context(t), jobspec.BackupRun, in, bad, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != restic.CodeAccessDenied {
		t.Errorf("wrong S3 secret: %+v", res)
	}
}
