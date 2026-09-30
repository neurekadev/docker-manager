package stacks

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/jobspec"
	"github.com/neurekadev/docker-manager/internal/manager/jobs"
	"github.com/neurekadev/docker-manager/internal/manager/store"
	"github.com/neurekadev/docker-manager/internal/protocol"
)

// Retries of deploys and pulls (POST /jobs/{id}/retries, #26): the new job
// keeps the original's options (services, pull and build choices,
// timeouts) but acts on the stack as it is now — its current directory,
// project name and Compose files and the registry connections that apply
// now — and becomes the stack's last job, like a deploy started from the
// stack page.

// retryKinds are the stack kinds the job engine may retry (jobspec
// marks them Retryable).
var retryKinds = []domain.JobKind{jobspec.StackDeploy, jobspec.StackPull}

// registerRetries installs the stack Retrier when the job engine supports
// retries (test fakes may not).
func (s *Service) registerRetries() {
	r, ok := s.opts.Jobs.(interface {
		OnRetry(kind domain.JobKind, r jobs.Retrier)
	})
	if !ok {
		return
	}
	for _, k := range retryKinds {
		r.OnRetry(k, jobs.Retrier{Input: s.retryInput, Queued: s.retryQueued})
	}
}

func notRetryable(msg string) error {
	return &domain.JobNotRetryableError{Reason: domain.RetryRefusedUnavailable, Message: msg}
}

// retryInput rebuilds a deploy's or pull's input from the stack's current
// record.
func (s *Service) retryInput(ctx context.Context, orig domain.Job) (any, error) {
	st, err := store.GetStack(ctx, s.db, stackTarget(orig))
	if errors.Is(err, domain.ErrStackNotFound) {
		return nil, notRetryable("the stack no longer exists")
	}
	if err != nil {
		return nil, err
	}
	if st.EnvironmentID != orig.EnvironmentID {
		return nil, notRetryable("the stack moved to another environment; start the action from the stack page")
	}
	var in protocol.StackJobInput
	if err := json.Unmarshal(orig.Input, &in); err != nil {
		return nil, &domain.JobNotRetryableError{Reason: domain.RetryRefusedNoInput,
			Message: "the job's input cannot be read; start the action from the stack page"}
	}
	regs, err := s.registryConnections(ctx, st)
	if err != nil {
		return nil, err
	}
	in.StackID, in.Stack, in.RegistryConnections = st.ID, Ref(st), regs
	return in, nil
}

// retryQueued makes the retry the stack's last job.
func (s *Service) retryQueued(ctx context.Context, j domain.Job) {
	var st domain.Stack
	err := s.tx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cur, err := store.GetStack(ctx, tx, stackTarget(j))
		if err != nil {
			return err
		}
		cur.LastJobID, cur.LastJobKind, cur.UpdatedAt = j.ID, j.Kind, s.now()
		st = cur
		return store.UpdateStack(ctx, tx, &cur)
	})
	if err != nil {
		s.log.Warn("record the retried stack job", "job_id", j.ID, "error", err)
		return
	}
	s.publish(EventUpdated, st, map[string]string{"jobId": j.ID, "kind": string(j.Kind)})
}
