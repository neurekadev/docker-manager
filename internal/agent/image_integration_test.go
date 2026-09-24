//go:build integration

package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/testharness"
)

// TestEngineAgentImage runs the built agent image (deploy/docker/agent.Dockerfile)
// inside a DinD Engine of the matrix with the deploy/compose mounts and
// proves #2's second Done-when bullet: the agent starts with local Engine
// access (connects and negotiates through the Moby SDK), becomes healthy and
// has no listening socket in its network namespace (read from /proc/net by a
// sidecar sharing that namespace).
func TestEngineAgentImage(t *testing.T) {
	img := testharness.AgentImage(t)
	want, err := testharness.SelectEngine()
	if err != nil {
		t.Fatal(err)
	}
	e := testharness.StartEngine(t, testharness.EngineOptions{Version: want})
	e.LoadWorkload(t)
	e.LoadHostImage(t, img)
	id := e.StartAgent(t, testharness.AgentOptions{Image: img, Name: "dockyard-agent"})

	logs := e.WaitLog(t, id, "connected to Docker Engine", 3*time.Minute)
	negotiated := testharness.MinVersion("1.56", want.APIVersion)
	for _, s := range []string{`"engine_version":"` + want.Version + `"`, `"negotiated_api_version":"` + negotiated + `"`, `"docker_root_dir":"/var/lib/docker"`} {
		if !strings.Contains(logs, s) {
			t.Errorf("agent log lacks %s:\n%s", s, logs)
		}
	}
	e.WaitHealthy(t, id, 3*time.Minute)
	if l := e.Listeners(t, id); len(l) != 0 {
		t.Fatalf("the agent has listening sockets: %v", l)
	}
	// Positive control: the same check detects a listening container.
	listener := e.StartWorkload(t, "listen 7070")
	e.WaitLog(t, listener, "listening", time.Minute)
	if l := e.Listeners(t, listener); len(l) == 0 || !strings.HasPrefix(l[0], "tcp") {
		t.Fatalf("listener check missed a listening socket: %v", l)
	}
	t.Logf("agent on Engine %s: connected (API %s), healthy, no listeners", want.Version, negotiated)
}
