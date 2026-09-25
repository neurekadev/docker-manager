//go:build integration

package containerio_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/agent/containerio"
	"github.com/neurekadev/dockyard/internal/agent/engine"
	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/protocol"
	"github.com/neurekadev/dockyard/internal/streammux"
	"github.com/neurekadev/dockyard/internal/streammux/muxtest"
	"github.com/neurekadev/dockyard/internal/testharness"
	"github.com/neurekadev/dockyard/internal/testutil"
)

// TestEngineLogsAndExec runs the agent's #8 requests and streams against a
// real Docker Engine: bounded logs with stdout/stderr, a followed log that
// survives a restart and ends on removal, exec sessions with stdin, exit
// codes, TTY size and resize, and the clear error for an image without a
// shell (the workload image has none).
func TestEngineLogsAndExec(t *testing.T) {
	e := testharness.StartEngine(t, testharness.EngineOptions{})
	e.LoadWorkload(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)
	c, err := engine.Connect(ctx, engine.Options{Host: e.Host, Logger: testutil.Logger(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	svc := containerio.New(containerio.Options{Engine: func() containerio.Engine { return c }, Clock: clock.Real(),
		Logger: testutil.Logger(t), RestartPoll: 200 * time.Millisecond})
	hs := map[string]muxtest.Handler{}
	for k, h := range svc.Streams() {
		hs[k] = muxtest.Handler(h)
	}
	p := muxtest.New(t, hs)
	closeCode := func(err error) string {
		var ce *streammux.CloseError
		if errors.As(err, &ce) {
			return ce.Code
		}
		if err == nil {
			return ""
		}
		return "untyped: " + err.Error()
	}

	t.Run("logs", func(t *testing.T) {
		id := e.StartWorkload(t, "serve one err:two three")
		e.WaitLog(t, id, "three", time.Minute)
		out, err := svc.Logs(ctx, protocol.ContainerLogsInput{ContainerID: id, Stdout: true, Stderr: true})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range out.Lines {
			got = append(got, l.Stream+":"+string(l.Data))
			if l.At.IsZero() {
				t.Fatalf("line without timestamp %+v", l)
			}
		}
		if strings.Join(got, ",") != "stdout:one,stderr:two,stdout:three" || out.Truncated {
			t.Fatalf("logs %v truncated=%v", got, out.Truncated)
		}
		out, _ = svc.Logs(ctx, protocol.ContainerLogsInput{ContainerID: id, Stderr: true})
		if len(out.Lines) != 1 || string(out.Lines[0].Data) != "two" {
			t.Fatalf("stderr only %+v", out.Lines)
		}
		out, _ = svc.Logs(ctx, protocol.ContainerLogsInput{ContainerID: id, Stdout: true, Stderr: true, Tail: 1})
		if len(out.Lines) != 1 || string(out.Lines[0].Data) != "three" {
			t.Fatalf("tail 1 %+v", out.Lines)
		}
		if _, err := svc.Logs(ctx, protocol.ContainerLogsInput{ContainerID: "missing-container", Stdout: true}); err == nil {
			t.Fatal("logs of a missing container")
		}
	})

	t.Run("follow_restart_remove", func(t *testing.T) {
		id := e.StartWorkload(t, "tick 100")
		e.WaitLog(t, id, "tick 2", time.Minute)
		st, err := p.Open(ctx, protocol.StreamContainerLogs, protocol.ContainerLogsInput{ContainerID: id, Tail: 1, Stdout: true, Stderr: true},
			streammux.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		lines := make(chan protocol.LogLine, 1024)
		ended := make(chan error, 1)
		go func() {
			dec := json.NewDecoder(bufio.NewReader(st))
			for {
				var l protocol.LogLine
				if err := dec.Decode(&l); err != nil {
					ended <- err
					return
				}
				lines <- l
			}
		}()
		next := func() protocol.LogLine {
			t.Helper()
			select {
			case l := <-lines:
				return l
			case err := <-ended:
				t.Fatalf("log stream ended early: %v", err)
			case <-ctx.Done():
				t.Fatal("no log line")
			}
			return protocol.LogLine{}
		}
		for range 3 {
			if l := next(); !strings.HasPrefix(string(l.Data), "tick ") {
				t.Fatalf("line %q", l.Data)
			}
		}
		if err := c.StopContainer(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
		// After the restart the counter starts again at 1; the stream
		// delivers it without repeating what was already sent.
		var last time.Time
		for {
			l := next()
			if l.At.Before(last) {
				t.Fatalf("line out of order: %v before %v", l.At, last)
			}
			last = l.At
			if string(l.Data) == "tick 1" {
				break
			}
		}
		if err := c.RemoveContainer(ctx, id, engine.RemoveOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case <-lines:
				continue
			case err := <-ended:
				if closeCode(err) != protocol.CodeNotFound {
					t.Fatalf("stream end after removal: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("log stream did not end after removal")
			}
			break
		}
	})

	run := func(t *testing.T, st *streammux.Stream) (string, string, *protocol.StreamClosePayload, error) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, ch, err := st.ReadChannel(buf)
			if ch == protocol.LogStderr {
				stderr.Write(buf[:n])
			} else {
				stdout.Write(buf[:n])
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				return stdout.String(), stderr.String(), st.RemoteClose(), err
			}
		}
	}

	t.Run("exec", func(t *testing.T) {
		id := e.StartWorkload(t, "serve up")
		e.WaitLog(t, id, "up", time.Minute)
		open := func(in protocol.ExecCreateInput) *streammux.Stream {
			t.Helper()
			in.ContainerID = id
			out, err := svc.CreateExec(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			st, err := p.Open(ctx, protocol.StreamContainerExec, protocol.ExecStreamInput(out), streammux.OpenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return st
		}

		// stdin to stdout, end of input ends the process.
		st := open(protocol.ExecCreateInput{Cmd: []string{testharness.WorkloadBinary, "stdin"}})
		if _, err := st.WriteChannel("stdin", []byte("hello engine\n")); err != nil {
			t.Fatal(err)
		}
		_ = st.CloseWrite()
		stdout, _, rc, err := run(t, st)
		if err != nil || stdout != "hello engine\n" || rc == nil || rc.ExitCode == nil || *rc.ExitCode != 0 {
			t.Fatalf("stdin exec: %q %+v %v", stdout, rc, err)
		}

		// Exit codes and stderr separation without a TTY.
		st = open(protocol.ExecCreateInput{Cmd: []string{testharness.WorkloadBinary, "exit", "7", "bye"}})
		_ = st.CloseWrite()
		stdout, _, rc, err = run(t, st)
		if err != nil || stdout != "bye\n" || rc == nil || rc.ExitCode == nil || *rc.ExitCode != 7 {
			t.Fatalf("exit exec: %q %+v %v", stdout, rc, err)
		}

		// TTY: the initial size, a resize, then the new size.
		out, err := svc.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: id, Cmd: []string{testharness.WorkloadBinary, "ttysize"},
			Tty: true, Cols: 100, Rows: 30})
		if err != nil {
			t.Fatal(err)
		}
		st, err = p.Open(ctx, protocol.StreamContainerExec, protocol.ExecStreamInput(out), streammux.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 256)
		var first bytes.Buffer
		for !strings.Contains(first.String(), "\n") {
			n, _, err := st.ReadChannel(buf)
			first.Write(buf[:n])
			if err != nil {
				t.Fatalf("tty output %q: %v", first.String(), err)
			}
		}
		if !strings.Contains(first.String(), "size 30 100") {
			t.Fatalf("initial tty size %q", first.String())
		}
		if _, err := svc.ResizeExec(ctx, protocol.ExecResizeInput{ExecID: out.ExecID, Cols: 120, Rows: 40}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.WriteChannel("stdin", []byte("\r")); err != nil {
			t.Fatal(err)
		}
		rest, _, rc, err := run(t, st)
		if err != nil || !strings.Contains(rest, "size 40 120") || rc == nil || rc.ExitCode == nil || *rc.ExitCode != 0 {
			t.Fatalf("resized tty %q %+v %v", rest, rc, err)
		}

		// No shell in the image: a clear not_found, not a hang.
		st = open(protocol.ExecCreateInput{Cmd: []string{"/bin/sh"}, Tty: true})
		_ = st.CloseWrite()
		if _, _, _, err := run(t, st); closeCode(err) != protocol.CodeNotFound || !strings.Contains(err.Error(), "no shell") {
			t.Fatalf("missing shell: %v", err)
		}

		// A stopped container refuses new sessions.
		if err := c.StopContainer(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CreateExec(ctx, protocol.ExecCreateInput{ContainerID: id, Cmd: []string{"/workload", "echo"}}); err == nil {
			t.Fatal("exec in a stopped container")
		}
	})
}
