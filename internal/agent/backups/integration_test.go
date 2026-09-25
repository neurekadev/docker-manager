//go:build integration

package backups

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
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
	res, _, err := e.run(testutil.Context(t), jobspec.BackupRun, in, secrets, nil)
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
	ctx := testutil.Context(t)
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
	for _, k := range keys {
		if strings.Contains(k, "html") || strings.Contains(k, "index") {
			t.Errorf("plaintext file name in the bucket: %s", k)
		}
	}
	// Missing S3 credentials are an explicit failure.
	bad := &protocol.CommandSecrets{Repositories: []protocol.RepositoryCredential{{RepositoryID: ref.RepositoryID, Password: key,
		AccessKeyID: m.AccessKey, SecretAccessKey: "wrong-secret-access-key-000"}}}
	in := e.runInput(false, stackItem(protocol.BackupRules{}))
	in.Repository = ref
	res, _, _ := e.run(testutil.Context(t), jobspec.BackupRun, in, bad, nil)
	if res.Outcome != jobexec.OutcomeFailed || res.ErrorClass != restic.CodeAccessDenied {
		t.Errorf("wrong S3 secret: %+v", res)
	}
}
