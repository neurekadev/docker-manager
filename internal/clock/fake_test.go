package clock

import (
	"context"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func TestFakeTimerFiresOnAdvance(t *testing.T) {
	f := NewFake(epoch)
	timer := f.NewTimer(10 * time.Second)

	f.Advance(9 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired early")
	default:
	}

	f.Advance(time.Second)
	select {
	case got := <-timer.C():
		if !got.Equal(epoch.Add(10 * time.Second)) {
			t.Fatalf("fired at %v", got)
		}
	default:
		t.Fatal("timer did not fire")
	}
	if f.Waiters() != 0 {
		t.Fatalf("waiters = %d, want 0", f.Waiters())
	}
}

func TestFakeTimerStopAndReset(t *testing.T) {
	f := NewFake(epoch)
	timer := f.NewTimer(time.Minute)
	if !timer.Stop() {
		t.Fatal("Stop on active timer returned false")
	}
	f.Advance(time.Hour)
	select {
	case <-timer.C():
		t.Fatal("stopped timer fired")
	default:
	}
	if timer.Reset(time.Second) {
		t.Fatal("Reset on stopped timer returned true")
	}
	f.Advance(time.Second)
	select {
	case <-timer.C():
	default:
		t.Fatal("reset timer did not fire")
	}
}

func TestFakeTickerDropsWhenFull(t *testing.T) {
	f := NewFake(epoch)
	tk := f.NewTicker(time.Second)
	defer tk.Stop()

	f.Advance(5 * time.Second) // five ticks, channel holds one
	got := <-tk.C()
	if !got.Equal(epoch.Add(time.Second)) {
		t.Fatalf("first tick at %v", got)
	}
	select {
	case <-tk.C():
		t.Fatal("expected dropped ticks")
	default:
	}
	f.Advance(time.Second)
	if got := <-tk.C(); !got.Equal(epoch.Add(6 * time.Second)) {
		t.Fatalf("tick at %v", got)
	}
	if f.Now() != epoch.Add(6*time.Second) {
		t.Fatalf("now = %v", f.Now())
	}
}

func TestFakeBlockUntilWaiters(t *testing.T) {
	f := NewFake(epoch)
	done := make(chan time.Time)
	go func() {
		done <- <-f.After(time.Minute)
	}()
	if err := f.BlockUntilWaiters(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	f.Advance(time.Minute)
	if got := <-done; !got.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("after fired at %v", got)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.BlockUntilWaiters(ctx, 5); err == nil {
		t.Fatal("expected context error")
	}
}

func TestFakeSetBackwardsPanics(t *testing.T) {
	f := NewFake(epoch)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	f.Set(epoch.Add(-time.Second))
}
