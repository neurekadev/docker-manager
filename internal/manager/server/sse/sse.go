// Package sse is the one server-sent-events writer of the manager. Every
// SSE endpoint (job events #26, the live stream #23, logs) uses it so all
// streams behave the same behind reverse proxies (#27):
//
//   - Content-Type text/event-stream, Cache-Control no-store and
//     X-Accel-Buffering: no (nginx and compatible proxies stream instead of
//     buffering);
//   - every write is flushed immediately;
//   - callers send a ": heartbeat" comment every Heartbeat interval while
//     idle (default 15 s, DOCKYARD_STREAM_HEARTBEAT) so proxies with an
//     idle/read timeout (nginx proxy_read_timeout defaults to 60 s) never cut
//     a quiet stream, and clients notice dead connections.
//
// Typical loop:
//
//	sse.SetHeaders(w.Header().Set)          // or hctx.SetHeader in Huma
//	s := sse.NewWriter(w)
//	hb := clk.NewTicker(heartbeat); defer hb.Stop()
//	for {
//		select {
//		case ev := <-events:
//			if s.JSON(ev.Type, ev.ID, ev) != nil { return }
//		case <-hb.C():
//			if s.Heartbeat() != nil { return }
//		case <-ctx.Done():
//			return
//		}
//	}
package sse

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Heartbeat bounds.
const (
	// DefaultHeartbeat is the keep-alive interval of streams: well below
	// the common 60 s proxy idle/read timeouts.
	DefaultHeartbeat = 15 * time.Second
	// MinHeartbeat and MaxHeartbeat bound DOCKYARD_STREAM_HEARTBEAT. The
	// maximum stays below the 60 s defaults of nginx and most load balancers.
	MinHeartbeat = time.Second
	MaxHeartbeat = 55 * time.Second
)

// ContentType is the media type of every stream.
const ContentType = "text/event-stream"

// ErrInvalidField is returned when an event name or ID contains a line break
// (which would let data inject extra fields).
var ErrInvalidField = errors.New("sse: event name and id must not contain line breaks")

// SetHeaders sets the streaming response headers through set (e.g.
// w.Header().Set or huma.Context.SetHeader). Call before the first write.
func SetHeaders(set func(name, value string)) {
	set("Content-Type", ContentType)
	set("Cache-Control", "no-store")
	set("X-Accel-Buffering", "no")
}

// Writer writes events and flushes after each one. It is not safe for
// concurrent use; serialize writes in one goroutine.
type Writer struct {
	w     io.Writer
	flush func() error
}

// NewWriter wraps w. When w is an http.ResponseWriter (or unwraps to one)
// each event is flushed to the client; other writers are only written.
func NewWriter(w io.Writer) *Writer {
	s := &Writer{w: w, flush: func() error { return nil }}
	if rw, ok := w.(http.ResponseWriter); ok {
		rc := http.NewResponseController(rw)
		s.flush = func() error {
			err := rc.Flush()
			if errors.Is(err, http.ErrNotSupported) {
				return nil
			}
			return err
		}
	}
	return s
}

// Event is one server-sent event.
type Event struct {
	// ID becomes the client's Last-Event-ID (empty: none).
	ID string
	// Name is the event type (empty: "message").
	Name string
	// Data may contain newlines; each line becomes a data: field.
	Data string
	// Retry, when positive, advises the client's reconnect delay.
	Retry time.Duration
}

// Send writes e and flushes.
func (s *Writer) Send(e Event) error {
	if strings.ContainsAny(e.ID, "\r\n\x00") || strings.ContainsAny(e.Name, "\r\n") {
		return ErrInvalidField
	}
	var b strings.Builder
	if e.ID != "" {
		b.WriteString("id: " + e.ID + "\n")
	}
	if e.Name != "" {
		b.WriteString("event: " + e.Name + "\n")
	}
	if e.Retry > 0 {
		b.WriteString("retry: " + strconv.FormatInt(e.Retry.Milliseconds(), 10) + "\n")
	}
	data := strings.ReplaceAll(strings.ReplaceAll(e.Data, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	return s.write(b.String())
}

// JSON sends v encoded as JSON (one data line) as event name with id.
func (s *Writer) JSON(name, id string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Send(Event{ID: id, Name: name, Data: string(raw)})
}

// Comment writes a comment line (ignored by EventSource) and flushes.
func (s *Writer) Comment(text string) error {
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	return s.write(": " + text + "\n\n")
}

// Retry writes a standalone retry field (the client's reconnect delay) and
// flushes.
func (s *Writer) Retry(d time.Duration) error {
	return s.write("retry: " + strconv.FormatInt(d.Milliseconds(), 10) + "\n\n")
}

// Heartbeat writes the ": heartbeat" keep-alive comment and flushes.
func (s *Writer) Heartbeat() error { return s.Comment("heartbeat") }

// Flush pushes buffered bytes (e.g. the headers of a stream that has not
// sent an event yet) to the client.
func (s *Writer) Flush() error { return s.flush() }

func (s *Writer) write(str string) error {
	if _, err := io.WriteString(s.w, str); err != nil {
		return err
	}
	return s.flush()
}
