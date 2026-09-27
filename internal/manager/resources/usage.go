package resources

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Volume disk usage (#6): the Engine computes it by walking every local
// volume (/system/df), which is slow on large volumes. The service asks
// the agent at most once per VolumeUsageTTL per environment and lets
// concurrent callers share one computation.

const (
	// VolumeUsageTTL is how long an environment's volume sizes are reused.
	VolumeUsageTTL = time.Minute
	// volumeUsageTimeout bounds one computation (the agent's /system/df).
	volumeUsageTimeout = 2 * time.Minute
)

// servesChecker is the part of agents.Hub that tells whether an
// environment's agent serves a request (an N-1 agent closes the session on
// an unknown request name, so newer requests are only sent after it
// advertised them).
type servesChecker interface {
	Online(environmentID string) bool
	EnvironmentServes(environmentID, name string) bool
}

// usageEntry is one environment's latest (or running) computation.
type usageEntry struct {
	done   chan struct{}
	report domain.VolumeUsageReport
	err    error
}

// VolumeUsage returns the sizes of an environment's volumes, computed by
// its agent at most VolumeUsageTTL ago. Concurrent callers wait for the
// same computation; failures are not cached. An agent that predates the
// request yields Unsupported (not an error).
func (s *Service) VolumeUsage(ctx context.Context, env string) (domain.VolumeUsageReport, error) {
	s.usageMu.Lock()
	e := s.usage[env]
	if e != nil {
		select {
		case <-e.done:
			if e.err != nil || s.clk.Now().Sub(e.report.ComputedAt) >= VolumeUsageTTL {
				e = nil
			}
		default: // running: share it
		}
	}
	if e == nil {
		e = &usageEntry{done: make(chan struct{})}
		s.usage[env] = e
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			// Detached from the caller: a closed page must not cancel the
			// walk the next caller would wait for again.
			ctx, cancel := context.WithTimeout(s.lifetime, volumeUsageTimeout)
			defer cancel()
			e.report, e.err = s.computeVolumeUsage(ctx, env)
			close(e.done)
		}()
	}
	s.usageMu.Unlock()
	select {
	case <-e.done:
		return e.report, e.err
	case <-ctx.Done():
		return domain.VolumeUsageReport{}, ctx.Err()
	}
}

func (s *Service) computeVolumeUsage(ctx context.Context, env string) (domain.VolumeUsageReport, error) {
	unsupported := domain.VolumeUsageReport{Unsupported: true, ComputedAt: s.clk.Now()}
	if c, ok := s.opts.Agents.(servesChecker); ok && !c.EnvironmentServes(env, protocol.ReqVolumeUsage) {
		if !c.Online(env) {
			return domain.VolumeUsageReport{}, agentErr(jobs.ErrAgentOffline)
		}
		return unsupported, nil
	}
	raw, err := s.opts.Agents.RequestEnvironment(ctx, env, protocol.ReqVolumeUsage, protocol.VolumeUsageInput{}, volumeUsageTimeout)
	if err != nil {
		err = agentErr(err)
		var de *domain.DockerError
		if errors.As(err, &de) && de.Code == domain.DockerAgentUnsupported {
			return unsupported, nil
		}
		return domain.VolumeUsageReport{}, err
	}
	var out protocol.VolumeUsageOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return domain.VolumeUsageReport{}, &domain.DockerError{Code: domain.DockerEngineError,
			Message: "the agent answered with a malformed " + protocol.ReqVolumeUsage + " response"}
	}
	r := domain.VolumeUsageReport{ComputedAt: s.clk.Now(), Sizes: make(map[string]int64, len(out.Volumes))}
	for _, v := range out.Volumes {
		r.Sizes[v.Name] = v.Size
	}
	return r, nil
}
