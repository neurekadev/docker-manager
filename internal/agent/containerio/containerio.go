// Package containerio serves container logs and exec sessions on the agent
// (#8): the container.logs request (a bounded tail) and stream (follow
// with a resume cursor, surviving container restarts), and the
// container.exec.create/resize/delete requests plus the container.exec
// stream that attaches stdin/stdout/stderr of an exec instance. Everything
// goes through the Moby adapter (internal/agent/engine); the command runs
// inside the container, never on the host. Log lines and terminal I/O are
// never logged.
package containerio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/agent/session"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/streammux"
)

// Engine is the part of the Engine adapter the service needs.
type Engine interface {
	InspectContainer(ctx context.Context, id string) (engine.ContainerDetails, error)
	Logs(ctx context.Context, id string, o engine.LogOptions, fn func(engine.LogEntry) error) error
	CreateExec(ctx context.Context, containerID string, spec engine.ExecSpec) (string, error)
	AttachExec(ctx context.Context, execID string, stdio engine.ExecIO) error
	ResizeExec(ctx context.Context, execID string, height, width uint) error
	InspectExec(ctx context.Context, execID string) (engine.ExecStatus, error)
}

// Options configures the service.
type Options struct {
	// Engine returns the connected Engine (nil while disconnected).
	Engine func() Engine
	Clock  clock.Clock
	Logger *slog.Logger
	// RestartPoll is how often a followed log waits for a stopped
	// container to run again (default 2 s).
	RestartPoll time.Duration
	// MaxExecs bounds the exec instances tracked at once (default 256).
	MaxExecs int
	// UnattachedTTL forgets exec instances never attached (default 2 min;
	// the manager allows 60 s).
	UnattachedTTL time.Duration
}

// Service serves logs and exec sessions.
type Service struct {
	opts Options
	log  *slog.Logger

	mu    sync.Mutex
	execs map[string]*execState
}

type execState struct {
	containerID string
	tty         bool
	created     time.Time
	attached    bool
	// closeStdin ends the process's input (set while attached).
	closeStdin func()
	deleted    bool
}

// New returns the service.
func New(o Options) *Service {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.RestartPoll <= 0 {
		o.RestartPoll = 2 * time.Second
	}
	if o.MaxExecs <= 0 {
		o.MaxExecs = 256
	}
	if o.UnattachedTTL <= 0 {
		o.UnattachedTTL = 2 * time.Minute
	}
	return &Service{opts: o, log: o.Logger.With("component", "containerio"), execs: map[string]*execState{}}
}

func fail(code, format string, args ...any) error {
	return &session.HandlerError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// engineErr maps Engine adapter errors to protocol errors (messages are
// the adapter's sanitized ones).
func engineErr(err error) error {
	var he *session.HandlerError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &he):
		return err
	case engine.IsCode(err, engine.CodeNotFound):
		return fail(protocol.CodeNotFound, "the container or exec instance does not exist")
	case engine.IsCode(err, engine.CodeConflict):
		return fail(protocol.CodeConflict, "the container is not running")
	case engine.IsCode(err, engine.CodeEngineUnavailable), engine.IsCode(err, engine.CodeTimeout):
		return fail(protocol.CodeEngineUnavailable, "the Docker Engine is unavailable")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fail(protocol.CodeCancelled, "cancelled")
	}
	return fail(protocol.CodeEngineError, "the Docker Engine refused the operation")
}

func (s *Service) engine() (Engine, error) {
	if s.opts.Engine != nil {
		if e := s.opts.Engine(); e != nil {
			return e, nil
		}
	}
	return nil, fail(protocol.CodeEngineUnavailable, "the Docker Engine is not connected")
}

func validContainerRef(id string) bool {
	return id != "" && len(id) <= 255 && !strings.ContainsAny(id, "/\\\x00 ")
}

// splitLines turns an adapter log entry into bounded LogLines: the
// trailing newline is dropped; an entry without one continues in the next
// entry (partial); parts longer than MaxLogLine are split.
func splitLines(e engine.LogEntry, last time.Time) []protocol.LogLine {
	at := e.Time
	if at.IsZero() {
		at = last
	}
	data := e.Data
	partial := true
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data, partial = data[:n-1], false
	}
	stream := protocol.LogStdout
	if e.Stream == engine.Stderr {
		stream = protocol.LogStderr
	}
	var out []protocol.LogLine
	for len(data) > protocol.MaxLogLine {
		out = append(out, protocol.LogLine{At: at, Stream: stream, Data: bytes.Clone(data[:protocol.MaxLogLine]), Partial: true})
		data = data[protocol.MaxLogLine:]
	}
	return append(out, protocol.LogLine{At: at, Stream: stream, Data: bytes.Clone(data), Partial: partial})
}

