package registries

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/uptrace/bun"

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

// connectionOf returns the connection ID and secret version of a regclient
// credential key ("<connectionId>/<secretVersion>"; "" and 0 for anonymous
// access, version 0 when the key has none).
func connectionOf(credentialKey string) (string, int) {
	id, v, _ := strings.Cut(credentialKey, "/")
	version, err := strconv.Atoi(v)
	if err != nil {
		version = 0
	}
	return id, version
}

// same reports whether two observations say the same about the limit.
func same(a, b regclient.RateLimit) bool {
	return a.Limit == b.Limit && a.Remaining == b.Remaining && a.Window == b.Window && a.Limited == b.Limited
}

// observeRateLimit records a registry's answer to a check: when it says
// something new about the limit, else at most once a minute. A failed write
// is logged and never fails the check. An answer to a connection's
// credential is recorded only while that credential is the connection's
// current one (a check still in flight after a rotation or deletion must
// not bring its rows back), and only when it is not older than the stored
// answer.
func (s *Service) observeRateLimit(ctx context.Context, rl regclient.RateLimit) {
	if rl.Host == "" {
		return
	}
	connID, version := connectionOf(rl.CredentialKey)
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
	// One transaction (BEGIN IMMEDIATE on the single connection): the
	// credential check and the write are serialized with Rotate and Delete,
	// so a stale answer can never be written after its rows were removed.
	applied := false
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if connID != "" {
			current, err := currentCredential(ctx, tx, connID, version)
			if err != nil || !current {
				return err
			}
		}
		var err error
		applied, err = store.RecordRegistryPullLimit(ctx, tx, pullLimitOf(rl, connID))
		return err
	})
	if err != nil {
		s.opts.Logger.Warn("could not record a registry pull limit", "registry", rl.Host, "registry_connection_id", connID, "error", err)
	}
	if err != nil || !applied {
		// Not stored (failed, or a newer answer is stored): the next answer
		// must not be held back by this one.
		s.forgetWrite(key, rl.At)
	}
}

// currentCredential reports whether a connection exists, is active and its
// secret version is version.
func currentCredential(ctx context.Context, db bun.IDB, connID string, version int) (bool, error) {
	c, err := store.GetRegistryConnection(ctx, db, connID)
	if errors.Is(err, domain.ErrRegistryConnectionNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return c.Active() && c.SecretVersion == version, nil
}

// forgetWrite drops the remembered write of key made for the answer at at
// (a later answer's write stays).
func (s *Service) forgetWrite(key string, at time.Time) {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	if w, ok := s.limits[key]; ok && w.at.Equal(at) {
		delete(s.limits, key)
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
