// Package throttle limits authentication attempts per client IP and per
// account with golang.org/x/time/rate token buckets (#16, #18).
//
// Credential checks (sign-in, second factors, step-up, invitation and
// reset redemption) call Allow before doing any work and Fail after a
// failed attempt, so only failures consume tokens: a legitimate user who
// types the right password is never slowed down, and an attacker gets at
// most Burst guesses, then Rate per second. Keys come from
// requestinfo.ClientIP (never from forwarding headers) and from the
// normalized account name as typed, whether or not it exists, so the
// limiter itself does not reveal which accounts exist.
package throttle

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/neurekadev/dockyard/internal/clock"
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
	buckets map[string]*rate.Limiter
}

// New returns a limiter. maxEntries bounds memory; when the table is full
// and no bucket is idle (full again), new keys are refused (fail closed).
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
	return &Limiter{limit: limit, clk: clk, maxEntries: maxEntries, buckets: map[string]*rate.Limiter{}}
}

func (l *Limiter) bucket(key string, now time.Time) (*rate.Limiter, bool) {
	if b, ok := l.buckets[key]; ok {
		return b, true
	}
	if !l.hasRoom(now) {
		return nil, false
	}
	b := rate.NewLimiter(rate.Every(l.limit.Every), l.limit.Burst)
	l.buckets[key] = b
	return b, true
}

// hasRoom reports whether a new key fits, evicting idle buckets (full
// again, so forgetting them changes nothing) when the table is full.
func (l *Limiter) hasRoom(now time.Time) bool {
	if len(l.buckets) < l.maxEntries {
		return true
	}
	for k, b := range l.buckets {
		if b.TokensAt(now) >= float64(l.limit.Burst) {
			delete(l.buckets, k)
		}
	}
	return len(l.buckets) < l.maxEntries
}

// Allow reports whether key may attempt now (at least one token left). It
// does not consume a token.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	if b, ok := l.buckets[key]; ok {
		return b.TokensAt(now) >= 1
	}
	return l.hasRoom(now)
}

// Fail consumes one token for key (a failed attempt).
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	if b, ok := l.bucket(key, now); ok {
		b.AllowN(now, 1)
	}
}

// Take consumes one token if available (for flows where every attempt
// counts, e.g. creating ceremony state), reporting whether it did.
func (l *Limiter) Take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	b, ok := l.bucket(key, now)
	return ok && b.AllowN(now, 1)
}

// RetryAfter is how long until key has a token again (0 if it has one).
func (l *Limiter) RetryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	b, ok := l.buckets[key]
	if !ok {
		if !l.hasRoom(now) {
			return l.limit.Every
		}
		return 0
	}
	missing := 1 - b.TokensAt(now)
	if missing <= 0 {
		return 0
	}
	return time.Duration(missing * float64(l.limit.Every))
}

// Reset forgets key (e.g. the account after a successful sign-in).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
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