// Logs returns a bounded tail of a container's output.
func (s *Service) Logs(ctx context.Context, in protocol.ContainerLogsInput) (protocol.ContainerLogsOutput, error) {
	out := protocol.ContainerLogsOutput{Lines: []protocol.LogLine{}}
	if !validContainerRef(in.ContainerID) {
		return out, fail(protocol.CodeInvalidFrame, "invalid container reference")
	}
	eng, err := s.engine()
	if err != nil {
		return out, err
	}
	tail := in.Tail
	if tail <= 0 {
		tail = protocol.DefaultLogTail
	}
	tail = min(tail, protocol.MaxLogTail)
	var last time.Time
	size := 0
	err = eng.Logs(ctx, in.ContainerID, engine.LogOptions{Stdout: in.Stdout, Stderr: in.Stderr, Timestamps: true, Tail: tail,
		Since: in.Since, Until: in.Until}, func(e engine.LogEntry) error {
		for _, l := range splitLines(e, last) {
			last = l.At
			if !in.Since.IsZero() && l.At.Before(in.Since) {
				continue // the Engine filters by whole seconds
			}
			out.Lines = append(out.Lines, l)
			size += len(l.Data)
		}
		return nil
	})
	if err != nil {
		return out, engineErr(err)
	}
	if len(out.Lines) > tail {
		out.Lines, out.Truncated = out.Lines[len(out.Lines)-tail:], true
	}
	for size > protocol.MaxLogBytes && len(out.Lines) > 0 {
		size -= len(out.Lines[0].Data)
		out.Lines, out.Truncated = out.Lines[1:], true
	}
	return out, nil
}

// FollowLogs streams a container's output as NDJSON LogLine records until
// the manager closes the stream: the last tail lines (or everything after
// since), then new output. When the container stops the stream stays open
// and resumes when it runs again; when it is removed the stream ends with
// not_found.
func (s *Service) FollowLogs(ctx context.Context, st *streammux.Stream) error {
	var in protocol.ContainerLogsInput
	if err := json.Unmarshal(st.Input(), &in); err != nil || !validContainerRef(in.ContainerID) {
		return fail(protocol.CodeInvalidFrame, "invalid logs input")
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	if _, err := eng.InspectContainer(ctx, in.ContainerID); err != nil {
		return engineErr(err)
	}
	tail := in.Tail
	if tail <= 0 {
		tail = 200
	}
	tail = min(tail, protocol.MaxLogTail)
	cursor := in.Since
	w := bufio.NewWriterSize(st, 32<<10)
	enc := json.NewEncoder(w)
	var last time.Time
	// sent counts the lines already sent at the cursor's timestamp, so a
	// resume after a restart does not repeat them.
	sent := 0
	for {
		o := engine.LogOptions{Follow: true, Stdout: in.Stdout, Stderr: in.Stderr, Timestamps: true, Since: cursor}
		if cursor.IsZero() {
			o.Tail = tail
		} else {
			o.TailAll = true
		}
		skip := sent
		err := eng.Logs(ctx, in.ContainerID, o, func(e engine.LogEntry) error {
			for _, l := range splitLines(e, last) {
				last = l.At
				if !cursor.IsZero() && l.At.Before(cursor) {
					continue
				}
				if skip > 0 && l.At.Equal(cursor) {
					skip--
					continue
				}
				if err := enc.Encode(l); err != nil {
					return err
				}
				if l.At.Equal(cursor) {
					sent++
				} else {
					cursor, sent = l.At, 1
				}
			}
			return w.Flush()
		})
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			var ce *streammux.CloseError
			if errors.As(err, &ce) || errors.Is(err, streammux.ErrSessionClosed) {
				return nil // the manager went away
			}
			if engine.IsCode(err, engine.CodeNotFound) {
				return fail(protocol.CodeNotFound, "the container was removed")
			}
			return engineErr(err)
		}
		if err := w.Flush(); err != nil {
			return nil
		}
		// The container stopped: wait until it runs again (or is removed).
		if err := s.waitRunning(ctx, eng, in.ContainerID); err != nil {
			return err
		}
		if cursor.IsZero() {
			cursor = s.opts.Clock.Now().UTC()
		}
	}
}

