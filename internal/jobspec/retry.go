package jobspec

import (
	"bytes"
	"encoding/json"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// RetryRefusal reports why j cannot be retried (a *domain.JobNotRetryableError),
// or nil when it can, before authorization: the job must have finished
// without succeeding, its kind must be retryable and its input must have
// been kept.
func RetryRefusal(j domain.Job) error {
	spec, ok := Lookup(j.Kind)
	switch {
	case !j.State.Terminal():
		return &domain.JobNotRetryableError{Reason: domain.RetryRefusedActive,
			Message: "the job has not finished yet; wait for it or cancel it first"}
	case j.State == domain.JobSucceeded:
		return &domain.JobNotRetryableError{Reason: domain.RetryRefusedSucceeded, Message: "the job succeeded; there is nothing to retry"}
	case !ok || !spec.Retryable:
		return &domain.JobNotRetryableError{Reason: domain.RetryRefusedKind,
			Message: "jobs of this kind cannot be retried; start the action again from where it was started"}
	case !inputKept(j.Input):
		return &domain.JobNotRetryableError{Reason: domain.RetryRefusedNoInput,
			Message: "the job's input was not kept; start the action again from where it was started"}
	}
	return nil
}

// inputKept reports whether a job's stored input is a non-empty JSON
// object (every retryable kind needs input).
func inputKept(in []byte) bool {
	in = bytes.TrimSpace(in)
	if len(in) == 0 || bytes.Equal(in, []byte("{}")) {
		return false
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(in, &obj) == nil && len(obj) > 0
}
