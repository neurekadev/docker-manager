package registries

import (
	"context"
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/regclient"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Pull limits (#217): every answer a registry gives the manager's checks
// (regclient's rate-limit observer) is kept per registry host and
// credential, anonymous access apart from each connection. Pulls on the
// hosts go through the Docker Engine, so the numbers are what the registry
// reported to the manager's checks.

// limitWriteEvery is how often an unchanged pull limit is written again
// (its checked time).
const limitWriteEvery = time.Minute

// limitWriteTimeout bounds one write (it never fails a check).
const limitWriteTimeout = 5 * time.Second

// maxLimitMemory bounds the remembered writes (hosts × credentials).
const maxLimitMemory = 4096

// limitWrite is the last written observation of a host and credential.
type limitWrite struct {
	at  time.Time
	sig regclient.RateLimit
}

// connectionOf returns the connection ID of a regclient credential key
// ("<connectionId>/<secretVersion>", "" for anonymous access).
func connectionOf(credentialKey string) string {
	id, _, _ := strings.Cut(credentialKey, "/")
	return id
}

// same reports whether two observations say the same about the limit.
func same(a, b regclient.RateLimit) bool {
	return a.Limit == b.Limit && a.Remaining == b.Remaining && a.Window == b.Window && a.Limited == b.Limited
}

// observeRateLimit records a registry's answer to a check: when it says
// something new about the limit, else at most once a minute. A failed write
// is logged and never fails the check.
func (s *Service) observeRateLimit(ctx context.Context, rl regclient.RateLimit) {
	if rl.Host == "" {
		return
	}
	connID := connectionOf(rl.CredentialKey)
	key := rl.Host + "\x00" + connID
	sig := regclient.RateLimit{Limit: rl.Limit, Remaining: rl.Remaining, Window: rl.Window, Limited: rl.Limited}
	s.limitsMu.Lock()
	last, ok := s.limits[key]
	if ok && same(last.sig, sig) && rl.At.Sub(last.at) < limitWriteEvery {
		s.limitsMu.Unlock()
		return
	}
	if len(s.limits) >= maxLimitMemory {
		clear(s.limits)
	}
	s.limits[key] = limitWrite{at: rl.At, sig: sig}
	s.limitsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limitWriteTimeout)
	defer cancel()
	if err := store.RecordRegistryPullLimit(ctx, s.db, pullLimitOf(rl, connID)); err != nil {
		// Forget it so the next answer tries again.
		s.limitsMu.Lock()
		delete(s.limits, key)
		s.limitsMu.Unlock()
		s.opts.Logger.Warn("could not record a registry pull limit", "registry", rl.Host, "registry_connection_id", connID, "error", err)
	}
}

// pullLimitOf turns one observation into the stored values.
func pullLimitOf(rl regclient.RateLimit, connID string) domain.RegistryPullLimit {
	at := rl.At.UTC()
	p := domain.RegistryPullLimit{Host: rl.Host, ConnectionID: connID, CheckedAt: at}
	var resetAt *time.Time
	if !rl.ResetAt.IsZero() {
		t := rl.ResetAt.UTC()
		resetAt = &t
	}
	if rl.Reported() {
		limit := rl.Limit
		p.Limit, p.Window, p.ResetAt, p.ObservedAt = &limit, rl.Window, resetAt, &at
		if rl.Remaining >= 0 {
			remaining := rl.Remaining
			p.Remaining = &remaining
		}
	}
	if rl.Limited {
		p.LastLimitedAt = &at
		switch {
		case rl.RetryAfter > 0:
			until := at.Add(rl.RetryAfter)
			p.LimitedUntil = &until
		case resetAt != nil:
			p.LimitedUntil = resetAt
		}
	}
	return p
}

// forgetPullLimits drops a connection's pull limits (deleted, or another
// credential that may be another account).
func (s *Service) forgetPullLimits(ctx context.Context, connID string) {
	s.limitsMu.Lock()
	for k := range s.limits {
		if _, id, _ := strings.Cut(k, "\x00"); id == connID {
			delete(s.limits, k)
		}
	}
	s.limitsMu.Unlock()
	if err := store.DeleteRegistryPullLimits(ctx, s.db, connID); err != nil {
		s.opts.Logger.Warn("could not remove the pull limits of a registry connection", "registry_connection_id", connID, "error", err)
	}
}

// PullLimits returns the last pull limit every registry reported, per host
// and credential (anonymous access has no connection ID). Callers shape
// the list: a connection's limit belongs to its readers.
func (s *Service) PullLimits(ctx context.Context) ([]domain.RegistryPullLimit, error) {
	return store.ListRegistryPullLimits(ctx, s.db)
}
