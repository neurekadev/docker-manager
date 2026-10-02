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
	"container/list"
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
	hardMax    int

	mu      sync.Mutex
	buckets map[string]*list.Element // of *bucket
	lru     *list.List               // least recently used first
}

type bucket struct {
	key    string
	tokens float64
	at     time.Time
}

// evictScan bounds how many of the least recently used buckets a full
// table looks at for one that has refilled.
const evictScan = 64

// New returns a limiter. maxEntries bounds memory: when the table is full,
// a new key forgets a bucket that has refilled (forgetting it changes
// nothing), looking at the evictScan least recently used ones. Buckets
// that still hold failures are kept up to four times maxEntries, so
// flooding the table with made-up keys does not reset the limit of a key
// under attack; only past that is the least recently used one forgotten
// (its key gets a full burst again). New keys are never refused, so a
// flood never locks out other clients.
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
	return &Limiter{limit: limit, clk: clk, maxEntries: maxEntries, hardMax: 4 * maxEntries, buckets: map[string]*list.Element{}, lru: list.New()}
}

// bucket returns key's bucket refilled to now, creating (and evicting for)
// it when needed. The caller holds l.mu.
func (l *Limiter) bucket(key string, now time.Time) *bucket {
	if e, ok := l.buckets[key]; ok {
		l.lru.MoveToBack(e)
		b := e.Value.(*bucket)
		if d := now.Sub(b.at); d > 0 {
			b.tokens = min(float64(l.limit.Burst), b.tokens+float64(d)/float64(l.limit.Every))
			b.at = now
		}
		return b
	}
	for len(l.buckets) >= l.maxEntries {
		if e := l.refilled(now); e != nil {
			l.drop(e)
			continue
		}
		if len(l.buckets) < l.hardMax {
			break
		}
		l.drop(l.lru.Front())
	}
	b := &bucket{key: key, tokens: float64(l.limit.Burst), at: now}
	l.buckets[key] = l.lru.PushBack(b)
	return b
}

// refilled returns one of the evictScan least recently used buckets that
// is full again, or nil. The caller holds l.mu.
func (l *Limiter) refilled(now time.Time) *list.Element {
	e := l.lru.Front()
	for i := 0; e != nil && i < evictScan; i, e = i+1, e.Next() {
		b := e.Value.(*bucket)
		if b.tokens+float64(now.Sub(b.at))/float64(l.limit.Every) >= float64(l.limit.Burst) {
			return e
		}
	}
	return nil
}

// drop forgets e's bucket. The caller holds l.mu.
func (l *Limiter) drop(e *list.Element) {
	l.lru.Remove(e)
	delete(l.buckets, e.Value.(*bucket).key)
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
}

// RetryAfter is how long until key has a token again (0 if it has one).
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.buckets[key]
	if !ok {
		return 0
	}
	b := e.Value.(*bucket)
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
	if e, ok := l.buckets[key]; ok {
		l.drop(e)
	}
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
