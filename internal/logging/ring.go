package logging

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
)

// Ring keeps the most recent log lines in memory, JSON-encoded, for the
// support bundle (#34). It is bounded by line count and line length; it
// holds what the logger wrote, which follows the logging rules (no secrets:
// Secret values render as [REDACTED]).
type Ring struct {
	mu      sync.Mutex
	lines   [][]byte
	next    int
	full    bool
	maxLine int
	dropped uint64
}

// Default ring bounds.
const (
	DefaultRingLines   = 2000
	DefaultRingLineMax = 8 << 10
)

// NewRing returns a ring of n lines (DefaultRingLines when n <= 0), each
// truncated to DefaultRingLineMax bytes.
func NewRing(n int) *Ring {
	if n <= 0 {
		n = DefaultRingLines
	}
	return &Ring{lines: make([][]byte, n), maxLine: DefaultRingLineMax}
}

// Write stores one line (slog's JSON handler writes each record in one
// call). It implements io.Writer and never fails.
func (r *Ring) Write(p []byte) (int, error) {
	line := bytes.TrimRight(p, "\n")
	if len(line) > r.maxLine {
		line = append(append([]byte{}, line[:r.maxLine]...), []byte(`…(truncated)`)...)
	} else {
		line = append([]byte{}, line...)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		r.dropped++
	}
	r.lines[r.next] = line
	r.next++
	if r.next == len(r.lines) {
		r.next, r.full = 0, true
	}
	return len(p), nil
}

// Lines returns the retained lines, oldest first, and how many older lines
// were dropped.
func (r *Ring) Lines() (lines [][]byte, dropped uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		lines = append(lines, r.lines[r.next:]...)
	}
	lines = append(lines, r.lines[:r.next]...)
	return lines, r.dropped
}

// Handler returns a JSON handler writing into the ring at level.
func (r *Ring) Handler(level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(r, &slog.HandlerOptions{Level: level})
}

// Tee returns a handler that sends each record to every handler that is
// enabled for its level (the process log and the Ring).
func Tee(handlers ...slog.Handler) slog.Handler { return teeHandler(handlers) }

type teeHandler []slog.Handler

func (t teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range t {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (t teeHandler) Handle(ctx context.Context, rec slog.Record) error {
	var first error
	for _, h := range t {
		if !h.Enabled(ctx, rec.Level) {
			continue
		}
		if err := h.Handle(ctx, rec.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (t teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithGroup(name)
	}
	return out
}
