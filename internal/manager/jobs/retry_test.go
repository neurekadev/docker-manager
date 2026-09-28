package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/jobspec"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/jobs"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
)

// failedPull runs an image.pull of ref for policy pol to a failed state
// (in environment e1; every other command dispatched meanwhile fails too).
func (h *harness) failedPull(pol, ref string) domain.Job {
	h.t.Helper()
	h.disp.Connect("e1")
	j := h.enqueue(jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: "e1", PolicyID: pol, Targets: []domain.JobTarget{image(ref)},
		Input: map[string]any{"reference": ref}})
	h.dispatch()
	for _, cmd := range h.commands("e1") {
		h.ack("e1", cmd, protocol.AckPayload{Accepted: true})
		h.result("e1", cmd, protocol.ResultPayload{Outcome: "failed", Message: "rate limited"})
	}
	return h.wantState(j.ID, domain.JobFailed)
}

func TestRetryQueuesALinkedCopy(t *testing.T) {
	h := newHarness(t)
	orig := h.failedPull("pol-1", "nginx:1")
	nj, created, err := h.eng.Retry(h.ctx, user("bob"), orig.ID, "k-1")
	if err != nil || !created {
		t.Fatalf("retry: %v created=%v", err, created)
	}
	if nj.ID == orig.ID || nj.RetryOf != orig.ID || nj.Kind != orig.Kind || nj.EnvironmentID != "e1" || nj.PolicyID != "pol-1" ||
		!slices.Equal(nj.Targets, orig.Targets) || string(nj.Input) != string(orig.Input) {
		t.Fatalf("retry %+v of %+v", nj, orig)
	}
	if nj.State != domain.JobQueued || nj.InitiatorUserID != "bob" || nj.Origin != domain.OriginManual || nj.Attempt != 1 {
		t.Fatalf("retry is not a fresh job of the caller: %+v", nj)
	}
	if got := h.job(nj.ID); got.RetryOf != orig.ID {
		t.Fatalf("stored retryOf = %q", got.RetryOf)
	}
	if h.job(orig.ID).State != domain.JobFailed {
		t.Fatal("the original job changed")
	}
	// The same key returns the same retry; another job under it is a conflict.
	again, created, err := h.eng.Retry(h.ctx, user("bob"), orig.ID, "k-1")
	if err != nil || created || again.ID != nj.ID {
		t.Fatalf("repeated key: %v created=%v id=%s", err, created, again.ID)
	}
	// The policy filter and the count see the run and its retry.
	f := domain.JobFilter{PolicyID: "pol-1"}
	list, err := h.eng.List(h.ctx, f)
	if err != nil || len(list) != 2 || list[0].ID != nj.ID {
		t.Fatalf("policy filter: %d jobs, %v", len(list), err)
	}
	if n, err := h.eng.Count(h.ctx, f); err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}
	if n, err := h.eng.Count(h.ctx, domain.JobFilter{PolicyID: "pol-2"}); err != nil || n != 0 {
		t.Fatalf("count of another policy = %d, %v", n, err)
	}
	if n, err := h.eng.Count(h.ctx, domain.JobFilter{PolicyID: "pol-1", States: []domain.JobState{domain.JobFailed}}); err != nil || n != 1 {
		t.Fatalf("count of failed runs = %d, %v", n, err)
	}
	other := h.failedPull("pol-1", "redis:7")
	if _, _, err := h.eng.Retry(h.ctx, user("bob"), other.ID, "k-1"); !errors.Is(err, domain.ErrJobIdempotencyConflict) {
		t.Fatalf("key reused for another job's retry: %v", err)
	}
}