func (s *Service) waitRunning(ctx context.Context, eng Engine, id string) error {
	for {
		d, err := eng.InspectContainer(ctx, id)
		switch {
		case ctx.Err() != nil:
			return nil
		case engine.IsCode(err, engine.CodeNotFound):
			return fail(protocol.CodeNotFound, "the container was removed")
		case err == nil && d.State.Running:
			return nil
		}
		t := s.opts.Clock.NewTimer(s.opts.RestartPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C():
		}
	}
}

// CreateExec creates an exec instance in a running container.
func (s *Service) CreateExec(ctx context.Context, in protocol.ExecCreateInput) (protocol.ExecCreateOutput, error) {
	var out protocol.ExecCreateOutput
	if !validContainerRef(in.ContainerID) || len(in.Cmd) == 0 || len(in.Cmd) > protocol.MaxExecArgs {
		return out, fail(protocol.CodeInvalidFrame, "invalid exec input")
	}
	n := 0
	for _, a := range in.Cmd {
		n += len(a)
		if strings.ContainsRune(a, 0) {
			return out, fail(protocol.CodeInvalidFrame, "invalid exec command")
		}
	}
	if n > protocol.MaxExecArgBytes || in.Cols > 10000 || in.Rows > 10000 {
		return out, fail(protocol.CodeInvalidFrame, "invalid exec command or size")
	}
	eng, err := s.engine()
	if err != nil {
		return out, err
	}
	d, err := eng.InspectContainer(ctx, in.ContainerID)
	if err != nil {
		return out, engineErr(err)
	}
	if !d.State.Running || d.State.Paused {
		return out, fail(protocol.CodeConflict, "the container is not running")
	}
	s.mu.Lock()
	s.expireLocked()
	full := len(s.execs) >= s.opts.MaxExecs
	s.mu.Unlock()
	if full {
		return out, fail(protocol.CodeBusy, "too many exec sessions on this agent")
	}
	id, err := eng.CreateExec(ctx, d.ID, engine.ExecSpec{Cmd: in.Cmd, Tty: in.Tty, AttachStdin: true, WorkingDir: in.WorkingDir,
		User: in.User, Height: in.Rows, Width: in.Cols})
	if err != nil {
		return out, engineErr(err)
	}
	s.mu.Lock()
	s.execs[id] = &execState{containerID: d.ID, tty: in.Tty, created: s.opts.Clock.Now()}
	s.mu.Unlock()
	return protocol.ExecCreateOutput{ExecID: id}, nil
}

// expireLocked forgets exec instances that were never attached in time.
func (s *Service) expireLocked() {
	now := s.opts.Clock.Now()
	for id, e := range s.execs {
		if !e.attached && now.Sub(e.created) > s.opts.UnattachedTTL {
			delete(s.execs, id)
		}
	}
}

// ResizeExec resizes an exec's terminal.
func (s *Service) ResizeExec(ctx context.Context, in protocol.ExecResizeInput) (struct{}, error) {
	s.mu.Lock()
	e := s.execs[in.ExecID]
	s.mu.Unlock()
	if e == nil {
		return struct{}{}, fail(protocol.CodeNotFound, "unknown exec session")
	}
	if !e.tty || in.Cols == 0 || in.Rows == 0 || in.Cols > 10000 || in.Rows > 10000 {
		return struct{}{}, fail(protocol.CodeInvalidFrame, "invalid terminal size")
	}
	eng, err := s.engine()
	if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, engineErr(eng.ResizeExec(ctx, in.ExecID, in.Rows, in.Cols))
}

// DeleteExec ends an exec session: stdin is closed (the Engine cannot kill
// an exec process; one ignoring end of input runs until it exits).
func (s *Service) DeleteExec(_ context.Context, in protocol.ExecDeleteInput) (struct{}, error) {
	s.mu.Lock()
	e := s.execs[in.ExecID]
	var closeStdin func()
	if e != nil {
		e.deleted = true
		closeStdin = e.closeStdin
		if !e.attached {
			delete(s.execs, in.ExecID)
		}
	}
	s.mu.Unlock()
	if e == nil {
		return struct{}{}, fail(protocol.CodeNotFound, "unknown exec session")
	}
	if closeStdin != nil {
		closeStdin()
	}
	return struct{}{}, nil
}

