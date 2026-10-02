package throttle

import (
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/neurekadev/docker-manager/internal/testutil"
)

func TestFailuresConsumeAndRefill(t *testing.T) {
	clk := testutil.FakeClock()
	l := New(Limit{Every: time.Minute, Burst: 3}, clk, 100)
	k := AccountKey(" Alice ")
	if k != AccountKey("alice") {
		t.Fatal("account keys are not normalized")
	}
	for i := range 3 {
		if !l.Take(k) {
			t.Fatalf("attempt %d refused", i)
		}
	}
	if l.Take(k) {
		t.Fatal("fourth attempt allowed within the burst window")
	}
	if ra := l.RetryAfter(k); ra <= 0 || ra > time.Minute {
		t.Fatalf("retry after %v", ra)
	}
	clk.Advance(time.Minute)
	if l.RetryAfter(k) != 0 || !l.Take(k) {
		t.Fatal("token not refilled after one period")
	}
	if l.Take(k) {
		t.Fatal("refilled token not consumed")
	}
	l.Reset(k)
	if !l.Take(k) {
		t.Fatal("reset did not clear the bucket")
	}
	// Other keys are independent.
	if !l.Take(AccountKey("bob")) {
		t.Fatal("unrelated key limited")
	}
}

// TestRefundRestoresSuccessfulAttempts: a successful attempt gives its
// token back (only failures count), never above the burst.
func TestRefundRestoresSuccessfulAttempts(t *testing.T) {
	l := New(Limit{Every: time.Hour, Burst: 2}, testutil.FakeClock(), 10)
	for range 5 {
		if !l.Take("k") {
			t.Fatal("a refunded attempt was counted")
		}
		l.Refund("k")
	}
	l.Refund("k") // at the burst already
	taken := 0
	for range 3 {
		if l.Take("k") {
			taken++
		}
	}
	if taken != 2 {
		t.Fatalf("%d attempts after the refunds, want exactly the burst (2)", taken)
	}
	l.Refund("unknown") // never seen: nothing to restore, no bucket created
	if l.RetryAfter("unknown") != 0 {
		t.Fatal("refund of an unknown key")
	}
}

// TestConcurrentTakesStayWithinTheBurst: the token is taken before the
// (slow) credential check, so parallel attempts cannot all pass while the
// first ones are still being verified.
func TestConcurrentTakesStayWithinTheBurst(t *testing.T) {
	l := New(Limit{Every: time.Hour, Burst: 5}, testutil.FakeClock(), 10)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	for range 200 {
		wg.Go(func() {
			if l.Take("k") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if allowed != 5 {
		t.Fatalf("%d of 200 concurrent attempts allowed, want the burst (5)", allowed)
	}
}

// TestFullTableForgetsTheBucketClosestToFull: when the table is full a
// new key still gets a bucket (never fail closed), and the bucket that is
// full again soonest is forgotten, so a flood of made-up keys with one
// failure each never resets a key that holds more failures.
func TestFullTableForgetsTheBucketClosestToFull(t *testing.T) {
	clk := testutil.FakeClock()
	l := New(Limit{Every: time.Minute, Burst: 3}, clk, 3)
	for range 3 {
		l.Take("target") // drained: full again in 3 minutes
	}
	l.Take("idle")
	clk.Advance(time.Minute) // idle is full again
	l.Take("other")          // full again in 1 minute
	if !l.Take("new") {
		t.Fatal("full table refused a new key")
	}
	if l.RetryAfter("idle") != 0 || !l.Take("other") {
		t.Fatal("a bucket other than the refilled one was forgotten")
	}
	for i := range 1000 { // made-up keys, one failure each
		if !l.Take(fmt.Sprintf("junk-%d", i)) {
			t.Fatalf("junk key %d refused", i)
		}
	}
	if l.Take("target") {
		t.Fatal("made-up keys reset the drained bucket")
	}
	if len(l.buckets) != 3 || len(l.byFull) != 3 {
		t.Fatalf("table holds %d buckets (%d in the heap), want the bound 3", len(l.buckets), len(l.byFull))
	}
}

func TestAccountClientKey(t *testing.T) {
	a := netip.MustParseAddr("192.0.2.1")
	if AccountClientKey(" Alice ", a) != AccountClientKey("alice", a) {
		t.Fatal("account client keys are not normalized")
	}
	if AccountClientKey("alice", a) == AccountClientKey("alice", netip.MustParseAddr("192.0.2.2")) ||
		AccountClientKey("alice", a) == AccountKey("alice") {
		t.Fatal("account client keys must differ per client and from the account key")
	}
}

func TestIPKey(t *testing.T) {
	a := netip.MustParseAddr("2001:db8:1:2:aaaa::1")
	b := netip.MustParseAddr("2001:db8:1:2:bbbb::2")
	c := netip.MustParseAddr("2001:db8:1:3::1")
	if IPKey(a) != IPKey(b) || IPKey(a) == IPKey(c) {
		t.Fatalf("IPv6 /64 grouping: %s %s %s", IPKey(a), IPKey(b), IPKey(c))
	}
	if IPKey(netip.MustParseAddr("::ffff:192.0.2.1")) != IPKey(netip.MustParseAddr("192.0.2.1")) || IPKey(netip.MustParseAddr("192.0.2.1")) == IPKey(netip.MustParseAddr("192.0.2.2")) {
		t.Fatal("IPv4 keys")
	}
	if IPKey(netip.Addr{}) != "ip:unknown" {
		t.Fatal("invalid address key")
	}
}
