package containerio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/containerio/ciotest"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
	"code.neureka.dev/docker-manager/docker-manager/internal/agent/session"
	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/protocol"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux"
	"code.neureka.dev/docker-manager/docker-manager/internal/streammux/muxtest"
	"code.neureka.dev/docker-manager/docker-manager/internal/testutil"
)

func entry(sec int, stream engine.LogStream, text string) engine.LogEntry {
	return ciotest.Entry(testutil.Epoch, sec, stream, text)
}

func newService(t *testing.T, f *ciotest.Engine, clk clock.Clock) *Service {
	t.Helper()
	return New(Options{Engine: func() Engine { return f }, Clock: clk, Logger: testutil.Logger(t), RestartPoll: time.Second})
}

func code(err error) string {
	var he *session.HandlerError
	if errors.As(err, &he) {
		return he.Code
	}
	var ce *streammux.CloseError
	if errors.As(err, &ce) {
		return ce.Code
	}
	if err == nil {
		return ""
	}
	return "untyped: " + err.Error()
}

func TestLogsTailSplitAndBounds(t *testing.T) {
	f := ciotest.New()
	f.SetHistory(entry(1, engine.Stdout, "one\n"), entry(2, engine.Stderr, "two\n"), entry(3, engine.Stdout, "cont"),
		entry(3, engine.Stdout, "inued\n"), entry(4, engine.Stdout, strings.Repeat("x", protocol.MaxLogLine+10)+"\n"))
	s := newService(t, f, testutil.FakeClock())
	ctx := testutil.Context(t)
	out, err := s.Logs(ctx, protocol.ContainerLogsInput{ContainerID: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 6 || string(out.Lines[0].Data) != "one" || out.Lines[1].Stream != protocol.LogStderr ||
		!out.Lines[2].Partial || out.Lines[3].Partial || !out.Lines[4].Partial || len(out.Lines[4].Data) != protocol.MaxLogLine ||
		out.Lines[5].Partial || len(out.Lines[5].Data) != 10 {
		t.Fatalf("lines %+v", out.Lines)
	}
	if o := f.LogCalls()[0]; !o.Timestamps || o.Tail != protocol.DefaultLogTail || o.Follow {
		t.Fatalf("log options %+v", o)
	}
	out, _ = s.Logs(ctx, protocol.ContainerLogsInput{ContainerID: "web", Tail: 2})
	if len(out.Lines) != 2 {
		t.Fatalf("tail 2: %d lines", len(out.Lines))
	}
	// Sub-second since filtering (the Engine filters by whole seconds).
	out, _ = s.Logs(ctx, protocol.ContainerLogsInput{ContainerID: "web", Since: testutil.Epoch.Add(2*time.Second + time.Millisecond)})
	if len(out.Lines) != 4 || string(out.Lines[0].Data) != "cont" {
		t.Fatalf("since: %+v", out.Lines)
	}
	if _, err := s.Logs(ctx, protocol.ContainerLogsInput{ContainerID: "nope"}); code(err) != protocol.CodeNotFound {
		t.Fatalf("missing container: %v", err)
	}
	if _, err := s.Logs(ctx, protocol.ContainerLogsInput{ContainerID: "../x"}); code(err) != protocol.CodeInvalidFrame {
		t.Fatalf("bad reference: %v", err)
	}
	// The byte bound drops the oldest lines.
	g := ciotest.New()
	var hist []engine.LogEntry
	for i := range 100 {
		hist = append(hist, entry(i, engine.Stdout, strings.Repeat("y", 10<<10)+"\n"))
	}
	g.SetHistory(hist...)
	out, _ = newService(t, g, testutil.FakeClock()).Logs(ctx, protocol.ContainerLogsInput{ContainerID: "web", Tail: 100})
	size := 0
	for _, l := range out.Lines {
		size += len(l.Data)
	}
	if !out.Truncated || size > protocol.MaxLogBytes || !out.Lines[len(out.Lines)-1].At.Equal(testutil.Epoch.Add(99*time.Second)) {
		t.Fatalf("bounded: %d lines, %d bytes, truncated %v", len(out.Lines), size, out.Truncated)
	}
}

func pipe(t *testing.T, s *Service) *muxtest.Pipe {
	hs := map[string]muxtest.Handler{}
	for k, h := range s.Streams() {
		hs[k] = muxtest.Handler(h)
	}
	return muxtest.New(t, hs)
}

// TestFollowLogsSurvivesRestartAndEndsOnRemoval: the stream sends the
// tail, then live lines; when the container stops it waits and resumes
// after the last line (no repeats); removal ends it with not_found.
func TestFollowLogsSurvivesRestartAndEndsOnRemoval(t *testing.T) {
	f := ciotest.New()
	f.SetHistory(entry(1, engine.Stdout, "old\n"), entry(2, engine.Stdout, "tail\n"))
	clk := testutil.FakeClock()
	s := newService(t, f, clk)
	ctx := testutil.Context(t)
	st, err := pipe(t, s).Open(ctx, protocol.StreamContainerLogs, protocol.ContainerLogsInput{ContainerID: "web", Tail: 1}, streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(st)
	next := func() protocol.LogLine {
		t.Helper()
		if !lines.Scan() {
			t.Fatalf("stream ended: %v", lines.Err())
		}
		var l protocol.LogLine
		if err := json.Unmarshal(lines.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := next(); string(l.Data) != "tail" {
		t.Fatalf("first line %q", l.Data)
	}
	f.Emit(entry(3, engine.Stdout, "live\n"))
	if l := next(); string(l.Data) != "live" {
		t.Fatalf("live line %q", l.Data)
	}
	// The container stops: the follow ends, the service polls.
	f.SetRunning(false)
	if err := clk.BlockUntilWaiters(ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.SetRunning(true)
	clk.Advance(time.Second)
	f.Emit(entry(4, engine.Stdout, "after restart\n"))
	if l := next(); string(l.Data) != "after restart" {
		t.Fatalf("after restart %q (older lines must not repeat)", l.Data)
	}
	calls := f.LogCalls()
	if resume := calls[len(calls)-1]; !resume.Since.Equal(testutil.Epoch.Add(3*time.Second)) || !resume.TailAll {
		t.Fatalf("resume options %+v", resume)
	}
	// Removal ends the stream.
	f.Remove()
	for lines.Scan() {
	}
	if code(lines.Err()) != protocol.CodeNotFound {
		t.Fatalf("end: %v", lines.Err())
	}
}

// TestExecSessionRelaysAndReportsExit: stdin reaches the process, output
// comes back on its channels, the exit code closes the stream; resize,
// delete, double attach and a missing command behave.
func TestExecSessionRelaysAndReportsExit(t *testing.T) {
	f := ciotest.New()
	f.SetProcess(func(stdin io.Reader, stdout, stderr io.Writer) int {
		b, _ := io.ReadAll(stdin)
		_, _ = stdout.Write(bytes.ToUpper(b))
		_, _ = stderr.Write([]byte("bye"))
		return 3
	})
	s := newService(t, f, testutil.FakeClock())
	ctx := testutil.Context(t)
	out, err := s.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: "web", Cmd: []string{"/bin/sh"}, Tty: false, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if spec := f.Execs()[out.ExecID].Spec; !spec.AttachStdin || spec.Width != 80 || spec.Height != 24 || spec.Cmd[0] != "/bin/sh" {
		t.Fatalf("exec spec %+v", spec)
	}
	p := pipe(t, s)
	st, err := p.Open(ctx, protocol.StreamContainerExec, protocol.ExecStreamInput(out), streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteChannel("stdin", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseWrite(); err != nil { // end of input
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	buf := make([]byte, 64)
	for {
		n, ch, err := st.ReadChannel(buf)
		if ch == protocol.LogStderr {
			stderr.Write(buf[:n])
		} else {
			stdout.Write(buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			break
		}
	}
	rc := st.RemoteClose()
	if stdout.String() != "HELLO" || stderr.String() != "bye" || rc == nil || rc.ExitCode == nil || *rc.ExitCode != 3 {
		t.Fatalf("stdout %q stderr %q close %+v", stdout.String(), stderr.String(), rc)
	}
	// The session is gone after it ended.
	if _, err := s.ResizeExec(ctx, protocol.ExecResizeInput{ExecID: out.ExecID, Cols: 1, Rows: 1}); code(err) != protocol.CodeNotFound {
		t.Fatalf("resize after end: %v", err)
	}

	// A TTY session: resize, then delete closes stdin (the process ends).
	block := make(chan struct{})
	f.SetProcess(func(stdin io.Reader, _, _ io.Writer) int {
		close(block)
		_, _ = io.Copy(io.Discard, stdin) // until stdin closes
		return 0
	})
	out, err = s.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: "web", Cmd: []string{"sh"}, Tty: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err = p.Open(ctx, protocol.StreamContainerExec, protocol.ExecStreamInput(out), streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-block
	st2, err := p.Open(ctx, protocol.StreamContainerExec, protocol.ExecStreamInput(out), streammux.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(st2); code(err) != protocol.CodeConflict {
		t.Fatalf("second attach: %v", err)
	}
	if _, err := s.ResizeExec(ctx, protocol.ExecResizeInput{ExecID: out.ExecID, Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	if r := f.Resized(); r[0] != 40 || r[1] != 120 {
		t.Fatalf("resize %v", r)
	}
	if _, err := s.DeleteExec(ctx, protocol.ExecDeleteInput(out)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(st); err != nil {
		t.Fatalf("stream after delete: %v", err)
	}

	// No shell in the image: a clear not_found.
	f.SetProcess(func(_ io.Reader, stdout, _ io.Writer) int {
		_, _ = stdout.Write([]byte(`OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/sh": stat /bin/sh: no such file or directory: unknown`))
		return 127
	})
	out, _ = s.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: "web", Cmd: []string{"/bin/sh"}, Tty: true})
	st, _ = p.Open(ctx, protocol.StreamContainerExec, protocol.ExecStreamInput(out), streammux.OpenOptions{})
	_ = st.CloseWrite()
	if _, err := io.ReadAll(st); code(err) != protocol.CodeNotFound || !strings.Contains(err.Error(), "no shell") {
		t.Fatalf("missing shell: %v", err)
	}

	// Refusals.
	f.SetRunning(false)
	if _, err := s.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: "web", Cmd: []string{"sh"}}); code(err) != protocol.CodeConflict {
		t.Fatalf("stopped container: %v", err)
	}
	for _, in := range []protocol.ExecCreateInput{{ContainerID: "web"}, {ContainerID: "a b", Cmd: []string{"sh"}},
		{ContainerID: "web", Cmd: []string{"sh\x00"}}} {
		if _, err := s.CreateExec(ctx, in); code(err) != protocol.CodeInvalidFrame {
			t.Fatalf("invalid %+v: %v", in, err)
		}
	}
	if _, err := s.DeleteExec(ctx, protocol.ExecDeleteInput{ExecID: "nope"}); code(err) != protocol.CodeNotFound {
		t.Fatalf("delete unknown: %v", err)
	}
}

// TestUnattachedExecsExpire: exec instances never attached are forgotten.
func TestUnattachedExecsExpire(t *testing.T) {
	f := ciotest.New()
	clk := testutil.FakeClock()
	s := New(Options{Engine: func() Engine { return f }, Clock: clk, Logger: testutil.Logger(t), MaxExecs: 1, UnattachedTTL: time.Minute})
	ctx := testutil.Context(t)
	if _, err := s.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: "web", Cmd: []string{"sh"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: "web", Cmd: []string{"sh"}}); code(err) != protocol.CodeBusy {
		t.Fatalf("limit: %v", err)
	}
	clk.Advance(2 * time.Minute)
	if _, err := s.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: "web", Cmd: []string{"sh"}}); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}
