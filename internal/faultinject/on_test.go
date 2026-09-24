//go:build faultinject

package faultinject

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestArmedErrorAndBlock(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv(TraceEnv, trace)
	t.Setenv(EnvVar, "p.err:error,p.block:block")
	if err := Point(context.Background(), "p.err"); !errors.Is(err, ErrInjected) {
		t.Fatalf("error point returned %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Point(ctx, "p.block"); !errors.Is(err, context.Canceled) {
		t.Fatalf("block point returned %v", err)
	}
	if err := Point(context.Background(), "p.other"); err != nil {
		t.Fatalf("unarmed point returned %v", err)
	}
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(b)); strings.Join(got, ",") != "p.err,p.block,p.other" {
		t.Fatalf("trace = %v", got)
	}
}

// TestArmedCrashExits re-executes the test binary with a crash point armed.
func TestArmedCrashExits(t *testing.T) {
	if os.Getenv("FAULTINJECT_CRASH_CHILD") == "1" {
		_ = Point(context.Background(), "p.crash")
		os.Exit(0) // not reached when the point crashes
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestArmedCrashExits$")
	cmd.Env = append(os.Environ(), "FAULTINJECT_CRASH_CHILD=1", EnvVar+"=p.crash:crash")
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != ExitCode {
		t.Fatalf("child exit = %v, want code %d", err, ExitCode)
	}
}
