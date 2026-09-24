package throttle

import (
	"net/netip"
	"testing"
	"time"

	"github.com/neurekadev/dockyard/internal/testutil"
)

func TestFailuresConsumeAndRefill(t *testing.T) {
	clk := testutil.FakeClock()
	l := New(Limit{Every: time.Minute, Burst: 3}, clk, 100)
	k := AccountKey(" Alice ")
	if k != AccountKey("alice") {
		t.Fatal("account keys are not normalized")
	}
	for i := range 3 {
		if !l.Allow(k) {
			t.Fatalf("attempt %d refused", i)
		}
		l.Fail(k)
	}
	if l.Allow(k) {
		t.Fatal("fourth attempt allowed within the burst window")
	}
	if ra := l.RetryAfter(k); ra <= 0 || ra > time.Minute {
		t.Fatalf("retry after %v", ra)
	}
	clk.Advance(time.Minute)
	if !l.Allow(k) || l.RetryAfter(k) != 0 {
		t.Fatal("token not refilled after one period")
	}
	l.Fail(k)
	if l.Allow(k) {
		t.Fatal("refilled token not consumed")
	}
	l.Reset(k)
	if !l.Allow(k) {
		t.Fatal("reset did not clear the bucket")
	}
	// Other keys are independent.
	if !l.Allow(AccountKey("bob")) {
		t.Fatal("unrelated key limited")
	}
}

func TestAllowDoesNotConsume(t *testing.T) {
	l := New(Limit{Every: time.Hour, Burst: 1}, testutil.FakeClock(), 10)
	for range 5 {
		if !l.Allow("k") {
			t.Fatal("Allow consumed a token")
		}
	}
	if !l.Take("k") || l.Take("k") {
		t.Fatal("Take must consume exactly the available token")
	}
}

// TestTableBoundFailsClosed: when every tracked key is still limited, new
// keys are refused instead of growing the table without bound; idle
// buckets are evicted to make room.
func TestTableBoundFailsClosed(t *testing.T) {
	clk := testutil.FakeClock()
	l := New(Limit{Every: time.Minute, Burst: 1}, clk, 2)
	l.Fail("a")
	l.Fail("b")
	if l.Allow("c") || l.Take("c") || l.RetryAfter("c") == 0 {
		t.Fatal("full table admitted a new key")
	}
	clk.Advance(time.Minute) // a and b are full again: evictable
	if !l.Allow("c") {
		t.Fatal("idle buckets were not evicted")
	}
	l.Fail("c")
	if l.Allow("c") {
		t.Fatal("c not limited after its failure")
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
