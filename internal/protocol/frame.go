// Package protocol defines the private manager<->agent session protocol
// (dockyard.agent/v1): a JSON frame envelope exchanged over one WebSocket
// that the agent dials out to /agent/v1/session.
//
// This package is intentionally small. Enrollment, the handshake and
// capability schema arrive with #3; command/job semantics (attempt, fencing
// tokens, deadlines) with #26. Keep it free of manager/agent internals so
// both binaries share it. The protocol is versioned independently of /api/v1.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Version is the protocol identifier negotiated in the hello frame and used
// as the WebSocket subprotocol.
const Version = "dockyard.agent/v1"

// MaxFrameSize bounds an encoded frame (bytes). Larger payloads (logs, file
// transfers) must be split into stream_data frames.
const MaxFrameSize = 1 << 20 // 1 MiB

// Type is the frame type.
type Type string

// Frame types.
const (
	TypeHello          Type = "hello"
	TypeHeartbeat      Type = "heartbeat"
	TypeCapabilities   Type = "capabilities"
	TypeCommand        Type = "command"
	TypeAck            Type = "ack"
	TypeProgress       Type = "progress"
	TypeResult         Type = "result"
	TypeEvent          Type = "event"
	TypeFSInvalidation Type = "fs_invalidation"
	TypeRescan         Type = "rescan"
	TypeStreamOpen     Type = "stream_open"
	TypeStreamData     Type = "stream_data"
	TypeStreamClose    Type = "stream_close"
	TypeCancel         Type = "cancel"
	TypeError          Type = "error"
)

var knownTypes = map[Type]bool{
	TypeHello: true, TypeHeartbeat: true, TypeCapabilities: true, TypeCommand: true,
	TypeAck: true, TypeProgress: true, TypeResult: true, TypeEvent: true,
	TypeFSInvalidation: true, TypeRescan: true, TypeStreamOpen: true, TypeStreamData: true,
	TypeStreamClose: true, TypeCancel: true, TypeError: true,
}

// Types returns every known frame type.
func Types() []Type {
	return []Type{TypeHello, TypeHeartbeat, TypeCapabilities, TypeCommand, TypeAck, TypeProgress,
		TypeResult, TypeEvent, TypeFSInvalidation, TypeRescan, TypeStreamOpen, TypeStreamData,
		TypeStreamClose, TypeCancel, TypeError}
}

// Frame is the envelope of every message.
type Frame struct {
	// Type selects how Payload is interpreted.
	Type Type `json:"type"`
	// ID uniquely identifies this frame within the session (sender-generated).
	ID string `json:"id"`
	// CorrelationID refers to the frame this one answers (ack/progress/result/
	// cancel/error refer to a command; stream frames to their stream_open).
	CorrelationID string `json:"correlationId,omitempty"`
	// JobID is the durable job this frame belongs to (#26).
	JobID string `json:"jobId,omitempty"`
	// Attempt is the job attempt number, starting at 1.
	Attempt uint32 `json:"attempt,omitempty"`
	// FencingToken orders command authority: agents reject commands whose
	// token is lower than one already seen for the same job/lock (#26).
	FencingToken uint64 `json:"fencingToken,omitempty"`
	// Deadline after which the receiver must not start (or must abort) work.
	Deadline *time.Time `json:"deadline,omitempty"`
	// Payload is the type-specific body; a JSON object when present.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Validation errors.
var (
	ErrFrameTooLarge = fmt.Errorf("protocol: frame exceeds %d bytes", MaxFrameSize)
	ErrInvalidFrame  = errors.New("protocol: invalid frame")
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Validate checks envelope invariants. Payload schemas are validated by the
// handlers of each type.
func (f *Frame) Validate() error {
	if !knownTypes[f.Type] {
		return fmt.Errorf("%w: unknown type %q", ErrInvalidFrame, f.Type)
	}
	if !idRE.MatchString(f.ID) {
		return fmt.Errorf("%w: id must match %s", ErrInvalidFrame, idRE)
	}
	if f.CorrelationID != "" && !idRE.MatchString(f.CorrelationID) {
		return fmt.Errorf("%w: correlationId must match %s", ErrInvalidFrame, idRE)
	}
	if f.JobID != "" && !idRE.MatchString(f.JobID) {
		return fmt.Errorf("%w: jobId must match %s", ErrInvalidFrame, idRE)
	}
	if f.Deadline != nil && f.Deadline.IsZero() {
		return fmt.Errorf("%w: deadline must not be zero", ErrInvalidFrame)
	}
	if len(f.Payload) > 0 {
		trimmed := bytes.TrimLeft(f.Payload, " \t\r\n")
		if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(f.Payload) {
			return fmt.Errorf("%w: payload must be a JSON object", ErrInvalidFrame)
		}
	}
	switch f.Type {
	case TypeCommand:
		if f.JobID == "" || f.Attempt == 0 || f.FencingToken == 0 || f.Deadline == nil {
			return fmt.Errorf("%w: command requires jobId, attempt >= 1, fencingToken >= 1 and deadline", ErrInvalidFrame)
		}
	case TypeAck, TypeProgress, TypeResult, TypeCancel, TypeStreamData, TypeStreamClose:
		if f.CorrelationID == "" {
			return fmt.Errorf("%w: %s requires correlationId", ErrInvalidFrame, f.Type)
		}
	}
	return nil
}

// Encode validates f and returns its JSON encoding.
func Encode(f *Frame) ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("protocol: encode: %w", err)
	}
	if len(b) > MaxFrameSize {
		return nil, ErrFrameTooLarge
	}
	return b, nil
}

// Decode parses and validates one frame. Unknown fields and trailing data
// are rejected so both sides agree on the exact envelope.
func Decode(b []byte) (*Frame, error) {
	if len(b) > MaxFrameSize {
		return nil, ErrFrameTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f Frame
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	if rest := b[dec.InputOffset():]; len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: trailing data after frame", ErrInvalidFrame)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}
