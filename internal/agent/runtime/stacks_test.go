package runtime

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/neurekadev/docker-manager/internal/agent/engine/enginetest"
	"github.com/neurekadev/docker-manager/internal/agent/session"
	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/testutil"
)

// TestAgentServesStacksAndResources (#6, #7): the runtime wires the stack
// executors and compose.* requests next to the Docker resource ones, and
// caller-supplied handlers win.
func TestAgentServesStacksAndResources(t *testing.T) {
	custom := func(context.Context, json.RawMessage) (any, error) { return "custom", nil }
	a, err := New(Options{
		Config:  fakeEngineConfig(t, t.TempDir(), enginetest.Options{}),
		Logger:  testutil.Logger(t),
		Clock:   testutil.FakeClock(),
		Geteuid: func() int { return 0 },
		Requests: map[string]session.RequestHandler{
			protocol.ReqComposeRead: custom,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmds := a.commands()
	for _, k := range []string{"stack.deploy", "stack.start", "stack.stop", "stack.restart", "stack.down", "stack.remove", "container.start"} {
		if !slices.Contains(cmds, k) {
			t.Errorf("commands %v lack %s", cmds, k)
		}
	}
	for _, r := range []string{protocol.ReqComposeDiscover, protocol.ReqComposeValidate, protocol.ReqComposeWrite, protocol.ReqComposeServices,
		protocol.ReqContainerList} {
		if a.opts.Requests[r] == nil {
			t.Errorf("request %s not served", r)
		}
	}
	if out, _ := a.opts.Requests[protocol.ReqComposeRead](testutil.Context(t), nil); out != "custom" {
		t.Error("a caller-supplied handler was replaced")
	}
}
