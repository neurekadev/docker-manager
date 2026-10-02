// Package throttle limits authentication attempts per client IP and per
// account with token buckets (#16, #18).
//
// Credential checks (sign-in, second factors, step-up, invitation and
// reset redemption) Take a token from every bucket they count against
// before doing any work, and Refund it when the attempt succeeds. Only
// failures keep their token, so a legitimate user who types the right
// password is never slowed down, and because the token is taken up front,
// concurrent attempts cannot all pass a check before the first failure is
// counted: an attacker gets at most Burst guesses, then one per Every.
// Keys come from requestinfo.ClientIP (never from forwarding headers) and
// from the normalized account name as typed, whether or not it exists, so
// the limiter itself does not reveal which accounts exist.
package throttle

import (
	"container/heap"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
)

// Limit is a token bucket: Burst attempts, refilled at one per Every.
type Limit struct {
	Every time.Duration
	Burst int
}

// Limiter is a bounded table of token buckets keyed by string. Safe for
// concurrent use.
type Limiter struct {
	limit      Limit
	clk        clock.Clock
	maxEntries int

	mu      sync.Mutex
	buckets map[string]*bucket
	byFull  fullHeap // the bucket that is full again soonest first
}

type bucket struct {
	key    string
	tokens float64
	at     time.Time
	// fullAt is when the bucket is full again without further use; index
	// is its place in byFull.
	fullAt time.Time
	index  int
}

// New returns a limiter. maxEntries bounds memory: when the table is full,
// a new key forgets the bucket that is full again soonest: one that has
// refilled already (forgetting it changes nothing), else the one closest
// to it. A key under attack holds more failures than made-up keys used to
// fill the table, so they are forgotten first and cannot reset its limit.
// New keys are never refused, so a flood never locks out other clients.
func New(limit Limit, clk clock.Clock, maxEntries int) *Limiter {
	if clk == nil {
		clk = clock.Real()
	}
	if maxEntries <= 0 {
		maxEntries = 10000
	}
	if limit.Burst <= 0 {
		limit.Burst = 1
	}
	if limit.Every <= 0 {
		limit.Every = time.Second
	}
	return &Limiter{limit: limit, clk: clk, maxEntries: maxEntries, buckets: map[string]*bucket{}}
}

// bucket returns key's bucket refilled to now, creating (and evicting for)
// it when needed. The caller holds l.mu and calls l.update after changing
// the tokens.
func (l *Limiter) bucket(key string, now time.Time) *bucket {
	if b, ok := l.buckets[key]; ok {
		if d := now.Sub(b.at); d > 0 {
			b.tokens = min(float64(l.limit.Burst), b.tokens+float64(d)/float64(l.limit.Every))
			b.at = now
		}
		return b
	}
	for len(l.buckets) >= l.maxEntries {
		l.drop(l.byFull[0])
	}
	b := &bucket{key: key, tokens: float64(l.limit.Burst), at: now, fullAt: now}
	l.buckets[key] = b
	heap.Push(&l.byFull, b)
	return b
}

// update reorders b after its tokens changed. The caller holds l.mu.
func (l *Limiter) update(b *bucket) {
	b.fullAt = b.at.Add(time.Duration((float64(l.limit.Burst) - b.tokens) * float64(l.limit.Every)))
	heap.Fix(&l.byFull, b.index)
}

// drop forgets b. The caller holds l.mu.
func (l *Limiter) drop(b *bucket) {
	heap.Remove(&l.byFull, b.index)
	delete(l.buckets, b.key)
}

// Take consumes one token for key if one is available, reporting whether
// it did. Credential checks call it before verifying anything.
func (l *Limiter) Take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.bucket(key, l.clk.Now())
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	l.update(b)
	return true
}

// Refund gives back a token Take consumed (the attempt succeeded, or ended
// before any credential was checked). The bucket never exceeds Burst; a
// bucket forgotten meanwhile is not recreated.
func (l *Limiter) Refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.buckets[key]; !ok {
		return
	}
	b := l.bucket(key, l.clk.Now())
	b.tokens = min(float64(l.limit.Burst), b.tokens+1)
	l.update(b)
}

// RetryAfter is how long until key has a token again (0 if it has one).
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		return 0
	}
	tokens := min(float64(l.limit.Burst), b.tokens+float64(l.clk.Now().Sub(b.at))/float64(l.limit.Every))
	if missing := 1 - tokens; missing > 0 {
		return time.Duration(missing * float64(l.limit.Every))
	}
	return 0
}

// Reset forgets key (e.g. the account and client after a successful
// sign-in).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[key]; ok {
		l.drop(b)
	}
}

// fullHeap orders buckets by fullAt (container/heap).
type fullHeap []*bucket

func (h fullHeap) Len() int           { return len(h) }
func (h fullHeap) Less(i, j int) bool { return h[i].fullAt.Before(h[j].fullAt) }
func (h fullHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *fullHeap) Push(x any) {
	b := x.(*bucket)
	b.index = len(*h)
	*h = append(*h, b)
}
func (h *fullHeap) Pop() any {
	old := *h
	b := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	return b
}

// IPKey groups a client address: IPv4 by address, IPv6 by /64 (one
// customer network), so rotating addresses inside a /64 does not escape
// the limit. An invalid address (no request info) shares one bucket.
func IPKey(a netip.Addr) string {
	if !a.IsValid() {
		return "ip:unknown"
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return "ip:" + p.String()
	}
	return "ip:" + a.String()
}

// AccountKey is the bucket of an account name as typed (trimmed, case-folded).
func AccountKey(name string) string {
	return "account:" + strings.ToLower(strings.TrimSpace(name))
}

// AccountClientKey is the bucket of an account name as typed from one
// client (IPKey grouping): the strict sign-in limit, so failures from one
// client never lock the account out for everyone else.
func AccountClientKey(name string, client netip.Addr) string {
	return AccountKey(name) + "|" + IPKey(client)
}
