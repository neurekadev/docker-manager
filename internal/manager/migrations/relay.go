package migrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/neurekadev/docker-manager/internal/protocol"
	"github.com/neurekadev/docker-manager/internal/streammux"
	"github.com/neurekadev/docker-manager/internal/transfer"
)

// DefaultRelayBuffer is the relay's copy buffer. With the stream windows
// (protocol.StreamWindow per stream) it bounds what one relayed part holds
// in manager memory: at most the source stream's window plus this buffer,
// whatever the part's size. Nothing is buffered on disk.
const DefaultRelayBuffer = 64 << 10

// RelayOptions tunes Relay.
type RelayOptions struct {
	// Limiter caps the rate (nil: unlimited).
	Limiter *transfer.Limiter
	// Progress receives the payload bytes relayed so far (may be nil).
	Progress func(bytes int64)
	// BufferSize of the copy buffer (default DefaultRelayBuffer).
	BufferSize int
}

// RelayResult is one part's checksums as each party saw them; Relay
// returns it only when they all agree.
type RelayResult struct {
	Source      protocol.MigrationPartResult
	Relayed     transfer.Summary
	Destination protocol.MigrationPartResult
	// StreamBytes counts the framed bytes that crossed the manager.
	StreamBytes int64
}

// ErrChecksumMismatch means the source, the relay and the destination
// disagree about a part's bytes.
var ErrChecksumMismatch = errors.New("migration: the checksums of the source, the manager and the destination differ")

// PartError is a relay failure on one side.
type PartError struct {
	// Side is "source", "destination" or "relay".
	Side string
	// Code is the protocol error code the side reported ("" unknown).
	Code string
	Err  error
}

func (e *PartError) Error() string {
	s := "migration " + e.Side
	if e.Code != "" {
		s += " (" + e.Code + ")"
	}
	return s + ": " + e.Err.Error()
}

func (e *PartError) Unwrap() error { return e.Err }

// SessionLost reports whether the failure is a lost agent session (the
// part can be retried once the agent is back).
func (e *PartError) SessionLost() bool {
	return errors.Is(e.Err, streammux.ErrSessionClosed)
}

func sideError(side string, err error) *PartError {
	pe := &PartError{Side: side, Err: err}
	var ce *streammux.CloseError
	if errors.As(err, &ce) && !ce.Local {
		pe.Code = ce.Code
	}
	switch {
	case errors.Is(err, transfer.ErrChecksum), errors.Is(err, streammux.ErrVerification):
		pe.Code = protocol.CodeDigestMismatch
	case errors.Is(err, transfer.ErrFormat):
		pe.Code = protocol.CodeInvalidFrame
	}
	return pe
}

// Relay copies one framed part from src (a migration.send stream on the
// source agent) to dst (a migration.receive stream on the destination
// agent). Backpressure is end to end: the manager reads from src only
// after dst accepted the previous bytes (stream credit), so the source
// slows down to the destination's pace and to the bandwidth cap. Every
// byte passes a transfer.Verifier: a corrupted chunk aborts both streams.
// After the source's final close, the destination's result is awaited and
// all three checksums are compared (ErrChecksumMismatch). On any failure
// both streams are aborted.
func Relay(ctx context.Context, src, dst *streammux.Stream, o RelayOptions) (RelayResult, error) {
	size := o.BufferSize
	if size <= 0 {
		size = DefaultRelayBuffer
	}
	abort := func(code, msg string) {
		src.Abort(protocol.CloseReasonError, code, msg)
		dst.Abort(protocol.CloseReasonError, code, msg)
	}
	var res RelayResult
	buf := make([]byte, size)
	v := transfer.NewVerifier()
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := v.Write(buf[:n]); err != nil {
				abort(protocol.CodeDigestMismatch, "the relayed data failed verification")
				return res, &PartError{Side: "relay", Code: protocol.CodeDigestMismatch, Err: err}
			}
			if err := o.Limiter.Wait(ctx, n); err != nil {
				abort(protocol.CodeCancelled, "the migration stopped")
				return res, &PartError{Side: "relay", Code: protocol.CodeCancelled, Err: err}
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				src.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "the destination failed")
				return res, sideError("destination", err)
			}
			res.StreamBytes += int64(n)
			if o.Progress != nil {
				o.Progress(v.Summary().Bytes)
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			dst.Abort(protocol.CloseReasonCancelled, protocol.CodeCancelled, "the source failed")
			return res, sideError("source", rerr)
		}
	}
	if err := v.Close(); err != nil {
		abort(protocol.CodeDigestMismatch, "the relayed data ended early")
		return res, &PartError{Side: "relay", Code: protocol.CodeInvalidFrame, Err: err}
	}
	res.Relayed = v.Summary()
	// The source sent everything (its eof close carries its result); end
	// our receiving side, then let the destination finish and answer.
	_ = src.CloseWrite()
	if rc := src.RemoteClose(); rc != nil && len(rc.Result) > 0 {
		if err := json.Unmarshal(rc.Result, &res.Source); err != nil {
			dst.Abort(protocol.CloseReasonError, protocol.CodeInvalidFrame, "malformed source result")
			return res, &PartError{Side: "source", Code: protocol.CodeInvalidFrame, Err: err}
		}
	}
	if err := dst.CloseWrite(); err != nil {
		return res, sideError("destination", err)
	}
	raw, err := dst.Result(ctx)
	if err != nil {
		return res, sideError("destination", err)
	}
	if err := json.Unmarshal(raw, &res.Destination); err != nil {
		return res, &PartError{Side: "destination", Code: protocol.CodeInvalidFrame, Err: err}
	}
	s, r, d := res.Source, res.Relayed, res.Destination
	if s.SHA256 != r.SHA256 || d.SHA256 != r.SHA256 || s.Bytes != r.Bytes || d.Bytes != r.Bytes || s.Chunks != r.Chunks || d.Chunks != r.Chunks {
		return res, &PartError{Side: "relay", Code: protocol.CodeDigestMismatch, Err: fmt.Errorf("%w: source %s (%d bytes), manager %s (%d bytes), destination %s (%d bytes)",
			ErrChecksumMismatch, short(s.SHA256), s.Bytes, short(r.SHA256), r.Bytes, short(d.SHA256), d.Bytes)}
	}
	return res, nil
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}
