package backups

import (
	"errors"
	"fmt"
	"testing"

	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// TestAgentErrorClassTimeout: an agent that did not answer in time is a
// timeout (a slow scope preview), whatever the error's wording.
func TestAgentErrorClassTimeout(t *testing.T) {
	cases := map[error]string{
		fmt.Errorf("scope preview: %w", protocol.ErrRequestTimeout): "timeout",
		jobs.ErrAgentOffline:         "agent_offline",
		errors.New("something else"): "agent_error",
	}
	for err, want := range cases {
		if got := agentErrorClass(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}
