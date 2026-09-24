package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// Server-sent event streams (#4, #23). The wire contract is in
// docs/api/streams.md; every SSE route writes through SSEWriter so framing,
// headers and heartbeats are identical across streams.

// SSEContentType is the media type of event streams.
const SSEContentType = "text/event-stream"

// SSEWriter writes one server-sent event stream.
type SSEWriter struct {
	w io.Writer
}

// StartSSE sets the stream headers (no-store, no proxy buffering), the 200
// status and returns a writer. Call it from a huma.StreamResponse body.
func StartSSE(hctx huma.Context) *SSEWriter {
	hctx.SetHeader("Content-Type", SSEContentType)
	hctx.SetHeader("Cache-Control", "no-store")
	hctx.SetHeader("X-Accel-Buffering", "no")
	hctx.SetStatus(http.StatusOK)
	return &SSEWriter{w: hctx.BodyWriter()}
}

// errSSEField rejects ids and event names that would break framing.
var errSSEField = errors.New("api: SSE id/event must not contain line breaks")

// Event writes one event: optional id (the resume cursor clients send back
// as Last-Event-ID), event name and a single-line JSON data payload.
func (s *SSEWriter) Event(event, id string, v any) error {
	if strings.ContainsAny(event+id, "\r\n") {
		return errSSEField
	}
	b, err := json.Marshal(v) // compact JSON never contains raw newlines
	if err != nil {
		return err
	}
	var sb strings.Builder
	if id != "" {
		sb.WriteString("id: " + id + "\n")
	}
	sb.WriteString("event: " + event + "\ndata: ")
	sb.Write(b)
	sb.WriteString("\n\n")
	_, err = io.WriteString(s.w, sb.String())
	return err
}

// Heartbeat writes the keep-alive comment.
func (s *SSEWriter) Heartbeat() error {
	_, err := io.WriteString(s.w, ": heartbeat\n\n")
	return err
}

// Retry tells EventSource clients how long to wait before reconnecting.
func (s *SSEWriter) Retry(ms int) error {
	_, err := io.WriteString(s.w, "retry: "+strconv.Itoa(ms)+"\n\n")
	return err
}

// Flush pushes buffered bytes to the client.
func (s *SSEWriter) Flush() {
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}
