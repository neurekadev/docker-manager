package transfer

import (
	"context"
	"sync"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
)

// Limiter caps a transfer's rate (DOCKER_MANAGER_MIGRATION_BANDWIDTH_LIMIT) with a
// token bucket on an injectable clock. The zero rate (or a nil Limiter)
// does not limit. One Limiter may be shared by concurrent transfers; the cap
// then applies to their sum.
type Limiter struct {
	clk   clock.Clock
	rate  float64 // bytes per second
	burst float64

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// MinBurst is the smallest bucket size (bytes): a transfer never waits for
// less than this.
const MinBurst = 64 << 10

// NewLimiter returns a Limiter of bytesPerSecond (<= 0: unlimited, nil).
// The bucket holds a quarter second of traffic (at least MinBurst) and
// starts full.
func NewLimiter(clk clock.Clock, bytesPerSecond int64) *Limiter {
	if bytesPerSecond <= 0 {
		return nil
	}
	if clk == nil {
		clk = clock.Real()
	}
	burst := max(float64(bytesPerSecond)/4, MinBurst)
	return &Limiter{clk: clk, rate: float64(bytesPerSecond), burst: burst, tokens: burst, last: clk.Now()}
}

// Rate returns the cap in bytes per second (0: unlimited).
func (l *Limiter) Rate() int64 {
	if l == nil {
		return 0
	}
	return int64(l.rate)
}

// Wait blocks until n bytes may pass (or ctx ends).
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l == nil {
		return nil
	}
	remaining := float64(n)
	for remaining > 0 {
		take := min(remaining, l.burst)
		l.mu.Lock()
		now := l.clk.Now()
		if el := now.Sub(l.last); el > 0 {
			l.tokens = min(l.burst, l.tokens+el.Seconds()*l.rate)
		}
		l.last = now
		if l.tokens >= take {
			l.tokens -= take
			l.mu.Unlock()
			remaining -= take
			continue
		}
		wait := time.Duration((take - l.tokens) / l.rate * float64(time.Second))
		l.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		t := l.clk.NewTimer(wait)
		select {
		case <-t.C():
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	return nil
}
