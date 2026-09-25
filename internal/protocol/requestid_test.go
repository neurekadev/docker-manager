package protocol

import (
	"errors"
	"testing"
	"time"
)

// TestFrameRequestID (#34): requestId is allowed on command, request and
// stream_open only, and must be a well-formed ID.
func TestFrameRequestID(t *testing.T) {
	d := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	ok := []*Frame{
		{Type: TypeRequest, ID: "q.1", Deadline: &d, Payload: []byte(`{"name":"engine.info"}`), RequestID: "0f1e2d3c4b5a69788796a5b4c3d2e1f0"},
		{Type: TypeCommand, ID: "cmd.j.1", JobID: "j", Attempt: 1, FencingToken: 1, Deadline: &d, RequestID: "req-1"},
		{Type: TypeStreamOpen, ID: "so.1", Payload: []byte(`{}`), RequestID: "req.2:x"},
	}
	for _, f := range ok {
		if err := f.Validate(); err != nil {
			t.Errorf("%s with requestId: %v", f.Type, err)
		}
	}
	bad := []*Frame{
		{Type: TypeRequest, ID: "q.1", Deadline: &d, Payload: []byte(`{"name":"engine.info"}`), RequestID: "bad id"},
		{Type: TypeHeartbeat, ID: "h.1", RequestID: "req-1"},
		{Type: TypeResponse, ID: "r.1", CorrelationID: "q.1", RequestID: "req-1"},
	}
	for _, f := range bad {
		if err := f.Validate(); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("%s %q accepted: %v", f.Type, f.RequestID, err)
		}
	}
	if RequestIDOrEmpty("a b") != "" || RequestIDOrEmpty("abc") != "abc" || RequestIDOrEmpty("") != "" {
		t.Error("RequestIDOrEmpty")
	}
}
