// Package testutil holds helpers shared by DockYard's unit tests.
//
// It must only be imported from _test.go files. Time control lives in
// internal/clock (clock.NewFake); this package wires common fixtures.
package testutil

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/clock"
)

// Epoch is the default start time for fake clocks in tests.
var Epoch = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// FakeClock returns a fake clock starting at Epoch.
func FakeClock() *clock.Fake { return clock.NewFake(Epoch) }

// Logger returns a logger that writes through t.Log, so output only appears
// for failing tests (or with -v).
func Logger(t testing.TB) *slog.Logger {
	t.Helper()
	w := &tWriter{t: t}
	t.Cleanup(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.done = true
	})
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// CaptureLogger returns a JSON logger and the buffer it writes to. Use it to
// assert on log output, e.g. that secrets never appear.
func CaptureLogger() (*slog.Logger, *LogBuffer) {
	buf := &LogBuffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// LogBuffer is a concurrency-safe log sink.
type LogBuffer struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

// String returns everything written so far.
func (b *LogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// Context returns a context canceled when the test ends, with a timeout as
// a safety net against hangs.
func Context(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// tWriter forwards to t.Log until the test finishes; late writes from
// goroutines that outlive the test are dropped instead of panicking.
type tWriter struct {
	mu   sync.Mutex
	t    testing.TB
	done bool
}

func (w *tWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// DriveClock runs fn in a goroutine and advances clk by step whenever a
// timer is waiting, until fn returns (its error) or ctx ends. Use it for
// code that sleeps on the fake clock an unknown number of times (rate
// limits, backoff).
func DriveClock(ctx context.Context, clk *clock.Fake, step time.Duration, fn func() error) error {
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- fn()
	}()
	for {
		select {
		case err := <-done:
			return err
		default:
		}
		bctx, cancel := context.WithCancel(ctx)
		stop := make(chan struct{})
		go func() {
			select {
			case <-finished:
				cancel()
			case <-stop:
			}
		}()
		err := clk.BlockUntilWaiters(bctx, 1)
		close(stop)
		cancel()
		if err != nil {
			select {
			case <-finished:
				continue
			default:
			}
			return err
		}
		clk.Advance(step)
	}
}
