package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Retries (POST /jobs/{id}/retries): a finished, unsuccessful job of a
// kind marked jobspec.Spec.Retryable is run again as a NEW job with the
// same kind, environment, policy, targets and input, linked to the
// original by domain.Job.RetryOf. The new job is an ordinary request of
// the caller: Enqueue authorizes the kind's capabilities on every target,
// refuses archived environments and records job.queued. The original job
// is never changed.

// Retrier lets the feature owning a retryable kind take part in its
// retries. Both functions are optional.
type Retrier struct {
	// Input rebuilds the input of a retry of orig from the feature's
	// current state (a stack's current directory and Compose files, the
	// registry connections that apply now); nil retries with the stored
	// input. Return a *domain.JobNotRetryableError (reason
	// domain.RetryRefusedUnavailable) when what orig acted on is gone or
	// can no longer be acted on this way.
	Input func(ctx context.Context, orig domain.Job) (input any, err error)
	// Queued runs after a retry was queued (not for a repeated
	// idempotency key), e.g. to point the feature's record at the new
	// job. It cannot fail the retry; log what goes wrong.
	Queued func(ctx context.Context, j domain.Job)
}

// OnRetry registers the Retrier of a retryable kind (call before Run).
// Kinds without one are retried with their stored input.
func (e *Engine) OnRetry(kind domain.JobKind, r Retrier) {
	e.hooksMu.Lock()
	defer e.hooksMu.Unlock()
	e.retriers[kind] = r
}

func (e *Engine) retrier(kind domain.JobKind) Retrier {
	e.hooksMu.RLock()
	defer e.hooksMu.RUnlock()
	return e.retriers[kind]
}

// Retry enqueues a new job re-running job id for principal p. created is
// false when a repeated idempotency key returned the retry it started.
// Errors: domain.ErrJobNotFound, domain.ErrJobNotRetryable (a
// *domain.JobNotRetryableError), and every Enqueue error
// (domain.ErrJobForbidden when p lacks the kind's capabilities on a
// target, domain.ErrEnvironmentArchived, domain.ErrJobIdempotencyConflict, ...).
func (e *Engine) Retry(ctx context.Context, p authz.Principal, id, idempotencyKey string) (job domain.Job, created bool, err error) {
	orig, err := store.GetJob(ctx, e.db, id)
	if err != nil {
		return domain.Job{}, false, err
	}
	if err := jobspec.RetryRefusal(orig); err != nil {
		return domain.Job{}, false, err
	}
	// Authorize before a Retrier touches anything (it may ask the agent);
	// Enqueue checks again with the refreshed input.
	spec, _ := jobspec.Lookup(orig.Kind)
	caps, err := spec.Capabilities(orig.Targets, orig.Input)
	if err != nil {
		return domain.Job{}, false, err
	}
	if d := e.authorize(ctx, p, caps, orig.EnvironmentID, spec.AuthorizationTargets(orig.Targets)); !d.Allowed {
		return domain.Job{}, false, fmt.Errorf("%w: %s", domain.ErrJobForbidden, d.Reason)
	}
	r := e.retrier(orig.Kind)
	input := any(json.RawMessage(orig.Input))
	if r.Input != nil {
		if input, err = r.Input(ctx, orig); err != nil {
			return domain.Job{}, false, err
		}
	}
	job, created, err = e.Enqueue(ctx, Request{Kind: orig.Kind, Principal: p, PolicyID: orig.PolicyID, EnvironmentID: orig.EnvironmentID,
		Targets: slices.Clone(orig.Targets), Input: input, IdempotencyKey: idempotencyKey, retryOf: orig.ID})
	if err == nil && created && r.Queued != nil {
		r.Queued(ctx, job)
	}
	return job, created, err
}
