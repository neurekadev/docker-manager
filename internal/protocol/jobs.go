package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Job command semantics (#26).
//
//	manager                                   agent
//	command  {jobId, attempt, fencingToken,   ──▶ fencing check, journal (fsync), then
//	          deadline, CommandPayload}             ack {AckPayload} (correlationId = command id)
//	                                          ◀──  progress {ProgressPayload}
//	                                          ◀──  result {ResultPayload} (correlationId = command id)
//	ack {forget:[jobId]} (correlationId =     ──▶ agent drops the journal entry
//	     result id)
//	cancel {jobId, attempt, fencingToken}     ──▶ honored at the kind's next safe point
//
// On every (re)connect the agent sends job_report {JobReportPayload} with its
// fencing high-water mark and every journaled job; the manager reconciles
// (never re-runs) and answers with an ack whose Forget lists the entries the
// agent may drop.
//
// Fencing: the manager allocates tokens from a persisted per-environment
// counter and sends commands of one environment in token order. The agent
// persists the highest token it accepted and rejects any command whose token
// is not above it (AckStaleToken), except an exact duplicate of the command
// it already journaled (AckDuplicate: acknowledged again, never re-run). A
// command replayed after a reconnect or from a superseded dispatch is
// therefore rejected.

// Ack codes.
const (
	// AckDuplicate: the command was already accepted; it is not run again.
	AckDuplicate = "duplicate"
	// AckStaleToken: the token is not above the agent's high-water mark.
	AckStaleToken = "stale_fencing_token"
	// AckUnsupportedKind: this agent has no executor for the kind.
	AckUnsupportedKind = "unsupported_kind"
	// AckInvalid: the command payload is malformed.
	AckInvalid = "invalid_command"
	// AckBusy: an earlier attempt of the same job is still running here.
	AckBusy = "attempt_in_progress"
	// AckDeadlineExceeded: the command's deadline passed before it arrived.
	AckDeadlineExceeded = "deadline_exceeded"
	// AckJournalFailed: the agent could not persist the command.
	AckJournalFailed = "journal_failed"
)

// Job report entry statuses.
const (
	ReportRunning  = "running"
	ReportFinished = "finished"
)

// JobRef identifies one dispatch (attempt) of a job.
type JobRef struct {
	JobID        string
	Attempt      uint32
	FencingToken uint64
}

// Ref returns the job reference carried by a frame's envelope.
func (f *Frame) Ref() JobRef {
	return JobRef{JobID: f.JobID, Attempt: f.Attempt, FencingToken: f.FencingToken}
}

// CommandFrameID is the deterministic frame ID of a job command, so cancel,
// ack and result frames can correlate with it without extra state.
func CommandFrameID(jobID string, attempt uint32) string {
	return "cmd." + jobID + "." + strconv.FormatUint(uint64(attempt), 10)
}

// CommandPayload is the body of a command frame.
type CommandPayload struct {
	Kind  string          `json:"kind"`
	Input json.RawMessage `json:"input,omitempty"`
	// CompletedSteps lists steps completed by earlier attempts; a resumed
	// attempt skips them.
	CompletedSteps []string `json:"completedSteps,omitempty"`
}

// AckPayload is the body of an ack frame.
type AckPayload struct {
	Accepted bool   `json:"accepted"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	// HighWater is the agent's highest accepted fencing token (sent with
	// AckStaleToken so the manager can move its counter past it).
	HighWater uint64 `json:"highWater,omitempty"`
	// Forget lists job IDs whose finished journal entries the agent may drop
	// (manager acks of result and job_report frames).
	Forget []string `json:"forget,omitempty"`
}

// ItemPayload is a per-item result.
type ItemPayload struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// ProgressPayload is the body of a progress frame.
type ProgressPayload struct {
	Step string `json:"step,omitempty"`
	// Percent is 0..100, or -1 when unknown.
	Percent int          `json:"percent"`
	Message string       `json:"message,omitempty"`
	Item    *ItemPayload `json:"item,omitempty"`
}

// CompensationPayload reports a compensating action's outcome.
type CompensationPayload struct {
	Name  string `json:"name"`
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

// ResultPayload is the body of a result frame and of finished job_report
// entries.
type ResultPayload struct {
	// Outcome is succeeded, failed, partial, cancelled or interrupted.
	Outcome    string        `json:"outcome"`
	ErrorClass string        `json:"errorClass,omitempty"`
	Message    string        `json:"message,omitempty"`
	Recovery   string        `json:"recovery,omitempty"`
	Items      []ItemPayload `json:"items,omitempty"`
	// CompletedSteps are the steps known to have completed.
	CompletedSteps []string `json:"completedSteps,omitempty"`
	// InterruptedStep is the step in flight when the executor died.
	InterruptedStep string `json:"interruptedStep,omitempty"`
	// Resumable: an interrupted attempt may be resumed as a new attempt
	// (the in-flight step, if any, is idempotent and no compensation ran).
	Resumable     bool                  `json:"resumable,omitempty"`
	Compensations []CompensationPayload `json:"compensations,omitempty"`
}

// JobReportEntry is one journaled job in a job_report.
type JobReportEntry struct {
	JobID        string `json:"jobId"`
	Attempt      uint32 `json:"attempt"`
	FencingToken uint64 `json:"fencingToken"`
	Kind         string `json:"kind"`
	// Status is running (still executing in this agent process) or finished
	// (Result holds the outcome).
	Status         string         `json:"status"`
	CurrentStep    string         `json:"currentStep,omitempty"`
	CompletedSteps []string       `json:"completedSteps,omitempty"`
	Result         *ResultPayload `json:"result,omitempty"`
}

// JobReportPayload is the body of a job_report frame.
type JobReportPayload struct {
	HighWater uint64           `json:"highWater"`
	Jobs      []JobReportEntry `json:"jobs"`
}

// NewFrame builds a frame with a JSON payload and validates it.
func NewFrame(typ Type, id, correlationID string, ref JobRef, payload any) (*Frame, error) {
	f := &Frame{Type: typ, ID: id, CorrelationID: correlationID, JobID: ref.JobID, Attempt: ref.Attempt, FencingToken: ref.FencingToken}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("protocol: encode payload: %w", err)
		}
		f.Payload = b
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return f, nil
}

// NewCommandFrame builds a job command frame.
func NewCommandFrame(ref JobRef, deadline time.Time, p CommandPayload) (*Frame, error) {
	d := deadline.UTC()
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("protocol: encode payload: %w", err)
	}
	f := &Frame{Type: TypeCommand, ID: CommandFrameID(ref.JobID, ref.Attempt), JobID: ref.JobID,
		Attempt: ref.Attempt, FencingToken: ref.FencingToken, Deadline: &d, Payload: b}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return f, nil
}

// DecodePayload strictly decodes a frame's payload into T.
func DecodePayload[T any](f *Frame) (T, error) {
	var v T
	if len(f.Payload) == 0 {
		return v, fmt.Errorf("%w: %s frame has no payload", ErrInvalidFrame, f.Type)
	}
	dec := json.NewDecoder(bytes.NewReader(f.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("%w: %s payload: %v", ErrInvalidFrame, f.Type, err)
	}
	return v, nil
}
