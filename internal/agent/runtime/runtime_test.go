package runtime

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/testutil"
)

func testConfig(stateDir string) config.Config {
	return config.Config{
		ManagerURL:      &url.URL{Scheme: "https", Host: "docker.example.com"},
		EnrollmentToken: "dy-enroll-CANARY",
		StateDir:        stateDir,
		DockerHost:      config.DefaultDockerHost,
	}
}

func TestRefusesNonRoot(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	err := Run(testutil.Context(t), Options{
		Config:  testConfig(stateDir),
		Logger:  testutil.Logger(t),
		Clock:   testutil.FakeClock(),
		Geteuid: func() int { return 1000 },
	})
	if !errors.Is(err, ErrNotRoot) || !strings.Contains(err.Error(), "UID 1000") {
		t.Fatalf("Run = %v, want ErrNotRoot", err)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state dir created before the root check")
	}
}

func TestLoopUpdatesHealthFile(t *testing.T) {
	stateDir := t.TempDir()
	clk := testutil.FakeClock()
	logger, logs := testutil.CaptureLogger()
	writes := make(chan time.Time, 4)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Config:           testConfig(stateDir),
			Logger:           logger,
			Clock:            clk,
			Geteuid:          func() int { return 0 },
			afterHealthWrite: func(ts time.Time) { writes <- ts },
		})
	}()

	if ts := <-writes; !ts.Equal(testutil.Epoch) {
		t.Fatalf("first health write at %v", ts)
	}
	if err := CheckHealth(stateDir, clk.Now(), HealthMaxAge); err != nil {
		t.Fatalf("fresh agent unhealthy: %v", err)
	}
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(HealthInterval)
	if ts := <-writes; !ts.Equal(testutil.Epoch.Add(HealthInterval)) {
		t.Fatalf("second health write at %v", ts)
	}

	// Freshness is judged against the last write.
	last := testutil.Epoch.Add(HealthInterval)
	if err := CheckHealth(stateDir, last.Add(HealthMaxAge), HealthMaxAge); err != nil {
		t.Fatalf("health within max age failed: %v", err)
	}
	if err := CheckHealth(stateDir, last.Add(HealthMaxAge+time.Second), HealthMaxAge); err == nil {
		t.Fatal("stale health file accepted")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "enrollment arrives with #3") {
		t.Fatalf("missing not-enrolled notice: %s", out)
	}
	if strings.Contains(out, "CANARY") {
		t.Fatalf("enrollment token leaked into logs: %s", out)
	}
}

func TestCheckHealthMissingOrCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := CheckHealth(dir, testutil.Epoch, HealthMaxAge); err == nil {
		t.Fatal("missing file accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, HealthFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckHealth(dir, testutil.Epoch, HealthMaxAge); err == nil {
		t.Fatal("corrupt file accepted")
	}
}
