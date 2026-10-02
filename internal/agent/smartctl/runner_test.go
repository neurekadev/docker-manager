package smartctl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

var testEpoch = testutil.Epoch

// The test binary doubles as a fake smartctl: tests link (or copy) it into
// a temporary directory as fakeName next to a scenario.json, and when it
// runs under that name TestMain answers from the scenario instead of
// running the tests. The runner builds the child environment from scratch,
// so the fake finds its scenario next to its own path (argv[0]), records
// its arguments and environment in calls.jsonl and answers per device
// (the last argument) or "scan".
func TestMain(m *testing.M) {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == fakeName {
		os.Exit(fakeSmartctl())
	}
	os.Exit(m.Run())
}

// fakeName is the fake's file name (the test binary itself is
// smartctl.test).
const fakeName = "fake-smartctl"

type fakeCall struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

type fakeAnswer struct {
	// Fixture is a testdata file printed on stdout.
	Fixture string `json:"fixture"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Exit    int    `json:"exit"`
	// Hang blocks until the process is interrupted or killed.
	Hang bool `json:"hang"`
	// Flood prints more than MaxOutput bytes.
	Flood bool `json:"flood"`
	// Stuck hangs and leaves a child holding standard output for a few
	// seconds: like a smartctl stuck in the kernel, killing it does not
	// end the output.
	Stuck bool `json:"stuck"`
}

// holdArg makes the fake a child that keeps the inherited standard output
// open for holdFor.
const (
	holdArg = "--hold-stdout"
	holdFor = 3 * time.Second
)

func fakeSmartctl() int {
	dir := filepath.Dir(os.Args[0])
	args := os.Args[1:]
	if len(args) == 1 && args[0] == holdArg {
		time.Sleep(holdFor)
		return 0
	}
	b, _ := json.Marshal(fakeCall{Args: args, Env: os.Environ()})
	if f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
	answers := map[string]fakeAnswer{}
	if raw, err := os.ReadFile(filepath.Join(dir, "scenario.json")); err == nil {
		_ = json.Unmarshal(raw, &answers)
	}
	key := "scan"
	if len(args) > 0 && args[0] != "--scan-open" {
		key = args[len(args)-1]
	}
	a, ok := answers[key]
	if !ok {
		os.Stderr.WriteString("no scenario for " + key)
		return 1
	}
	if a.Fixture != "" {
		fx, err := os.ReadFile(filepath.Join(dir, "testdata", a.Fixture))
		if err != nil {
			return 1
		}
		_, _ = os.Stdout.Write(fx)
	}
	_, _ = os.Stdout.WriteString(a.Stdout)
	_, _ = os.Stderr.WriteString(a.Stderr)
	if a.Flood {
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for written := 0; written <= MaxOutput; written += len(chunk) {
			if _, err := os.Stdout.Write(chunk); err != nil {
				break
			}
		}
	}
	if a.Stuck {
		child := exec.Command(os.Args[0], holdArg) //nolint:gosec // the test binary itself
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			return 1
		}
	}
	if a.Hang || a.Stuck {
		time.Sleep(time.Minute)
	}
	return a.Exit
}

type fake struct {
	t   *testing.T
	dir string
	r   *Runner
}

// newFake installs the test binary as <tmp>/fake-smartctl with a
// scenario and the fixtures.
func newFake(t *testing.T, answers map[string]fakeAnswer) *fake {
	t.Helper()
	dir := t.TempDir()
	name := fakeName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	if err := os.Link(os.Args[0], bin); err != nil {
		copyFile(t, os.Args[0], bin)
	}
	b, _ := json.Marshal(answers)
	if err := os.WriteFile(filepath.Join(dir, "scenario.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "testdata"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, a := range answers {
		if a.Fixture != "" {
			if err := os.WriteFile(filepath.Join(dir, "testdata", a.Fixture), fixture(t, a.Fixture), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &fake{t: t, dir: dir, r: &Runner{Binary: bin, Logger: testutil.Logger(t), Timeout: 20 * time.Second, KillGrace: 2 * time.Second}}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f *fake) calls() []fakeCall {
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

func TestRunnerScanAndReadArgumentsAndEnvironment(t *testing.T) {
	t.Setenv("DOCKER_MANAGER_TEST_PARENT_VALUE", "must-not-leak")
	f := newFake(t, map[string]fakeAnswer{
		"scan":       {Fixture: "scan.json"},
		"/dev/sda":   {Fixture: "ata_healthy.json"},
		"/dev/nvme0": {Fixture: "nvme_healthy.json"},
	})
	ctx := testutil.Context(t)
	devs, err := f.r.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 6 {
		t.Fatalf("devices %+v", devs)
	}
	r, err := f.r.Read(ctx, devs[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Device.State != protocol.DiskOK || r.Device.Serial != "WD-WX12D3456789" {
		t.Fatalf("%+v", r.Device)
	}
	// A read that may wake the disk does not pass -n standby.
	if _, err := f.r.Read(ctx, devs[0], true); err != nil {
		t.Fatal(err)
	}
	// Only ATA devices are asked for device statistics.
	if _, err := f.r.Read(ctx, devs[3], false); err != nil {
		t.Fatal(err)
	}
	calls := f.calls()
	if len(calls) != 4 {
		t.Fatalf("calls %+v", calls)
	}
	devstat := []string{"-l", "devstat,5", "-l", "devstat,7"}
	if want := slices.Concat([]string{"--json", "-a"}, devstat, []string{"-n", "never", "-d", "sat", "/dev/sda"}); !reflect.DeepEqual(calls[2].Args, want) {
		t.Errorf("waking read args %q", calls[2].Args)
	}
	if want := []string{"--scan-open", "--json"}; !reflect.DeepEqual(calls[0].Args, want) {
		t.Errorf("scan args %q", calls[0].Args)
	}
	if want := slices.Concat([]string{"--json", "-a"}, devstat, []string{"-n", "standby,3", "-d", "sat", "/dev/sda"}); !reflect.DeepEqual(calls[1].Args, want) {
		t.Errorf("read args %q", calls[1].Args)
	}
	if want := []string{"--json", "-a", "-n", "standby,3", "-d", "nvme", "/dev/nvme0"}; !reflect.DeepEqual(calls[3].Args, want) {
		t.Errorf("nvme read args %q", calls[3].Args)
	}
	for _, c := range calls {
		for _, e := range c.Env {
			k, _, _ := strings.Cut(e, "=")
			if e == "LANG=C" || (runtime.GOOS == "windows" && strings.EqualFold(k, "SYSTEMROOT")) {
				continue
			}
			t.Errorf("child environment has %q", e)
		}
	}
}

func TestRunnerReadsDeviceProblemsFromExitBits(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{
		"/dev/sda": {Fixture: "ata_when_failed.json", Exit: BitDiskFailing | BitPrefail},
		"/dev/sdb": {Fixture: "standby.json", Exit: StandbyExit},
		"/dev/sdc": {Fixture: "permission_denied.json", Exit: BitOpenFailed},
	})
	ctx := testutil.Context(t)
	r, err := f.r.Read(ctx, ScanDevice{Name: "/dev/sda", Type: "sat"}, false)
	if err != nil || r.Device.State != protocol.DiskFailing || r.Device.Model == "" {
		t.Fatalf("failing disk: %+v, %v", r.Device, err)
	}
	r, err = f.r.Read(ctx, ScanDevice{Name: "/dev/sdb", Type: "sat"}, false)
	if err != nil || !r.Standby || r.Device.State != protocol.DiskSleeping {
		t.Fatalf("sleeping disk: %+v, %v", r, err)
	}
	r, err = f.r.Read(ctx, ScanDevice{Name: "/dev/sdc", Type: "sat"}, false)
	if err != nil || r.Device.ErrorCode != protocol.DiskErrPermissionDenied {
		t.Fatalf("denied: %+v, %v", r.Device, err)
	}
}

func TestRunnerRefusesOptionLikeDevices(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{})
	for _, d := range []ScanDevice{{Name: "-a", Type: "sat"}, {Name: "/dev/sda", Type: "-d"}, {Name: "/dev/sda", Type: ""}} {
		if _, err := f.r.Read(testutil.Context(t), d, false); !IsCode(err, CodeFailed) {
			t.Errorf("%+v: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "calls.jsonl")); err == nil {
		t.Error("smartctl ran for a refused device")
	}
}

func TestRunnerTimeoutCancellationAndOutputCap(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{
		"/dev/sda": {Hang: true},
		"/dev/sdb": {Flood: true},
	})
	f.r.Timeout = 300 * time.Millisecond
	f.r.KillGrace = time.Second
	sda := ScanDevice{Name: "/dev/sda", Type: "sat"}
	if _, err := f.r.Read(testutil.Context(t), sda, false); !IsCode(err, CodeTimeout) {
		t.Errorf("hanging smartctl: %v", err)
	}
	f.r.Timeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(testutil.Context(t), 300*time.Millisecond)
	defer cancel()
	if _, err := f.r.Read(ctx, sda, false); !IsCode(err, CodeCancelled) {
		t.Errorf("cancelled read: %v", err)
	}
	if _, err := f.r.Read(testutil.Context(t), ScanDevice{Name: "/dev/sdb", Type: "sat"}, false); !IsCode(err, CodeFailed) ||
		!strings.Contains(err.Error(), "output larger") {
		t.Errorf("flooding smartctl: %v", err)
	}
}

func TestRunnerMissingBinary(t *testing.T) {
	r := &Runner{Binary: filepath.Join(t.TempDir(), "smartctl-missing")}
	if _, err := r.Scan(testutil.Context(t)); !IsCode(err, CodeNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunnerScanFailure(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{"scan": {Stdout: "garbage", Stderr: "smartctl: scan failed\n", Exit: 1}})
	_, err := f.r.Scan(testutil.Context(t))
	if !IsCode(err, CodeFailed) || !strings.Contains(err.Error(), "scan failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunnerLogsNoSerials(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{"/dev/sda": {Fixture: "ata_healthy.json"}})
	logger, buf := testutil.CaptureLogger()
	f.r.Logger = logger
	if _, err := f.r.Read(testutil.Context(t), ScanDevice{Name: "/dev/sda", Type: "sat"}, false); err != nil {
		t.Fatal(err)
	}
	if s := buf.String(); strings.Contains(s, "WD-WX12D3456789") || !strings.Contains(s, "/dev/sda") {
		t.Fatalf("log: %s", s)
	}
}

// TestRunnerGivesUpOnAStuckSmartctl: a smartctl whose output stays open
// after the kill (stuck in the kernel on a dying disk) is given up on
// AbandonAfter later, and its device is not read again until it exits;
// other devices are.
func TestRunnerGivesUpOnAStuckSmartctl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the held output outlives the test's temporary directory on Windows")
	}
	f := newFake(t, map[string]fakeAnswer{
		"/dev/sda": {Stuck: true},
		"/dev/sdb": {Fixture: "ata_healthy.json"},
	})
	f.r.Timeout, f.r.KillGrace, f.r.AbandonAfter = 200*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond
	sda := ScanDevice{Name: "/dev/sda", Type: "sat"}
	start := time.Now()
	_, err := f.r.Read(testutil.Context(t), sda, false)
	if !IsCode(err, CodeTimeout) || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("stuck smartctl: %v", err)
	}
	if took := time.Since(start); took >= holdFor {
		t.Fatalf("the call waited for the held output (%s)", took)
	}
	if _, err := f.r.Read(testutil.Context(t), sda, false); !IsCode(err, CodeStuck) {
		t.Fatalf("a second read of the stuck device: %v", err)
	}
	if r, err := f.r.Read(testutil.Context(t), ScanDevice{Name: "/dev/sdb", Type: "sat"}, false); err != nil ||
		r.Device.State != protocol.DiskOK {
		t.Fatalf("another device: %+v, %v", r.Device, err)
	}
}
