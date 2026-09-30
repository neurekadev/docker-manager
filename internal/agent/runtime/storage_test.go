package runtime

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/compose"
	"github.com/neurekadev/docker-manager/internal/agent/engine"
	"github.com/neurekadev/docker-manager/internal/agent/engine/enginetest"
	"github.com/neurekadev/docker-manager/internal/agent/storage"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

const stacksDir = "/var/lib/docker/volumes/docker-manager_stacks/_data"

func runWithStorage(t *testing.T, r storage.Result) (*Agent, func()) {
	t.Helper()
	stateDir := t.TempDir()
	logger, logs := testutil.CaptureLogger()
	caps := make(chan Capabilities, 4)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	a, err := New(Options{
		Config:         fakeEngineConfig(t, stateDir, enginetest.Options{}),
		Logger:         logger,
		Clock:          testutil.FakeClock(),
		Geteuid:        func() int { return 0 },
		VerifyStorage:  func(context.Context, engine.Engine) storage.Result { return r },
		OnCapabilities: func(c Capabilities) { caps <- c },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	<-caps
	t.Cleanup(func() { t.Logf("logs:\n%s", logs.String()) })
	return a, func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
		if st := readHealth(t, stateDir); st.Storage != storageStatusOf(r) {
			t.Errorf("health storage %q, want %q", st.Storage, storageStatusOf(r))
		}
	}
}

func storageStatusOf(r storage.Result) string {
	if len(r.Diagnostics) > 0 {
		return r.Diagnostics[0].Code
	}
	return "verified"
}

func TestVerifiedStorageEnablesStacks(t *testing.T) {
	r := storage.Result{Containerized: true, StacksDir: stacksDir, VolumesDir: "/var/lib/docker/volumes",
		Roots: []storage.Root{{Kind: storage.KindStacks, Path: stacksDir, OK: true}, {Kind: storage.KindVolumes, Path: "/var/lib/docker/volumes", OK: true}}}
	a, stop := runWithStorage(t, r)
	defer stop()
	if err := a.StackGuard(stacksDir + "/shop"); err != nil {
		t.Errorf("guard refused a verified stack: %v", err)
	}
	p, ok := a.CapabilitiesPayload()
	if !ok || p.Validate() != nil || !slices.Contains(p.Features, "stacks") || len(p.Roots) != 2 || len(p.Diagnostics) != 0 {
		t.Fatalf("payload %+v (ok %v, validate %v)", p, ok, p.Validate())
	}
	if a.Compose() == nil {
		t.Fatal("compose adapter not connected")
	}
}

func TestFailedStorageCheckRefusesStacksOnly(t *testing.T) {
	diag := storage.Diagnostic{Code: storage.CodePathMismatch, Kind: storage.KindStacks, Path: stacksDir,
		Message: stacksDir + " is mounted into the agent from /srv/other"}
	r := storage.Result{Containerized: true, StacksDir: stacksDir, VolumesDir: "/var/lib/docker/volumes",
		Roots:       []storage.Root{{Kind: storage.KindStacks, Path: stacksDir}, {Kind: storage.KindVolumes, Path: "/var/lib/docker/volumes", OK: true}},
		Diagnostics: []storage.Diagnostic{diag}}
	a, stop := runWithStorage(t, r)
	defer stop()
	var d storage.Diagnostic
	if err := a.StackGuard(stacksDir + "/shop"); !errors.As(err, &d) || d.Code != storage.CodePathMismatch {
		t.Errorf("guard: %v", err)
	}
	// The compose adapter refuses to load stacks, with the diagnostic code:
	// no deploy runs with a path mismatch.
	if _, err := a.Compose().Load(testutil.Context(t), compose.ProjectSpec{Dir: stacksDir + "/shop"}); engine.CodeOf(err) != storage.CodePathMismatch {
		t.Errorf("load from the stacks volume: %v (%s)", err, engine.CodeOf(err))
	}
	p, ok := a.CapabilitiesPayload()
	if !ok || p.Validate() != nil || slices.Contains(p.Features, "stacks") {
		t.Fatalf("payload %+v", p)
	}
	if len(p.Roots) != 1 || p.Roots[0].Kind != storage.KindVolumes {
		t.Errorf("roots %+v", p.Roots)
	}
	if len(p.Diagnostics) != 1 || p.Diagnostics[0] != (protocol.Diagnostic{Area: protocol.DiagnosticStorage, Code: storage.CodePathMismatch, Message: diag.Message, Path: stacksDir}) {
		t.Errorf("diagnostics %+v", p.Diagnostics)
	}
	// The rest of the agent keeps working.
	if a.Engine() == nil || a.Capabilities().EngineError != nil {
		t.Error("Engine unavailable after a storage failure")
	}
}
