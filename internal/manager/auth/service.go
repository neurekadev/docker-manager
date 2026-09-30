package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/secrets"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Time windows of the identity flows.
const (
	// StepUpWindow is how long a sign-in or step-up counts as "recent
	// authentication" for sensitive changes.
	StepUpWindow = 10 * time.Minute
	// PendingSignInTTL bounds the time between the first and the last
	// factor of a sign-in.
	PendingSignInTTL = 10 * time.Minute
	// TOTPEnrollmentTTL bounds the time to confirm a new TOTP secret.
	TOTPEnrollmentTTL = 10 * time.Minute
	// OwnerRecoveryTTL is the validity of an owner-recovery code.
	OwnerRecoveryTTL = time.Hour
	// StreamSweepInterval is how often open streams are re-checked against
	// account state changed outside this process (owner-recovery CLI).
	StreamSweepInterval = 15 * time.Second
	// RecoveryCodeCount is the size of a recovery code set.
	RecoveryCodeCount = 10
)

// Forgetter drops stored Idempotency-Key responses of a principal
// (idempotency.Store.Forget).
type Forgetter interface {
	Forget(ctx context.Context, principalKey string) (int, error)
}

// ServiceOptions configures the identity service.
type ServiceOptions struct {
	DB               *bun.DB
	Kit              *Kit
	Keyring          *secrets.Keyring
	Logger           *slog.Logger
	PublicURL        *url.URL
	LocalDevelopment bool
	// IdleTimeout and Lifetime bound sessions, StayIdleTimeout and
	// StayLifetime those signed in with "Stay signed in" (checked on the
	// injected clock on every request, in addition to SCS's own expiry;
	// zero: the kit's).
	IdleTimeout     time.Duration
	Lifetime        time.Duration
	StayIdleTimeout time.Duration
	StayLifetime    time.Duration
	// Audit records security events (#30). Nil logs them.
	Audit Auditor
	// Idempotency is told when a principal's sessions or credentials
	// change. Optional.
	Idempotency Forgetter
}

// Service implements the identity flows of #16 on the #18 primitives.
type Service struct {
	db        *bun.DB
	kit       *Kit
	keyring   *secrets.Keyring
	clk       clock.Clock
	log       *slog.Logger
	publicURL *url.URL
	localDev  bool
	idle      time.Duration
	lifetime  time.Duration
	stayIdle  time.Duration
	stayLife  time.Duration
	audit     Auditor
	idem      Forgetter
	hub       *hub
	ownerID   atomic.Pointer[string]
	// scopes validates API token scopes (#31, the permission service).
	scopes atomic.Pointer[ScopeValidator]
}

