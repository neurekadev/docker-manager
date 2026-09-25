//go:build integration

package restic

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestStorage* run the pinned restic (testharness.FetchRestic; on other
// systems DOCKYARD_TEST_RESTIC may name a local restic 0.19.1) against a
// local repository and MinIO. Run with -tags integration.

func realRunner(t *testing.T) *Runner {
	t.Helper()
	bin := os.Getenv("DOCKYARD_TEST_RESTIC")
	if bin == "" {
		var err error
		if bin, err = testharness.FetchRestic(testutil.Context(t)); err != nil {
			t.Fatal(err)
		}
	}
	tmp := t.TempDir()
	return &Runner{Binary: bin, TempDir: tmp, CacheDir: filepath.Join(tmp, "cache"), Logger: testutil.Logger(t), RetryLock: 5 * time.Second}
}

func exerciseRepository(t *testing.T, r *Runner, loc Location) {
	ctx := testutil.Context(t)
	const key = "DYRK-TEST-AAAA-BBBB-CCCC-DDDD"
	repo := r.Open(loc, key)
	id, err := repo.Init(ctx)
	if err != nil || id == "" {
		t.Fatalf("init: %q %v", id, err)
	}
	if _, err := repo.Init(ctx); !IsCode(err, CodeRepositoryExists) {
		t.Errorf("second init: %v", err)
	}
	cfg, err := repo.Config(ctx)
	if err != nil || cfg.ID != id {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	if _, err := r.Open(loc, "DYRK-WRONG-KEY-0000").Config(ctx); !IsCode(err, CodeKeyRejected) {
		t.Errorf("wrong key: %v", err)
	}

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "data", "skip"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"data/a.txt": "alpha", "data/skip/x.log": "skipped", "compose.yaml": "services: {}\n"} {
		if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var progress int
	sum, err := repo.Backup(ctx, BackupRequest{Paths: []string{src}, Excludes: []string{filepath.Join(src, "data", "skip")},
		Tags: []string{"dockyard", "set:s1"}, Host: "env-1", Progress: func(Progress) { progress++ }})
	if err != nil || sum.SnapshotID == "" || sum.Incomplete {
		t.Fatalf("backup: %+v %v", sum, err)
	}
	man, err := repo.Backup(ctx, BackupRequest{Stdin: bytes.NewReader([]byte(`{"m":1}`)), StdinFilename: "dockyard-manifest.json",
		Tags: []string{"dockyard-manifest", "set:s1"}, Host: "env-1"})
	if err != nil {
		t.Fatalf("manifest backup: %v", err)
	}
	snaps, err := repo.Snapshots(ctx, SnapshotFilter{Tags: []string{"dockyard-manifest", "set:s1"}})
	if err != nil || len(snaps) != 1 || snaps[0].ID != man.SnapshotID {
		t.Fatalf("snapshots by tag: %+v %v", snaps, err)
	}
	var buf bytes.Buffer
	if err := repo.Dump(ctx, man.SnapshotID, "/dockyard-manifest.json", &buf); err != nil || buf.String() != `{"m":1}` {
		t.Fatalf("dump: %q %v", buf.String(), err)
	}
	l, err := repo.Ls(ctx, sum.SnapshotID, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range l.Nodes {
		names = append(names, n.Name)
	}
	if !slices.Contains(names, "a.txt") || slices.Contains(names, "x.log") {
		t.Errorf("listing names = %v", names)
	}
	if runtime.GOOS == "linux" { // restoring Windows directory metadata needs privileges
		dst := t.TempDir()
		rs, err := repo.Restore(ctx, RestoreRequest{SnapshotID: sum.SnapshotID, Target: dst})
		if err != nil || rs.FilesRestored == 0 {
			t.Fatalf("restore: %+v %v", rs, err)
		}
		if b, err := os.ReadFile(filepath.Join(dst, src, "data", "a.txt")); err != nil || string(b) != "alpha" {
			t.Errorf("restored a.txt = %q, %v", b, err)
		}
	}
	if err := repo.AddKey(ctx, "DYRK-NEXT-KEY-1111"); err != nil {
		t.Fatal(err)
	}
	keys, err := repo.Keys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys: %+v %v", keys, err)
	}
	next := r.Open(loc, "DYRK-NEXT-KEY-1111")
	for _, k := range keys {
		if k.Current {
			if err := next.RemoveKey(ctx, k.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := repo.Config(ctx); !IsCode(err, CodeKeyRejected) {
		t.Errorf("old key after rotation: %v", err)
	}
	if _, err := next.Check(ctx, CheckRequest{ReadDataSubset: "100%"}); err != nil {
		t.Fatalf("check: %v", err)
	}
	if err := next.Forget(ctx, []string{sum.SnapshotID}); err != nil {
		t.Fatal(err)
	}
	if err := next.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := next.Stats(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStorageResticRunnerLocalRepository(t *testing.T) {
	r := realRunner(t)
	exerciseRepository(t, r, Location{Repository: filepath.Join(t.TempDir(), "repo")})
	if _, err := r.Open(Location{Repository: filepath.Join(t.TempDir(), "missing")}, "x-key-123456").Snapshots(testutil.Context(t), SnapshotFilter{}); !IsCode(err, CodeRepositoryNotFound) {
		t.Errorf("missing repository: %v", err)
	}
}

func TestStorageResticRunnerMinIO(t *testing.T) {
	r := realRunner(t)
	m := testharness.StartMinIO(t, testharness.MinIOOptions{Buckets: []string{"dockyard"}})
	loc := Location{Repository: m.ResticRepository("dockyard", "prefix/dockyard-manager"),
		S3: &S3{AccessKeyID: m.AccessKey, SecretAccessKey: m.SecretKey, Region: m.Region, PathStyle: true}}
	exerciseRepository(t, r, loc)
	bad := loc
	bad.S3 = &S3{AccessKeyID: m.AccessKey, SecretAccessKey: "wrong-secret-key-000000", Region: m.Region, PathStyle: true}
	if _, err := r.Open(bad, "DYRK-TEST-AAAA-BBBB-CCCC-DDDD").Config(testutil.Context(t)); !IsCode(err, CodeAccessDenied) {
		t.Errorf("wrong S3 secret: %v", err)
	}
}
