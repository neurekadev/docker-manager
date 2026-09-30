package events

import (
	"testing"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestBusOrderSeqAndBoundedDelivery(t *testing.T) {
	clk := testutil.FakeClock()
	b := New(clk)
	all := b.Subscribe(10, nil)
	small := b.Subscribe(2, nil)
	envOnly := b.Subscribe(10, func(e Event) bool { return e.EnvironmentID == "e1" })
	for i := range 5 {
		env := "e1"
		if i%2 == 1 {
			env = "e2"
		}
		got := b.Publish(Event{Type: EnvironmentOnline, EnvironmentID: env})
		if got.Seq != uint64(i+1) || !got.At.Equal(testutil.Epoch) {
			t.Fatalf("publish %d: %+v", i, got)
		}
	}
	if b.Seq() != 5 {
		t.Fatalf("seq %d", b.Seq())
	}
	for want := uint64(1); want <= 5; want++ {
		if e := <-all.C(); e.Seq != want {
			t.Fatalf("all: seq %d, want %d", e.Seq, want)
		}
	}
	// The small subscriber got 1 and 2, then dropped 3..5: the next event
	// it sees reveals the gap through its sequence number.
	if a, b2 := <-small.C(), <-small.C(); a.Seq != 1 || b2.Seq != 2 || small.Dropped() != 3 {
		t.Fatalf("small: %d %d dropped %d", a.Seq, b2.Seq, small.Dropped())
	}
	b.Publish(Event{Type: EnvironmentOffline, EnvironmentID: "e1"})
	if e := <-small.C(); e.Seq != 6 {
		t.Fatalf("after drops: %d", e.Seq)
	}
	// Filtered delivery: only e1 events (1, 3, 5, 6), filtered ones are not drops.
	for _, want := range []uint64{1, 3, 5, 6} {
		if e := <-envOnly.C(); e.Seq != want {
			t.Fatalf("filtered: %d, want %d", e.Seq, want)
		}
	}
	if envOnly.Dropped() != 0 {
		t.Fatal("filtered events counted as drops")
	}
	if e := <-all.C(); e.Seq != 6 {
		t.Fatalf("all: %d", e.Seq)
	}
	all.Close()
	b.Publish(Event{Type: EnvironmentOnline})
	select {
	case e := <-all.C():
		t.Fatalf("closed subscription received %+v", e)
	default:
	}
	var nilBus *Bus
	if e := nilBus.Publish(Event{Type: "x"}); e.Seq != 0 {
		t.Fatal("nil bus assigned a sequence")
	}
}
