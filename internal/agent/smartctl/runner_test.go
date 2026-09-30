package smartctl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
}

func fakeSmartctl() int {
	dir := filepath.Dir(os.Args[0])
	args := os.Args[1:]
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
	if a.Hang {
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
	if len(devs) != 4 {
		t.Fatalf("devices %+v", devs)
	}
	r, err := f.r.Read(ctx, devs[0])
	if err != nil {
		t.Fatal(err)
	}
	if r.Device.State != protocol.DiskOK || r.Device.Serial != "WD-WX12D3456789" {
		t.Fatalf("%+v", r.Device)
	}
	calls := f.calls()
	if len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	if want := []string{"--scan-open", "--json"}; !reflect.DeepEqual(calls[0].Args, want) {
		t.Errorf("scan args %q", calls[0].Args)
	}
	if want := []string{"--json", "-a", "-n", "standby,3", "-d", "sat", "/dev/sda"}; !reflect.DeepEqual(calls[1].Args, want) {
		t.Errorf("read args %q", calls[1].Args)
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
	r, err := f.r.Read(ctx, ScanDevice{Name: "/dev/sda", Type: "sat"})
	if err != nil || r.Device.State != protocol.DiskFailing || r.Device.Model == "" {
		t.Fatalf("failing disk: %+v, %v", r.Device, err)
	}
	r, err = f.r.Read(ctx, ScanDevice{Name: "/dev/sdb", Type: "sat"})
	if err != nil || !r.Standby || r.Device.State != protocol.DiskSleeping {
		t.Fatalf("sleeping disk: %+v, %v", r, err)
	}
	r, err = f.r.Read(ctx, ScanDevice{Name: "/dev/sdc", Type: "sat"})
	if err != nil || r.Device.ErrorCode != protocol.DiskErrPermissionDenied {
		t.Fatalf("denied: %+v, %v", r.Device, err)
	}
}

func TestRunnerRefusesOptionLikeDevices(t *testing.T) {
	f := newFake(t, map[string]fakeAnswer{})
	for _, d := range []ScanDevice{{Name: "-a", Type: "sat"}, {Name: "/dev/sda", Type: "-d"}, {Name: "/dev/sda", Type: ""}} {
		if _, err := f.r.Read(testutil.Context(t), d); !IsCode(err, CodeFailed) {
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
	if _, err := f.r.Read(testutil.Context(t), sda); !IsCode(err, CodeTimeout) {
		t.Errorf("hanging smartctl: %v", err)
	}
	f.r.Timeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(testutil.Context(t), 300*time.Millisecond)
	defer cancel()
	if _, err := f.r.Read(ctx, sda); !IsCode(err, CodeCancelled) {
		t.Errorf("cancelled read: %v", err)
	}
	if _, err := f.r.Read(testutil.Context(t), ScanDevice{Name: "/dev/sdb", Type: "sat"}); !IsCode(err, CodeFailed) ||
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
	if _, err := f.r.Read(testutil.Context(t), ScanDevice{Name: "/dev/sda", Type: "sat"}); err != nil {
		t.Fatal(err)
	}
	if s := buf.String(); strings.Contains(s, "WD-WX12D3456789") || !strings.Contains(s, "/dev/sda") {
		t.Fatalf("log: %s", s)
	}
}
