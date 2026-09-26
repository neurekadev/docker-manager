// Package ciotest is a scripted Engine for container log and exec tests
// (#8): one container "web" whose state, log history, live log lines and
// exec processes a test controls. Import it from tests only.
package ciotest

import (
	"context"
	"io"
	"strconv"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/agent/engine"
)

// Process is an exec process: it reads stdin, writes output and returns
// its exit code.
type Process func(stdin io.Reader, stdout, stderr io.Writer) int

// Engine implements containerio.Engine.
type Engine struct {
	mu        sync.Mutex
	running   bool
	removed   bool
	logs      []engine.LogEntry
	follow    chan engine.LogEntry
	followEnd chan struct{}
	logCalls  []engine.LogOptions
	execs     map[string]*Exec
	resized   []uint
	process   Process
}

// Exec is a created exec instance.
type Exec struct {
	Spec engine.ExecSpec
	Exit int
}

// New returns an Engine with "web" running.
func New() *Engine {
	return &Engine{running: true, execs: map[string]*Exec{}, follow: make(chan engine.LogEntry, 1024), followEnd: make(chan struct{}),
		process: func(stdin io.Reader, _, _ io.Writer) int { _, _ = io.Copy(io.Discard, stdin); return 0 }}
}

// Entry builds a log entry sec seconds after base.
func Entry(base time.Time, sec int, stream engine.LogStream, text string) engine.LogEntry {
	return engine.LogEntry{Time: base.Add(time.Duration(sec) * time.Second), Stream: stream, Data: []byte(text)}
}

// SetHistory replaces the log history.
func (f *Engine) SetHistory(es ...engine.LogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append([]engine.LogEntry(nil), es...)
}

// Emit sends a live log line to followers (and the history).
func (f *Engine) Emit(e engine.LogEntry) { f.follow <- e }

// SetRunning sets the container's state; stopping ends current follows.
func (f *Engine) SetRunning(running bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running && !running {
		close(f.followEnd)
		f.followEnd = make(chan struct{})
	}
	f.running = running
}

// Remove removes the container (follows end).
func (f *Engine) Remove() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = true
	close(f.followEnd)
	f.followEnd = make(chan struct{})
}

// SetProcess sets the process of the next attached execs.
func (f *Engine) SetProcess(p Process) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.process = p
}

// LogCalls returns the options of every Logs call.
func (f *Engine) LogCalls() []engine.LogOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.LogOptions(nil), f.logCalls...)
}

// Execs returns the created exec instances by ID.
func (f *Engine) Execs() map[string]Exec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]Exec{}
	for k, v := range f.execs {
		out[k] = *v
	}
	return out
}

// Resized returns the (height, width) pairs of every resize.
func (f *Engine) Resized() []uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint(nil), f.resized...)
}

// InspectContainer implements containerio.Engine.
func (f *Engine) InspectContainer(_ context.Context, id string) (engine.ContainerDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removed || id != "web" {
		return engine.ContainerDetails{}, &engine.Error{Code: engine.CodeNotFound, Message: "no such container"}
	}
	return engine.ContainerDetails{ID: "web", Name: "web", State: engine.ContainerState{Running: f.running}}, nil
}

// Logs implements containerio.Engine (Since with whole-second precision,
// like the Engine).
func (f *Engine) Logs(ctx context.Context, id string, o engine.LogOptions, fn func(engine.LogEntry) error) error {
	f.mu.Lock()
	f.logCalls = append(f.logCalls, o)
	hist := append([]engine.LogEntry(nil), f.logs...)
	end, removed := f.followEnd, f.removed
	f.mu.Unlock()
	if id != "web" || removed {
		return &engine.Error{Code: engine.CodeNotFound, Message: "no such container"}
	}
	if !o.TailAll && o.Tail > 0 && len(hist) > o.Tail {
		hist = hist[len(hist)-o.Tail:]
	}
	for _, e := range hist {
		if !o.Since.IsZero() && e.Time.Before(o.Since.Truncate(time.Second)) {
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	if !o.Follow {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-end:
			return nil
		case e := <-f.follow:
			f.mu.Lock()
			f.logs = append(f.logs, e)
			f.mu.Unlock()
			if err := fn(e); err != nil {
				return err
			}
		}
	}
}

// CreateExec implements containerio.Engine.
func (f *Engine) CreateExec(_ context.Context, _ string, spec engine.ExecSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "exec-" + strconv.Itoa(len(f.execs)+1)
	f.execs[id] = &Exec{Spec: spec}
	return id, nil
}

// AttachExec implements containerio.Engine.
func (f *Engine) AttachExec(ctx context.Context, execID string, stdio engine.ExecIO) error {
	f.mu.Lock()
	x := f.execs[execID]
	proc := f.process
	f.mu.Unlock()
	if x == nil {
		return &engine.Error{Code: engine.CodeNotFound}
	}
	stdout, stderr := stdio.Stdout, stdio.Stderr
	if stdio.Tty {
		stderr = stdout
	}
	code := proc(stdio.Stdin, stdout, stderr)
	f.mu.Lock()
	x.Exit = code
	f.mu.Unlock()
	return ctx.Err()
}

// ResizeExec implements containerio.Engine.
func (f *Engine) ResizeExec(_ context.Context, _ string, h, w uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resized = append(f.resized, h, w)
	return nil
}

// InspectExec implements containerio.Engine.
func (f *Engine) InspectExec(_ context.Context, execID string) (engine.ExecStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x := f.execs[execID]
	if x == nil {
		return engine.ExecStatus{}, &engine.Error{Code: engine.CodeNotFound}
	}
	return engine.ExecStatus{ExitCode: x.Exit}, nil
}
