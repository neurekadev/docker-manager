// Package smartctl runs the agent image's pinned smartctl (smartmontools,
// a separate GPL-2.0 program shipped next to the agent, ADR 0005) and
// parses its JSON output into protocol.SMARTDevice values (#143).
//
// Rules (docs/internal/architecture/engine-integration.md, "Process
// execution"): the runner is the agent's only process execution besides
// restic; it runs one fixed binary, never a shell, with a minimal
// environment (LANG=C), bounded output and a per-call timeout. It only
// reads: it never starts a self-test, changes a setting or wakes a disk in
// standby (-n standby). Serial numbers are data, never logged.
package smartctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// DefaultBinary is where the agent image installs smartctl
// (DOCKER_AGENT_SMARTCTL_BINARY).
const DefaultBinary = "/usr/local/bin/smartctl"

// Defaults and bounds.
const (
	// DefaultTimeout bounds one smartctl call (one device read or the
	// scan).
	DefaultTimeout = 30 * time.Second
	// DefaultKillGrace is how long a cancelled smartctl may take to exit
	// after the interrupt before it is killed.
	DefaultKillGrace = 5 * time.Second
	// MaxOutput bounds smartctl's standard output.
	MaxOutput = 4 << 20
	// maxStderrTail bounds the kept end of standard error.
	maxStderrTail = 4 << 10
	// StandbyExit is the exit status smartctl returns for a device in a
	// low-power mode (-n standby,3; smartctl(8) suggests 3 as a unique
	// status: bits 0 and 1 never come together otherwise).
	StandbyExit = 3
)

// Exit status bits (smartctl(8), EXIT STATUS). Bits 2 to 7 describe the
// device: its JSON output is still complete and parsed.
const (
	BitCommandLine   = 1 << 0 // the command line did not parse (also: unknown USB bridge)
	BitOpenFailed    = 1 << 1 // device open failed, no IDENTIFY, or a low-power mode
	BitCommandFailed = 1 << 2 // some SMART or ATA command failed, or a checksum error
	BitDiskFailing   = 1 << 3 // SMART status "DISK FAILING"
	BitPrefail       = 1 << 4 // prefail attributes at or below threshold
	BitPastPrefail   = 1 << 5 // attributes at or below threshold in the past
	BitErrorLog      = 1 << 6 // the device error log has records
	BitSelfTestLog   = 1 << 7 // the self-test log has errors
)

// ExitBits decodes an exit status.
type ExitBits int

// Has reports whether every bit in mask is set.
func (b ExitBits) Has(mask int) bool { return int(b)&mask == mask }

// DeviceProblem reports a bit about the device's health (3 to 7).
func (b ExitBits) DeviceProblem() bool {
	return int(b)&(BitDiskFailing|BitPrefail|BitPastPrefail|BitErrorLog|BitSelfTestLog) != 0
}

// Error codes.
const (
	CodeNotInstalled = "not_installed" // the binary is missing
	CodeTimeout      = "timeout"
	CodeCancelled    = "cancelled"
	CodeFailed       = "failed"
)

// Error is a failed call (the process could not run, timed out or its
// output was unusable); a device problem is not an error.
type Error struct {
	Op       string
	Code     string
	ExitCode int
	Message  string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "smartctl " + e.Op + ": " + e.Code
	}
	return "smartctl " + e.Op + ": " + e.Code + ": " + e.Message
}

// IsCode reports whether err is an *Error with code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Runner executes smartctl.
type Runner struct {
	// Binary is the smartctl executable (default DefaultBinary).
	Binary string
	// Logger receives one debug record per call (operation, device name,
	// exit status, duration; never output).
	Logger *slog.Logger
	// Timeout bounds one call (default DefaultTimeout).
	Timeout time.Duration
	// KillGrace (default DefaultKillGrace).
	KillGrace time.Duration
}

// result is one finished call.
type result struct {
	stdout []byte
	exit   ExitBits
	stderr string
}