func TestRetryRefusals(t *testing.T) {
	h := newHarness(t)
	reason := func(err error) string {
		var nr *domain.JobNotRetryableError
		if !errors.Is(err, domain.ErrJobNotRetryable) || !errors.As(err, &nr) {
			t.Fatalf("want a not-retryable error, got %v", err)
		}
		return nr.Reason
	}
	// Still waiting.
	queued := h.enqueue(jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: "e2", Targets: []domain.JobTarget{image("a:1")},
		Input: map[string]any{"reference": "a:1"}})
	if _, _, err := h.eng.Retry(h.ctx, user("alice"), queued.ID, ""); reason(err) != domain.RetryRefusedActive {
		t.Fatalf("queued: %v", err)
	}
	// Succeeded.
	h.disp.Connect("e3")
	ok := h.enqueue(jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: "e3", Targets: []domain.JobTarget{image("b:1")},
		Input: map[string]any{"reference": "b:1"}})
	h.dispatch()
	h.completeAll("e3")
	h.wantState(ok.ID, domain.JobSucceeded)
	if _, _, err := h.eng.Retry(h.ctx, user("alice"), ok.ID, ""); reason(err) != domain.RetryRefusedSucceeded {
		t.Fatalf("succeeded: %v", err)
	}
	// A kind that is not retryable (cancelled while queued).
	stop := h.enqueue(jobs.Request{Kind: jobspec.StackStop, EnvironmentID: "e2", Targets: []domain.JobTarget{stack("web")},
		Input: map[string]any{"stackId": "web"}})
	if _, err := h.eng.Cancel(h.ctx, stop.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.eng.Retry(h.ctx, user("alice"), stop.ID, ""); reason(err) != domain.RetryRefusedKind {
		t.Fatalf("stack.stop: %v", err)
	}
	// No input kept.
	bare := h.enqueue(jobs.Request{Kind: jobspec.ImagePull, EnvironmentID: "e2", Targets: []domain.JobTarget{image("c:1")}})
	if _, err := h.eng.Cancel(h.ctx, bare.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.eng.Retry(h.ctx, user("alice"), bare.ID, ""); reason(err) != domain.RetryRefusedNoInput {
		t.Fatalf("no input: %v", err)
	}
	// Unknown job.
	if _, _, err := h.eng.Retry(h.ctx, user("alice"), "nope", ""); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
}

func TestRetryNeedsTheKindsCapabilityAndRunsTheRetrier(t *testing.T) {
	h := newHarness(t)
	var inputs, queued []string
	h.eng.OnRetry(jobspec.ImagePull, jobs.Retrier{
		Input: func(_ context.Context, orig domain.Job) (any, error) {
			inputs = append(inputs, orig.ID)
			if orig.PolicyID == "gone" {
				return nil, &domain.JobNotRetryableError{Reason: domain.RetryRefusedUnavailable, Message: "gone"}
			}
			return map[string]any{"reference": "nginx:2"}, nil
		},
		Queued: func(_ context.Context, j domain.Job) { queued = append(queued, j.ID) },
	})
	orig := h.failedPull("", "nginx:1")
	// Without image.pull on the target the retry is forbidden, before the
	// Retrier runs (it may ask the agent).
	h.az.revoke("mallory", "image.pull")
	if _, _, err := h.eng.Retry(h.ctx, user("mallory"), orig.ID, ""); !errors.Is(err, domain.ErrJobForbidden) {
		t.Fatalf("mallory: %v", err)
	}
	if len(inputs) != 0 {
		t.Fatal("the Retrier ran for an unauthorized caller")
	}
	nj, _, err := h.eng.Retry(h.ctx, user("alice"), orig.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]string
	if err := json.Unmarshal(nj.Input, &in); err != nil || in["reference"] != "nginx:2" {
		t.Fatalf("refreshed input %s (%v)", nj.Input, err)
	}
	if !slices.Equal(queued, []string{nj.ID}) {
		t.Fatalf("Queued ran for %v", queued)
	}
	gone := h.failedPull("gone", "redis:7")
	var nr *domain.JobNotRetryableError
	if _, _, err := h.eng.Retry(h.ctx, user("alice"), gone.ID, ""); !errors.As(err, &nr) || nr.Reason != domain.RetryRefusedUnavailable {
		t.Fatalf("gone: %v", err)
	}
	if len(queued) != 1 {
		t.Fatal("Queued ran for a refused retry")
	}
}