// notFoundMarkers identify an exec whose command does not exist in the
// image (Engine/OCI runtime messages).
var notFoundMarkers = []string{"executable file not found", "no such file or directory", "not found"}

// Exec attaches the container.exec stream to a created exec instance:
// stdin frames feed the process, its output flows back (stdout/stderr
// channels), and the final stream_close carries the exit code. A command
// missing from the image ends with not_found.
func (s *Service) Exec(ctx context.Context, st *streammux.Stream) error {
	var in protocol.ExecStreamInput
	if err := json.Unmarshal(st.Input(), &in); err != nil {
		return fail(protocol.CodeInvalidFrame, "invalid exec input")
	}
	eng, err := s.engine()
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	s.mu.Lock()
	e := s.execs[in.ExecID]
	switch {
	case e == nil || e.deleted:
		s.mu.Unlock()
		return fail(protocol.CodeNotFound, "unknown exec session")
	case e.attached:
		s.mu.Unlock()
		return fail(protocol.CodeConflict, "the exec session is already attached")
	}
	e.attached = true
	e.closeStdin = func() { _ = pw.Close() }
	tty := e.tty
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.execs, in.ExecID)
		s.mu.Unlock()
		_ = pw.Close()
	}()

	// stdin: frames from the manager until it half-closes.
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, _, err := st.ReadChannel(buf)
			if n > 0 {
				if _, werr := pw.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				_ = pw.Close()
				return
			}
		}
	}()
	head := &headBuffer{max: 1024}
	stdout := &channelWriter{st: st, channel: protocol.LogStdout, head: head}
	stderr := &channelWriter{st: st, channel: protocol.LogStderr, head: head}
	err = eng.AttachExec(ctx, in.ExecID, engine.ExecIO{Tty: tty, Stdin: pr, Stdout: stdout, Stderr: stderr})
	if ctx.Err() != nil {
		return nil // the stream or session ended
	}
	if err != nil {
		return engineErr(err)
	}
	status, err := eng.InspectExec(context.WithoutCancel(ctx), in.ExecID)
	if err != nil {
		return engineErr(err)
	}
	if (status.ExitCode == 126 || status.ExitCode == 127) && head.contains(notFoundMarkers) {
		return fail(protocol.CodeNotFound, "the command was not found in the container (the image may have no shell); choose another command")
	}
	return st.CloseWriteExit(status.ExitCode)
}

// channelWriter writes process output to a stream channel.
type channelWriter struct {
	st      *streammux.Stream
	channel string
	head    *headBuffer
}

func (w *channelWriter) Write(p []byte) (int, error) {
	w.head.add(p)
	return w.st.WriteChannel(w.channel, p)
}

// headBuffer keeps the first bytes of the output (to recognize a missing
// command); never logged.
type headBuffer struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func (h *headBuffer) add(p []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := h.max - len(h.b); n > 0 {
		h.b = append(h.b, p[:min(n, len(p))]...)
	}
}

func (h *headBuffer) contains(markers []string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	low := strings.ToLower(string(h.b))
	return slices.ContainsFunc(markers, func(m string) bool { return strings.Contains(low, m) })
}

func handler[I, O any](fn func(context.Context, I) (O, error)) session.RequestHandler {
	return func(ctx context.Context, input json.RawMessage) (any, error) {
		var in I
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fail(protocol.CodeInvalidFrame, "malformed input")
		}
		return fn(ctx, in)
	}
}

// Requests returns the container.logs and container.exec.* handlers.
func (s *Service) Requests() map[string]session.RequestHandler {
	return map[string]session.RequestHandler{
		protocol.ReqContainerLogs:       handler(s.Logs),
		protocol.ReqContainerExecCreate: handler(s.CreateExec),
		protocol.ReqContainerExecResize: handler(s.ResizeExec),
		protocol.ReqContainerExecDelete: handler(s.DeleteExec),
	}
}

// Streams returns the container.logs and container.exec stream handlers.
func (s *Service) Streams() map[string]session.StreamHandler {
	return map[string]session.StreamHandler{
		protocol.StreamContainerLogs: s.FollowLogs,
		protocol.StreamContainerExec: s.Exec,
	}
}
