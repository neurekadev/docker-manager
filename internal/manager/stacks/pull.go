package stacks

import (
	"context"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// Pull enqueues a stack.pull job: the agent pulls the images of the
// stack's definition on disk (with the registry connections a deploy would
// use, #19) and touches no container; the next deploy runs what it pulled.
// The finish hook marks the services whose reference now names another
// image than the one they run. Agents without the feature answer
// agent_unsupported (environment_offline while offline).
func (s *Service) Pull(ctx context.Context, p authz.Principal, st domain.Stack, r domain.StackJobRequest) (domain.Job, error) {
	fh, ok := s.opts.Agents.(interface {
		EnvironmentHasFeature(environmentID, feature string) bool
	})
	if !ok || !fh.EnvironmentHasFeature(st.EnvironmentID, protocol.FeatureStackPull) {
		if !s.Online(ctx, st.EnvironmentID) {
			return domain.Job{}, agentError(jobs.ErrAgentOffline)
		}
		return domain.Job{}, &domain.StackError{Code: domain.StackErrEnvironmentUnsupported,
			Message: "the environment's agent cannot pull a stack's images without deploying them yet; upgrade it"}
	}
	regs, err := s.registryConnections(ctx, st)
	if err != nil {
		return domain.Job{}, err
	}
	r.Services, r.TimeoutSeconds = nil, 0
	return s.enqueue(ctx, p, st, jobspec.StackPull, r, protocol.StackJobInput{RegistryConnections: regs})
}

// onPullFinished records, per service the stack runs, whether the pull
// left a newer image for its reference on the host (a different image ID
// than the applied one). Status, applied revision and last deploy time do
// not change: nothing was deployed.
func (s *Service) onPullFinished(ctx context.Context, db bun.IDB, j domain.Job) error {
	st, err := store.GetStack(ctx, db, stackTarget(j))
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	out, ok := s.output(j)
	if ok {
		markPulled(&st, out.Images, s.now())
	}
	if ok && out.After != nil {
		s.setEngine(&st, statesFrom(out.After))
	}
	st.UpdatedAt = s.now()
	if err := store.UpdateStack(ctx, db, &st); err != nil {
		return err
	}
	s.log.Info("stack images pulled", "stack_id", st.ID, "state", j.State, "pulled", len(out.Pulled), "job_id", j.ID)
	s.publish(EventUpdated, st, map[string]string{"jobId": j.ID, "kind": string(j.Kind), "state": string(j.State)})
	return nil
}

// markPulled compares the images a pull reports with the applied ones: a
// different image ID is a newer image waiting for a deploy; the applied
// one again clears the mark.
func markPulled(st *domain.Stack, pulled []protocol.AppliedImage, now time.Time) {
	by := map[string]protocol.AppliedImage{}
	for _, i := range pulled {
		by[i.Service] = i
	}
	for k, cur := range st.Images {
		p, ok := by[cur.Service]
		if !ok || p.ImageID == "" || cur.Build {
			continue
		}
		switch {
		case p.ImageID == cur.ImageID:
			cur.PulledImageID, cur.PulledDigest, cur.PulledAt = "", "", nil
		case p.ImageID != cur.PulledImageID:
			t := now
			cur.PulledImageID, cur.PulledDigest, cur.PulledAt = p.ImageID, p.Digest, &t
		}
		st.Images[k] = cur
	}
}
