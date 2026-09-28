package jobspec

import (
	"errors"
	"slices"
	"testing"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// TestRetryableKinds pins the allowlist: every kind added here must be safe
// to run again from its stored input (see Spec.Retryable) and be listed in
// docs/internal/architecture/job-engine.md.
func TestRetryableKinds(t *testing.T) {
	var got []domain.JobKind
	for _, s := range Catalog() {
		if s.Retryable {
			got = append(got, s.Kind)
		}
	}
	want := []domain.JobKind{ImagePull, StackDeploy, StackPull, UpdateCheck}
	if !slices.Equal(got, want) {
		t.Fatalf("retryable kinds = %v, want %v", got, want)
	}
}

func TestRetryRefusal(t *testing.T) {
	in := []byte(`{"reference":"nginx:1"}`)
	for name, c := range map[string]struct {
		job    domain.Job
		reason string
	}{
		"failed":      {domain.Job{Kind: ImagePull, State: domain.JobFailed, Input: in}, ""},
		"partial":     {domain.Job{Kind: ImagePull, State: domain.JobPartial, Input: in}, ""},
		"interrupted": {domain.Job{Kind: StackDeploy, State: domain.JobInterrupted, Input: in}, ""},
		"cancelled":   {domain.Job{Kind: UpdateCheck, State: domain.JobCancelled, Input: in}, ""},
		"queued":      {domain.Job{Kind: ImagePull, State: domain.JobQueued, Input: in}, domain.RetryRefusedActive},
		"running":     {domain.Job{Kind: ImagePull, State: domain.JobRunning, Input: in}, domain.RetryRefusedActive},
		"succeeded":   {domain.Job{Kind: ImagePull, State: domain.JobSucceeded, Input: in}, domain.RetryRefusedSucceeded},
		"prune":       {domain.Job{Kind: PruneRun, State: domain.JobFailed, Input: in}, domain.RetryRefusedKind},
		"update run":  {domain.Job{Kind: UpdateRun, State: domain.JobFailed, Input: in}, domain.RetryRefusedKind},
		"backup":      {domain.Job{Kind: BackupRun, State: domain.JobFailed, Input: in}, domain.RetryRefusedKind},
		"build":       {domain.Job{Kind: ImageBuild, State: domain.JobFailed, Input: in}, domain.RetryRefusedKind},
		"unknown":     {domain.Job{Kind: "nope.run", State: domain.JobFailed, Input: in}, domain.RetryRefusedKind},
		"empty input": {domain.Job{Kind: ImagePull, State: domain.JobFailed, Input: []byte("{}")}, domain.RetryRefusedNoInput},
		"no input":    {domain.Job{Kind: ImagePull, State: domain.JobFailed}, domain.RetryRefusedNoInput},
		"bad input":   {domain.Job{Kind: ImagePull, State: domain.JobFailed, Input: []byte("[1]")}, domain.RetryRefusedNoInput},
	} {
		err := RetryRefusal(c.job)
		if c.reason == "" {
			if err != nil {
				t.Errorf("%s: refused: %v", name, err)
			}
			continue
		}
		var nr *domain.JobNotRetryableError
		if !errors.Is(err, domain.ErrJobNotRetryable) || !errors.As(err, &nr) || nr.Reason != c.reason || nr.Message == "" {
			t.Errorf("%s: got %v, want reason %s", name, err, c.reason)
		}
	}
}