// run executes smartctl with args. A non-zero exit is not an error (the
// caller decodes the bits); starting, timing out, cancellation and an
// oversized output are.
func (r *Runner) run(ctx context.Context, op, device string, args ...string) (result, error) {
	bin := r.Binary
	if bin == "" {
		bin = DefaultBinary
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	grace := r.KillGrace
	if grace <= 0 {
		grace = DefaultKillGrace
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...) //nolint:gosec // fixed binary; arguments are flags, a device type and a /dev path; no shell
	cmd.Env = env()
	cmd.Cancel = func() error {
		if runtime.GOOS == "windows" {
			return cmd.Process.Kill()
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = grace
	stderr := &tailBuffer{max: maxStderrTail}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result{}, &Error{Op: op, Code: CodeFailed, Message: err.Error()}
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		code := CodeFailed
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			code = CodeNotInstalled
		}
		return result{}, &Error{Op: op, Code: code, Message: err.Error()}
	}
	var out bytes.Buffer
	_, readErr := io.Copy(&out, io.LimitReader(stdout, MaxOutput+1))
	tooLarge := out.Len() > MaxOutput
	_, _ = io.Copy(io.Discard, stdout)
	_ = cmd.Wait()
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	if r.Logger != nil {
		r.Logger.Debug("smartctl", "op", op, "device", device, "exit_code", exit,
			"duration", time.Since(start).Round(time.Millisecond).String())
	}
	res := result{stdout: out.Bytes(), exit: ExitBits(max(exit, 0)), stderr: stderr.String()}
	switch {
	case ctx.Err() != nil:
		return res, &Error{Op: op, Code: CodeCancelled, ExitCode: exit, Message: "cancelled"}
	case runCtx.Err() != nil:
		return res, &Error{Op: op, Code: CodeTimeout, ExitCode: exit, Message: fmt.Sprintf("no answer within %s", timeout)}
	case tooLarge:
		return res, &Error{Op: op, Code: CodeFailed, ExitCode: exit, Message: fmt.Sprintf("output larger than %d bytes", MaxOutput)}
	case readErr != nil:
		return res, &Error{Op: op, Code: CodeFailed, ExitCode: exit, Message: "read output: " + readErr.Error()}
	case exit < 0:
		// Killed by a signal.
		return res, &Error{Op: op, Code: CodeFailed, ExitCode: exit, Message: "terminated"}
	}
	return res, nil
}

// env is the child's whole environment: C messages (parsed as text in
// the JSON messages), nothing inherited.
func env() []string {
	out := []string{"LANG=C"}
	if runtime.GOOS == "windows" {
		// Windows processes need SYSTEMROOT to load system libraries (the
		// tests re-execute the test binary as a fake smartctl).
		if v, ok := os.LookupEnv("SYSTEMROOT"); ok {
			out = append(out, "SYSTEMROOT="+v)
		}
	}
	return out
}

// Scan lists the devices smartctl finds and opens (--scan-open).
func (r *Runner) Scan(ctx context.Context) ([]ScanDevice, error) {
	res, err := r.run(ctx, "scan", "", "--scan-open", "--json")
	if err != nil {
		return nil, err
	}
	devs, perr := parseScan(res.stdout)
	if perr != nil {
		msg := summarize(res.stderr)
		if msg == "" {
			msg = perr.Error()
		}
		return nil, &Error{Op: "scan", Code: CodeFailed, ExitCode: int(res.exit), Message: msg}
	}
	return devs, nil
}

// Read reads one device's SMART data without waking it from standby
// (smartctl --json -a -n standby,3 -d <type> <name>).
func (r *Runner) Read(ctx context.Context, dev ScanDevice) (Reading, error) {
	if !validName(dev.Name) || !validType(dev.Type) {
		return Reading{}, &Error{Op: "read", Code: CodeFailed, Message: "invalid device name or type"}
	}
	res, err := r.run(ctx, "read", dev.Name, "--json", "-a", "-n", fmt.Sprintf("standby,%d", StandbyExit), "-d", dev.Type, dev.Name)
	if err != nil {
		return Reading{}, err
	}
	return parseRead(dev, res.stdout, res.exit, res.stderr), nil
}

// validName accepts the device paths smartctl reports (never an option).
func validName(name string) bool {
	return strings.HasPrefix(name, "/dev/") && len(name) <= 255 && !strings.ContainsAny(name, " \t\n\x00")
}

// validType accepts smartctl device types (sat, nvme, scsi, megaraid,N,
// usbjmicron,0, ...).
func validType(t string) bool {
	if t == "" || len(t) > 64 || strings.HasPrefix(t, "-") {
		return false
	}
	for _, c := range t {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', strings.ContainsRune(",+_-/", c):
		default:
			return false
		}
	}
	return true
}

// summarize keeps the last lines of stderr (bounded).
func summarize(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < 3; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			keep = append([]string{l}, keep...)
		}
	}
	msg := strings.Join(keep, "; ")
	if len(msg) > 256 {
		msg = msg[:256] + "…"
	}
	return msg
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
