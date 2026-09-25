package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/testutil"
	"github.com/neurekadev/dockyard/internal/testutil/canary"
)

// The test binary doubles as a fake restic: when the runner executes it,
// RESTIC_REPOSITORY is set (the runner builds the child environment from
// scratch, so the parent's test environment never has it) and TestMain
// runs fakeRestic instead of the tests. The fake records its arguments,
// environment and the password it read from RESTIC_PASSWORD_FILE into
// <repository>/calls.jsonl and answers from <repository>/scenario.json.
func TestMain(m *testing.M) {
	if repo := os.Getenv("RESTIC_REPOSITORY"); repo != "" {
		os.Exit(fakeRestic(repo))
	}
	os.Exit(m.Run())
}

type fakeCall struct {
	Args        []string `json:"args"`
	Env         []string `json:"env"`
	Password    string   `json:"password"`
	NewPassword string   `json:"newPassword,omitempty"`
	Stdin       string   `json:"stdin,omitempty"`
}

type fakeAnswer struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
	// Hang blocks until the process is interrupted or killed.
	Hang bool `json:"hang"`
}

// command returns the restic command name (the first argument that is not
// a global flag or its value).
func command(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-o" || a == "--retry-lock":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

func fakeRestic(repo string) int {
	pw, err := os.ReadFile(os.Getenv("RESTIC_PASSWORD_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fatal: cannot read password file:", err)
		return 1
	}
	c := fakeCall{Args: os.Args[1:], Env: os.Environ(), Password: string(pw)}
	for i, a := range c.Args {
		if a == "--new-password-file" && i+1 < len(c.Args) {
			np, err := os.ReadFile(c.Args[i+1])
			if err != nil {
				fmt.Fprintln(os.Stderr, "Fatal: cannot read new password file:", err)
				return 1
			}
			c.NewPassword = string(np)
		}
		if a == "--stdin" {
			in, _ := os.ReadFile("/dev/stdin")
			if len(in) == 0 {
				var buf bytes.Buffer
				_, _ = buf.ReadFrom(os.Stdin)
				in = buf.Bytes()
			}
			c.Stdin = string(in)
		}
	}
	b, _ := json.Marshal(c)
	f, err := os.OpenFile(filepath.Join(repo, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
	// Like restic, which stages pack files in TMPDIR while saving.
	pack, err := os.CreateTemp(os.Getenv("TMPDIR"), "restic-temp-pack-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fatal: unable to save snapshot:", err)
		return 1
	}
	_ = pack.Close()
	_ = os.Remove(pack.Name())
	answers := map[string]fakeAnswer{}
	if raw, err := os.ReadFile(filepath.Join(repo, "scenario.json")); err == nil {
		_ = json.Unmarshal(raw, &answers)
	}
	a, ok := answers[command(c.Args)]
	if !ok {
		a = answers["*"]
	}
	fmt.Fprint(os.Stdout, a.Stdout)
	fmt.Fprint(os.Stderr, a.Stderr)
	if a.Hang {
		time.Sleep(time.Minute)
	}
	return a.Exit
}

type fakeRepo struct {
	t    *testing.T
	dir  string
	tmp  string
	r    *Runner
	logs *testutil.LogBuffer
}

func newFake(t *testing.T, answers map[string]fakeAnswer) *fakeRepo {
	t.Helper()
	dir := t.TempDir()
	b, _ := json.Marshal(answers)
	if err := os.WriteFile(filepath.Join(dir, "scenario.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	logger, buf := testutil.CaptureLogger()
	tmp := t.TempDir()
	return &fakeRepo{t: t, dir: dir, tmp: tmp, logs: buf,
		r: &Runner{Binary: os.Args[0], TempDir: tmp, CacheDir: filepath.Join(tmp, "cache"), Logger: logger, KillGrace: 2 * time.Second}}
}

func (f *fakeRepo) calls() []fakeCall {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "calls.jsonl"))
	if err != nil {
		f.t.Fatal(err)
	}
	var out []fakeCall
	for _, l := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var c fakeCall
		if err := json.Unmarshal(l, &c); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func envValue(env []string, key string) (string, bool) {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func TestRunnerDeliversSecretsOutsideArgsAndLogs(t *testing.T) {
	set := canary.New()
	key := set.New(canary.RecoveryKey, "recovery key")
	newKey := set.New(canary.RecoveryKey, "new recovery key")
	access := set.New(canary.S3AccessKey, "s3 access key")
	secret := set.New(canary.S3SecretKey, "s3 secret key")
	// A secret in the parent's environment must not reach restic.
	parentSecret := set.New(canary.EnvValue, "parent environment")
	t.Setenv("DOCKYARD_TEST_PARENT_SECRET", parentSecret)

	f := newFake(t, map[string]fakeAnswer{
		"backup":    {Stdout: `{"message_type":"summary","snapshot_id":"abc123","data_added":10}` + "\n"},
		"snapshots": {Stdout: `[]`},
		"key":       {Stdout: `[]`},
		"*":         {},
	})
	loc := Location{Repository: f.dir, S3: &S3{AccessKeyID: access, SecretAccessKey: secret, Region: "eu-central-1", PathStyle: true}}
	repo := f.r.Open(loc, key)
	ctx := testutil.Context(t)
	if _, err := repo.Backup(ctx, BackupRequest{Paths: []string{"/data"}, Tags: []string{"dockyard"}, Host: "env-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Backup(ctx, BackupRequest{Stdin: strings.NewReader("manifest-bytes"), StdinFilename: "dockyard-manifest.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Snapshots(ctx, SnapshotFilter{Tags: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddKey(ctx, newKey); err != nil {
		t.Fatal(err)
	}
	calls := f.calls()
	if len(calls) != 4 {
		t.Fatalf("calls = %d", len(calls))
	}
	for i, c := range calls {
		set.AssertClean(t, fmt.Sprintf("call %d args", i), strings.Join(c.Args, " "))
		if c.Password != key {
			t.Errorf("call %d: restic read password %q from RESTIC_PASSWORD_FILE", i, c.Password)
		}
		for _, e := range c.Env {
			k, v, _ := strings.Cut(e, "=")
			switch k {
			case "AWS_ACCESS_KEY_ID":
				if v != access {
					t.Errorf("AWS_ACCESS_KEY_ID = %q", v)
				}
			case "AWS_SECRET_ACCESS_KEY":
				if v != secret {
					t.Errorf("AWS_SECRET_ACCESS_KEY not delivered")
				}
			default:
				set.AssertClean(t, fmt.Sprintf("call %d env %s", i, k), v)
			}
		}
		if _, ok := envValue(c.Env, "DOCKYARD_TEST_PARENT_SECRET"); ok {
			t.Errorf("call %d inherited the parent's environment", i)
		}
		if v, _ := envValue(c.Env, "RESTIC_REPOSITORY"); v != f.dir {
			t.Errorf("RESTIC_REPOSITORY = %q", v)
		}
		if !slices.Contains(c.Args, "s3.bucket-lookup=path") {
			t.Errorf("call %d: path-style option missing: %v", i, c.Args)
		}
	}
	if !slices.Equal(calls[0].Args[len(calls[0].Args)-2:], []string{"--", "/data"}) || !slices.Contains(calls[0].Args, "--host") {
		t.Errorf("backup args = %v", calls[0].Args)
	}
	if calls[1].Stdin != "manifest-bytes" || !slices.Contains(calls[1].Args, "dockyard-manifest.json") {
		t.Errorf("stdin backup: %q %v", calls[1].Stdin, calls[1].Args)
	}
	if !slices.Contains(calls[2].Args, "a,b") {
		t.Errorf("snapshot filter args = %v", calls[2].Args)
	}
	if calls[3].NewPassword != newKey {
		t.Errorf("key add read new password %q", calls[3].NewPassword)
	}
	set.AssertClean(t, "logs", f.logs.String())
	if !strings.Contains(f.logs.String(), `"op":"backup"`) {
		t.Errorf("no debug record per call: %s", f.logs.String())
	}
	// Password files never outlive the call.
	entries, _ := os.ReadDir(f.tmp)
	for _, e := range entries {
		if e.Name() != "cache" {
			t.Errorf("left behind in the temp directory: %s", e.Name())
		}
	}
}

func TestRunnerParsesBackupProgressAndSummary(t *testing.T) {
	out := strings.Join([]string{
		`{"message_type":"status","percent_done":0.25,"total_files":8,"files_done":2,"total_bytes":800,"bytes_done":200}`,
		`{"message_type":"error","error":{"message":"permission denied"},"during":"archival","item":"/data/locked"}`,
		`{"message_type":"summary","files_new":3,"files_changed":1,"files_unmodified":4,"data_added":4096,"total_files_processed":8,"total_bytes_processed":800,"snapshot_id":"5f2a"}`,
	}, "\n") + "\n"
	f := newFake(t, map[string]fakeAnswer{"backup": {Stdout: out, Exit: 3}})
	var got []Progress
	sum, err := f.r.Open(Location{Repository: f.dir}, "pw-1234567890").Backup(testutil.Context(t), BackupRequest{Paths: []string{"/data"},
		Progress: func(p Progress) { got = append(got, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if sum.SnapshotID != "5f2a" || sum.DataAdded != 4096 || sum.FilesNew != 3 || sum.TotalBytesProcessed != 800 || !sum.Incomplete {
		t.Errorf("summary = %+v", sum)
	}
	if len(sum.Errors) != 1 || !strings.Contains(sum.Errors[0], "/data/locked") {
		t.Errorf("errors = %v", sum.Errors)
	}
	if len(got) != 1 || got[0].Percent != 25 || got[0].FilesDone != 2 {
		t.Errorf("progress = %+v", got)
	}
}

func TestRunnerClassifiesFailuresWithoutSecrets(t *testing.T) {
	set := canary.New()
	key := set.New(canary.RecoveryKey, "key")
	cases := []struct {
		name   string
		answer fakeAnswer
		want   string
	}{
		{"wrong password exit code", fakeAnswer{Stderr: "Fatal: wrong password or no key found\n", Exit: 12}, CodeKeyRejected},
		{"missing repository exit code", fakeAnswer{Stderr: "Fatal: repository does not exist\n", Exit: 10}, CodeRepositoryNotFound},
		{"locked", fakeAnswer{Stderr: "repo already locked\n", Exit: 11}, CodeLocked},
		{"access denied", fakeAnswer{Stderr: "Fatal: unable to open config file: Stat: Access Denied.\n", Exit: 1}, CodeAccessDenied},
		{"missing bucket", fakeAnswer{Stderr: "Fatal: unable to open config file: Stat: The specified bucket does not exist.\n", Exit: 1}, CodeRepositoryNotFound},
		{"s3 forbidden", fakeAnswer{Stderr: "Fatal: create repository failed: client.BucketExists: Access Denied.\n", Exit: 1}, CodeAccessDenied},
		{"unreachable", fakeAnswer{Stderr: "Fatal: dial tcp 10.0.0.1:9000: connect: connection refused\n", Exit: 1}, CodeUnreachable},
		// The fake echoes the key on stderr: the error must not.
		{"leaky stderr", fakeAnswer{Stderr: "Fatal: something failed with " + key + "\n", Exit: 1}, CodeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, map[string]fakeAnswer{"*": tc.answer})
			_, err := f.r.Open(Location{Repository: f.dir}, key).Config(testutil.Context(t))
			var e *Error
			if !errors.As(err, &e) || e.Code != tc.want {
				t.Fatalf("error = %v, want code %s", err, tc.want)
			}
			if e.ErrorClass() != tc.want || e.Recovery() == "" {
				t.Errorf("classed error: %q %q", e.ErrorClass(), e.Recovery())
			}
			set.AssertClean(t, "error", err.Error())
		})
	}
}

func TestRunnerCheckFailureMeansDamaged(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{"check": {Stderr: "error: load <data/1234>: invalid data returned\nFatal: repository contains errors\n", Exit: 1}})
	_, err := f.r.Open(Location{Repository: f.dir}, "password-123456").Check(testutil.Context(t), CheckRequest{ReadDataSubset: "10%"})
	if !IsCode(err, CodeRepositoryDamaged) {
		t.Fatalf("check error = %v", err)
	}
	if c := f.calls()[0]; !slices.Contains(c.Args, "--read-data-subset") || !slices.Contains(c.Args, "10%") {
		t.Errorf("check args = %v", c.Args)
	}
}

func TestRunnerCancellation(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{"backup": {Hang: true}})
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan error, 1)
	go func() {
		_, err := f.r.Open(Location{Repository: f.dir}, "password-123456").Backup(ctx, BackupRequest{Paths: []string{"/x"}})
		done <- err
	}()
	// Wait until the fake recorded its call, i.e. the process runs.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(f.dir, "calls.jsonl")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake restic did not start")
		}
		time.Sleep(20 * time.Millisecond) // polling for process start, not an assertion
	}
	cancel()
	select {
	case err := <-done:
		if !IsCode(err, CodeCancelled) {
			t.Fatalf("error = %v, want cancelled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("cancelled restic did not stop")
	}
}

// TestRunnerFailsFastOnPermanentRetries: restic retries S3 errors it does
// not consider permanent for 15 minutes (a wrong secret key, an unknown key
// ID, a missing bucket); the runner ends restic at the first such retry
// notice and classifies it. Transient retries are left to restic.
func TestRunnerFailsFastOnPermanentRetries(t *testing.T) {
	const load = "Load(<config/0000000000>, 0, 0) returned error, retrying after 1.176s: "
	for _, tc := range []struct {
		name, cause, code string
	}{
		{"wrong secret key", "The request signature we calculated does not match the signature you provided. Check your key and signing method.", CodeAccessDenied},
		{"unknown key id", "The Access Key Id you provided does not exist in our records.", CodeAccessDenied},
		{"clock skew", "The difference between the request time and the server's time is too large.", CodeAccessDenied},
		{"missing bucket", "The specified bucket does not exist", CodeRepositoryNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// After the notice restic would retry for 15 minutes (the
			// fake hangs for one).
			f := newFake(t, map[string]fakeAnswer{"cat": {Stderr: "subprocess ssh: ignored line\n" + load + tc.cause + "\n", Hang: true}})
			_, err := f.r.Open(Location{Repository: f.dir}, "password-123456").Config(testutil.Context(t))
			var e *Error
			if !errors.As(err, &e) || e.Code != tc.code || !strings.Contains(e.Message, "returned error, retrying") {
				t.Fatalf("error = %#v, want %s", err, tc.code)
			}
		})
	}
	// A transient error (here a timeout) is retried by restic: the run
	// ends with restic's own result.
	f := newFake(t, map[string]fakeAnswer{"cat": {Stdout: `{"version":2,"id":"repo-id-1","chunker_polynomial":"3a"}`,
		Stderr: load + "Get \"https://s3.example/\": net/http: timeout awaiting response headers\nLoad(<config/0000000000>, 0, 0) operation successful after 1 retries\n"}})
	if cfg, err := f.r.Open(Location{Repository: f.dir}, "password-123456").Config(testutil.Context(t)); err != nil || cfg.ID != "repo-id-1" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
}

// TestRetryWatchSplitsLines: notices split across writes are recognized
// once, later output is still kept for the error message.
func TestRetryWatchSplitsLines(t *testing.T) {
	aborted := 0
	w := &retryWatch{tail: &tailBuffer{max: maxStderrTail}, abort: func() { aborted++ }}
	for _, p := range []string{"Save(<data/ab>) returned error, retr", "ying after 2s: Access Denied.", "\nFatal: x\n", "Load(<a>) returned error, retrying after 1s: Access Denied.\n"} {
		if n, err := w.Write([]byte(p)); err != nil || n != len(p) {
			t.Fatal(n, err)
		}
	}
	if w.code != CodeAccessDenied || aborted != 1 || !strings.HasSuffix(w.tail.String(), "Access Denied.\n") {
		t.Fatalf("code %q, aborted %d, tail %q", w.code, aborted, w.tail.String())
	}
}

func TestRunnerCreatesItsTempDir(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{"snapshots": {Stdout: "[]"}})
	// A fresh data volume has no tmp directory yet.
	f.r.TempDir = filepath.Join(f.tmp, "data", "tmp")
	if _, err := f.r.Open(Location{Repository: f.dir}, "password-123456").Snapshots(testutil.Context(t), SnapshotFilter{}); err != nil {
		t.Fatalf("snapshots: %v", err)
	}
	fi, err := os.Stat(f.r.TempDir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("temp dir not created: %v", err)
	}
	if got, _ := envValue(f.calls()[0].Env, "TMPDIR"); got != f.r.TempDir {
		t.Fatalf("TMPDIR = %q, want %q", got, f.r.TempDir)
	}
}

func TestRunnerMissingBinary(t *testing.T) {
	r := &Runner{Binary: filepath.Join(t.TempDir(), "no-restic"), TempDir: t.TempDir()}
	_, err := r.Open(Location{Repository: t.TempDir()}, "password-123456").Snapshots(testutil.Context(t), SnapshotFilter{})
	if !IsCode(err, CodeUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunnerParsesListings(t *testing.T) {
	ls := strings.Join([]string{
		`{"time":"2026-09-01T02:00:00Z","paths":["/data"],"id":"abc","struct_type":"snapshot","message_type":"snapshot"}`,
		`{"name":"data","type":"dir","path":"/data","mode":2147484141,"struct_type":"node","message_type":"node"}`,
		`{"name":"a.txt","type":"file","path":"/data/a.txt","size":12,"uid":0,"gid":0,"struct_type":"node","message_type":"node"}`,
		`{"name":"b.txt","type":"file","path":"/data/b.txt","size":3,"struct_type":"node","message_type":"node"}`,
	}, "\n")
	f := newFake(t, map[string]fakeAnswer{
		"ls":        {Stdout: ls},
		"snapshots": {Stdout: `[{"time":"2026-09-01T02:00:00Z","paths":["/data"],"hostname":"env-1","tags":["dockyard","set:1"],"id":"abcdef","short_id":"abcdef12"}]`},
		"key":       {Stdout: `[{"current":true,"id":"k1","userName":"dockyard","hostName":"dockyard","created":"2026-09-01 02:00:00"}]`},
		"cat":       {Stdout: `{"version":2,"id":"repo-id-1","chunker_polynomial":"3a"}`},
		"init":      {Stdout: `{"message_type":"initialized","id":"repo-id-2","repository":"/x"}`},
	})
	repo := f.r.Open(Location{Repository: f.dir}, "password-123456")
	ctx := testutil.Context(t)
	l, err := repo.Ls(ctx, "abc", "/data", false, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Nodes) != 2 || !l.Truncated || l.Nodes[1].Path != "/data/a.txt" || l.Nodes[1].Size != 12 {
		t.Errorf("listing = %+v", l)
	}
	snaps, err := repo.Snapshots(ctx, SnapshotFilter{})
	if err != nil || len(snaps) != 1 || !snaps[0].HasTag("set:1") || snaps[0].Hostname != "env-1" {
		t.Errorf("snapshots = %+v, %v", snaps, err)
	}
	cfg, err := repo.Config(ctx)
	if err != nil || cfg.ID != "repo-id-1" {
		t.Errorf("config = %+v, %v", cfg, err)
	}
	id, err := repo.Init(ctx)
	if err != nil || id != "repo-id-2" {
		t.Errorf("init = %q, %v", id, err)
	}
	keys, err := repo.Keys(ctx)
	if err != nil || len(keys) != 1 || !keys[0].Current || keys[0].ID != "k1" || keys[0].Created != "2026-09-01 02:00:00" {
		t.Errorf("keys = %+v, %v", keys, err)
	}
}