// NewService returns the identity service and loads the owner (if set up).
func NewService(ctx context.Context, o ServiceOptions) (*Service, error) {
	if o.DB == nil || o.Kit == nil || o.Keyring == nil {
		return nil, errors.New("auth: database, kit and keyring are required")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Audit == nil {
		o.Audit = LogAuditor{Logger: o.Logger}
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = o.Kit.IdleTimeout
	}
	if o.Lifetime <= 0 {
		o.Lifetime = o.Kit.Lifetime
	}
	if o.StayIdleTimeout <= 0 {
		o.StayIdleTimeout = o.Kit.StayIdleTimeout
	}
	if o.StayLifetime <= 0 {
		o.StayLifetime = o.Kit.StayLifetime
	}
	s := &Service{
		db: o.DB, kit: o.Kit, keyring: o.Keyring, clk: o.Kit.Clock, log: o.Logger, publicURL: o.PublicURL,
		localDev: o.LocalDevelopment, idle: o.IdleTimeout, lifetime: o.Lifetime, stayIdle: o.StayIdleTimeout, stayLife: o.StayLifetime,
		audit: o.Audit, idem: o.Idempotency,
		hub: newHub(),
	}
	if id, ok, err := store.OwnerID(ctx, o.DB); err != nil {
		return nil, err
	} else if ok {
		s.ownerID.Store(&id)
	}
	return s, nil
}

func (s *Service) now() time.Time { return s.clk.Now().UTC() }

// Now is the service clock (API response timestamps).
func (s *Service) Now() time.Time { return s.now() }

// isOwner reports whether userID is the instance owner. The owner never
// changes after setup (there is no ownership transfer in v1), so the ID is
// cached.
func (s *Service) isOwner(userID string) bool {
	id := s.ownerID.Load()
	return id != nil && userID != "" && *id == userID
}

// RequireOwner returns the owner's user ID when the request comes from the
// owner's full session; with recent it also demands a sign-in or step-up
// within StepUpWindow. It is the guard of owner-only flows in other
// services (#17 groups and permissions). Errors: domain.ErrNotAuthenticated,
// ErrEnrollmentRequired, ErrForbidden, ErrStepUpRequired.
func (s *Service) RequireOwner(ctx context.Context, recent bool) (string, error) {
	cur, err := s.requireOwner(ctx, recent)
	if err != nil {
		return "", err
	}
	return cur.user.ID, nil
}

// AccessChanged is called after the effective permissions of users may
// have changed (#17: rule edits, group moves): their stored idempotent
// responses are forgotten and their in-flight requests and open streams
// end at once, so nothing keeps the old access; clients reconnect and are
// filtered by the new rules. Sessions stay signed in
// (permissions are evaluated on every request, so no session state holds
// old access). The instance owner is never affected (owner bypass), so the
// owner's own request is not cancelled. The users' API tokens (#31) are
// narrowed the same way: token scope ∩ the new permissions applies to the
// next request, and their open streams and stored responses end too.
func (s *Service) AccessChanged(ctx context.Context, userIDs []string) {
	for _, id := range userIDs {
		if s.isOwner(id) {
			continue
		}
		s.forget(ctx, id)
		s.forgetUserTokens(ctx, id)
		s.hub.revokeUser(id, authz.ErrPermissionsChanged)
	}
}

// principalKey is the idempotency scope of a user (authz.Principal.Key).
func principalKey(userID string) string {
	return authz.Principal{Kind: authz.KindUser, UserID: userID}.Key()
}

// endSessions ends every session and open stream of userID: the session
// epoch was bumped by the caller (or is bumped here when bump is true),
// stored idempotent responses are forgotten and live request contexts are
// cancelled. Call it last: it may cancel the caller's own request context.
func (s *Service) endSessions(ctx context.Context, userID string, bump bool) error {
	ctx = context.WithoutCancel(ctx)
	if bump {
		if _, err := store.BumpSessionEpoch(ctx, s.db, userID, s.now()); err != nil {
			return err
		}
	}
	s.forget(ctx, userID)
	s.hub.revoke(userID, -1)
	return nil
}

func (s *Service) forget(ctx context.Context, userID string) {
	if s.idem == nil {
		return
	}
	if _, err := s.idem.Forget(context.WithoutCancel(ctx), principalKey(userID)); err != nil {
		s.log.Warn("forget idempotent responses", "user_id", userID, "error", err)
	}
}

// RunStreamSweeper re-checks open authenticated requests (streams) every
// StreamSweepInterval and cancels those whose account was disabled,
// deleted or had its sessions revoked, also by another process (the
// owner-recovery CLI). It returns when ctx ends.
func (s *Service) RunStreamSweeper(ctx context.Context) {
	t := s.clk.NewTicker(StreamSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			if err := s.SweepStreams(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("sweep streams", "error", err)
			}
		}
	}
}

// SweepStreams cancels open requests of sessions and API tokens that are
// no longer valid (a token revoked by another process, expired while a
// stream was open, or disabled instance-wide).
func (s *Service) SweepStreams(ctx context.Context) error {
	live, sids, tokens := s.hub.snapshot()
	if len(live) > 0 {
		ids := make([]string, 0, len(live))
		for id := range live {
			ids = append(ids, id)
		}
		states, err := store.SessionEpochs(ctx, s.db, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			st, ok := states[id]
			if !ok || !st.Active {
				s.hub.revoke(id, -1)
				continue
			}
			s.hub.revoke(id, st.Epoch)
		}
	}
	if len(sids) > 0 {
		// Devices signed out by another request (or process) or ended.
		ids := make([]string, 0, len(sids))
		for id := range sids {
			ids = append(ids, id)
		}
		alive, err := store.LiveUserSessionIDs(ctx, s.db, ids)
		if err != nil {
			return err
		}
		var ended []string
		for _, id := range ids {
			if !alive[id] {
				ended = append(ended, id)
			}
		}
		if len(ended) > 0 {
			s.hub.revokeSessions(ended, authz.ErrSessionEnded, 0)
		}
	}
	return s.sweepTokens(ctx, tokens)
}

// OpenStreams is the number of registered authenticated requests (tests,
// diagnostics).
func (s *Service) OpenStreams() int { return s.hub.count() }

func (s *Service) settings(ctx context.Context) (domain.SecuritySettings, error) {
	return store.GetSecuritySettings(ctx, s.db)
}
