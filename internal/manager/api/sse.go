package api

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/server/sse"
)

// Server-sent event streams (#4, #23). The wire contract is in
// docs/api/streams.md; every SSE route writes through SSEWriter so framing,
// headers and heartbeats are identical across streams. SSEWriter is the
// Huma-facing adapter of the manager's one SSE implementation,
// internal/manager/server/sse (#27: X-Accel-Buffering: no, no-store, a
// flush after every event, heartbeats below proxy idle timeouts).

// SSEContentType is the media type of event streams.
const SSEContentType = sse.ContentType

// SSEWriter writes one server-sent event stream.
type SSEWriter struct {
	w io.Writer
	s *sse.Writer
}

// StartSSE sets the stream headers (no-store, no proxy buffering), the 200
// status and returns a writer. Call it from a huma.StreamResponse body.
func StartSSE(hctx huma.Context) *SSEWriter {
	sse.SetHeaders(hctx.SetHeader)
	hctx.SetStatus(http.StatusOK)
	return &SSEWriter{w: hctx.BodyWriter()}
}

func (s *SSEWriter) stream() *sse.Writer {
	if s.s == nil {
		s.s = sse.NewWriter(s.w)
	}
	return s.s
}

// Event writes one event: optional id (the resume cursor clients send back
// as Last-Event-ID), event name and a single-line JSON data payload, and
// flushes it. Line breaks in event or id are rejected.
func (s *SSEWriter) Event(event, id string, v any) error {
	return s.stream().JSON(event, id, v)
}

// Heartbeat writes the keep-alive comment and flushes.
func (s *SSEWriter) Heartbeat() error { return s.stream().Heartbeat() }

// Retry tells EventSource clients how long to wait before reconnecting.
func (s *SSEWriter) Retry(ms int) error {
	return s.stream().Retry(time.Duration(ms) * time.Millisecond)
}

// CloseEvent is the data of the final `close` event.
type CloseEvent struct {
	Reason string `json:"reason" enum:"permissions_changed,session_expired,max_age,shutdown"`
}

// CloseIfRevoked writes `event: close` with the reason when ctx (the
// request context) was ended by a permission change or the end of the
// session (authz.CloseReason), so the client drops cached data and
// reconnects or re-authenticates. Call it when the stream loop exits.
func (s *SSEWriter) CloseIfRevoked(ctx context.Context) {
	if reason := authz.CloseReason(ctx); reason != "" {
		_ = s.Event("close", "", CloseEvent{Reason: reason})
	}
}

// Flush pushes buffered bytes to the client.
func (s *SSEWriter) Flush() { _ = s.stream().Flush() }
