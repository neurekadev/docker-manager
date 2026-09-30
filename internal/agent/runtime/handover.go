package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/neurekadev/docker-manager/internal/agent/state"
	"github.com/neurekadev/docker-manager/internal/clock"
)

// HandoverResult is what `docker-agent enroll` reports.
type HandoverResult struct {
	// Enrollment is the outcome for the handed-over token (nil while the
	// agent has not picked it up).
	Enrollment *state.EnrollStatus
	// Online is true once the enrolled agent's environment is online.
	Online bool
	// Health is the last health file content read.
	Health *HealthState
}

// ErrHandoverTimeout means the agent did not finish within the wait.
var ErrHandoverTimeout = errors.New("the agent did not finish enrolling in time; is it running? (check its logs)")

// HandOver gives token to the agent running on stateDir (it polls the
// state directory every few seconds) and waits up to wait for the
// enrollment outcome and then for the session to come online. The token is
// written to the state directory only (0600) and removed by the agent once
// used. With wait 0 it returns right after handing over.
func HandOver(ctx context.Context, stateDir, token string, wait, poll time.Duration, clk clock.Clock) (HandoverResult, error) {
	if err := state.ValidateToken(token); err != nil {
		return HandoverResult{}, err
	}
	st, err := state.Open(stateDir)
	if err != nil {
		return HandoverResult{}, err
	}
	// Reset the status for this token first, so an outcome recorded for the
	// same token earlier is not mistaken for the answer to this handover.
	if err := st.WriteEnrollStatus(state.EnrollStatus{TokenID: state.TokenID(token), Status: state.EnrollPending, At: clk.Now().UTC()}); err != nil {
		return HandoverResult{}, err
	}
	if err := st.SubmitToken(token); err != nil {
		return HandoverResult{}, err
	}
	var res HandoverResult
	if wait <= 0 {
		return res, nil
	}
	id := state.TokenID(token)
	deadline := clk.NewTimer(wait)
	defer deadline.Stop()
	tick := clk.NewTicker(poll)
	defer tick.Stop()
	for {
		if es, err := st.EnrollStatus(); err == nil && es != nil && es.TokenID == id && es.Status != state.EnrollPending {
			res.Enrollment = es
			if es.Status == state.EnrollFailed {
				return res, nil
			}
		}
		if res.Enrollment != nil {
			if h, err := readHealthState(stateDir); err == nil {
				res.Health = h
				if h.Status == StatusOnline && h.AgentID == res.Enrollment.AgentID {
					res.Online = true
					return res, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-deadline.C():
			return res, ErrHandoverTimeout
		case <-tick.C():
		}
	}
}

func readHealthState(stateDir string) (*HealthState, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, HealthFileName))
	if err != nil {
		return nil, err
	}
	var h HealthState
	if err := json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	return &h, nil
}
