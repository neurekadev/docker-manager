package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/config"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/engine/enginetest"
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

// fakeEngineConfig returns a config whose DOCKER_HOST is a scripted fake
// Engine API, so the default Engine and Compose connectors are used.
func fakeEngineConfig(t *testing.T, stateDir string, opts enginetest.Options) config.Config {
	t.Helper()
	cfg := testConfig(stateDir)
	cfg.DockerHost = enginetest.Start(t, opts).Host
	return cfg
}

func readHealth(t *testing.T, dir string) HealthState {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, HealthFileName))
	if err != nil {
		t.Fatal(err)
	}
	var st HealthState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
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
	a, err := New(Options{
		Config:           fakeEngineConfig(t, stateDir, enginetest.Options{APIVersion: "1.44"}),
		Logger:           logger,
		Clock:            clk,
		Geteuid:          func() int { return 0 },
		afterHealthWrite: func(ts time.Time) { writes <- ts },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	if ts := <-writes; !ts.Equal(testutil.Epoch) {
		t.Fatalf("first health write at %v", ts)
	}
	if err := CheckHealth(stateDir, clk.Now(), HealthMaxAge); err != nil {
		t.Fatalf("fresh agent unhealthy: %v", err)
	}
	if st := readHealth(t, stateDir); st.Engine != "connected" || st.Status != StatusNotEnrolled {
		t.Fatalf("health %+v", st)
	}
	caps := a.Capabilities()
	if caps.Engine == nil || caps.Engine.NegotiatedAPIVersion != "1.44" || caps.Engine.EngineID != "FAKE:ENGINE:ID" || caps.EngineError != nil {
		t.Fatalf("capabilities %+v", caps)
	}
	// The protocol frame (#3) validates and carries the Engine identity.
	payload, ok := a.CapabilitiesPayload()
	if !ok || payload.Validate() != nil || payload.Engine.ID != "FAKE:ENGINE:ID" || payload.Engine.APIVersion != "1.44" ||
		!slices.Contains(payload.Features, "engine."+engine.CapCompose) {
		t.Fatalf("capabilities payload %+v (ok %v, validate %v)", payload, ok, payload.Validate())
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
	// The configured value is not a dye_ token: refused without echoing it.
	if !strings.Contains(out, "DOCKYARD_ENROLLMENT_TOKEN is not an enrollment token") {
		t.Fatalf("missing invalid-token notice: %s", out)
	}
	if !strings.Contains(out, `"negotiated_api_version":"1.44"`) || !strings.Contains(out, `"engine_id":"FAKE:ENGINE:ID"`) {
		t.Fatalf("Engine identity not logged: %s", out)
	}
	if strings.Contains(out, "CANARY") {
		t.Fatalf("enrollment token leaked into logs: %s", out)
	}
	if a.Engine() != nil {
		t.Error("Engine not closed on shutdown")
	}
}

func TestRetriesEngineConnectionWithBackoff(t *testing.T) {
	stateDir := t.TempDir()
	clk := testutil.FakeClock()
	cfg := fakeEngineConfig(t, stateDir, enginetest.Options{})
	var attempts atomic.Int32
	tried := make(chan int32, 8)
	caps := make(chan Capabilities, 8)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	a, err := New(Options{
		Config:  cfg,
		Logger:  testutil.Logger(t),
		Clock:   clk,
		Geteuid: func() int { return 0 },
		ConnectEngine: func(ctx context.Context) (engine.Engine, error) {
			n := attempts.Add(1)
			defer func() { tried <- n }()
			if n <= 2 {
				return nil, engine.Errorf("engine.connect", engine.CodeEngineUnavailable, "Cannot connect to the Docker daemon")
			}
			return engine.Connect(ctx, engine.Options{Host: cfg.DockerHost})
		},
		OnCapabilities: func(c Capabilities) { caps <- c },
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.Run(ctx) }()

	if c := <-caps; c.Engine != nil || c.EngineError == nil || c.EngineError.Code != engine.CodeEngineUnavailable {
		t.Fatalf("first capabilities %+v", c)
	}
	// Retry timer (2s) and health ticker are armed.
	if err := clk.BlockUntilWaiters(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if st := readHealth(t, stateDir); st.Engine != string(engine.CodeEngineUnavailable) {
		t.Fatalf("health engine status %q", st.Engine)
	}
	if n := <-tried; n != 1 {
		t.Fatalf("attempt %d", n)
	}
	clk.Advance(EngineRetryMin) // second attempt fails, backoff doubles
	if n := <-tried; n != 2 {
		t.Fatalf("attempt %d", n)
	}
	if err := clk.BlockUntilWaiters(ctx, 2); err != nil { // retry timer re-armed
		t.Fatal(err)
	}
	clk.Advance(EngineRetryMin) // not yet: the backoff is 4s now
	if len(tried) != 0 {
		t.Fatal("retried before the doubled backoff elapsed")
	}
	clk.Advance(EngineRetryMin)
	c := <-caps
	if c.Engine == nil || c.EngineError != nil || attempts.Load() != 3 {
		t.Fatalf("after recovery: %+v (attempts %d)", c, attempts.Load())
	}
}

func TestUnsupportedEngineKeepsAgentRunning(t *testing.T) {
	stateDir := t.TempDir()
	clk := testutil.FakeClock()
	caps := make(chan Capabilities, 4)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	defer cancel()
	a, err := New(Options{
		Config:         fakeEngineConfig(t, stateDir, enginetest.Options{APIVersion: "1.41"}),
		Logger:         testutil.Logger(t),
		Clock:          clk,
		Geteuid:        func() int { return 0 },
		OnCapabilities: func(c Capabilities) { caps <- c },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	c := <-caps
	if c.Engine != nil || c.EngineError == nil || c.EngineError.Code != engine.CodeUnsupportedAPIVersion ||
		!strings.Contains(c.EngineError.Message, "1.44") {
		t.Fatalf("capabilities %+v", c)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
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
