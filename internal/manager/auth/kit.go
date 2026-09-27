// Package auth is Docker Manager's identity layer (#16) built on the vetted
// libraries selected in #18 (docs/internal/adr/0003-auth-libraries.md):
//
//	sessions   alexedwards/scs/v2 + a Bun store on the manager database
//	password   alexedwards/argon2id, versioned parameters, blocklist policy
//	totp       pquerna/otp (RFC 6238) + Docker Manager replay/skew handling
//	passkey    go-webauthn/webauthn, RP ID/origin from DOCKER_MANAGER_PUBLIC_URL
//	csrf       net/http CrossOriginProtection
//	throttle   golang.org/x/time/rate per client IP and per account
//
// Kit assembles these primitives from the manager configuration. The
// libraries own the security-sensitive primitives; Docker Manager code owns only
// the product workflows around them (owner, invitations, factor policy,
// recovery, permissions).
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/csrf"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/passkey"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/password"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/sessions"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/throttle"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/auth/totp"
)

// Throttling defaults for credential checks (failed attempts only).
var (
	// PerIP bounds failed attempts from one client IP (IPv6: per /64).
	PerIP = throttle.Limit{Every: 6 * time.Second, Burst: 20}
	// PerAccount bounds failed attempts against one account name, from
	// anywhere: 10 guesses, then one per minute (NIST SP 800-63B allows at
	// most 100 consecutive failures; this is far below).
	PerAccount = throttle.Limit{Every: time.Minute, Burst: 10}
)

// SweepInterval is how often expired sessions are deleted.
const SweepInterval = 15 * time.Minute

// KitOptions configures the primitives.
type KitOptions struct {
	DB        bun.IDB
	Clock     clock.Clock
	Logger    *slog.Logger
	PublicURL *url.URL
	// IdleTimeout and Lifetime bound sessions (zero: sessions defaults).
	IdleTimeout time.Duration
	Lifetime    time.Duration
	// SessionError answers requests whose session cannot be loaded or saved.
	SessionError func(http.ResponseWriter, *http.Request, error)
	// PasswordParams overrides password.Current (tests).
	PasswordParams *password.Params
	// OnPasswordCompute is called for every Argon2id computation (tests).
	OnPasswordCompute func()
}

// Kit holds the configured primitives.
type Kit struct {
	Clock clock.Clock
	// IdleTimeout and Lifetime bound sessions. SCS enforces the lifetime
	// (row expiry, cookie Max-Age); the identity service enforces both on
	// the injected clock at every request (SCS's own idle handling is off:
	// it would rewrite every session on every request).
	IdleTimeout  time.Duration
	Lifetime     time.Duration
	SessionStore *sessions.Store
	Sessions     *scs.SessionManager
	Passwords    *password.Hasher
	RP           *passkey.RelyingParty
	CSRF         *csrf.Guard
	IPLimit      *throttle.Limiter
	AccountLimit *throttle.Limiter
	logger       *slog.Logger
}

// NewKit validates the configuration (a public URL that cannot be a
// WebAuthn relying party fails startup) and builds the primitives.
func NewKit(o KitOptions) (*Kit, error) {
	if o.DB == nil {
		return nil, errors.New("auth: database is required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = sessions.DefaultIdleTimeout
	}
	if o.Lifetime <= 0 {
		o.Lifetime = sessions.DefaultLifetime
	}
	if o.IdleTimeout > o.Lifetime {
		return nil, errors.New("auth: session idle timeout exceeds the lifetime")
	}
	// SCS computes session deadlines from the wall clock, so its store must
	// compare them with the wall clock too. Docker Manager's own idle/lifetime
	// checks (identity service) run on the injected clock.
	store := sessions.NewStore(o.DB, clock.Real())
	sm, err := sessions.NewManager(sessions.Options{
		Store: store, IdleTimeout: -1, Lifetime: o.Lifetime, ErrorFunc: o.SessionError,
	})
	if err != nil {
		return nil, err
	}
	hasher, err := password.NewHasher(password.Options{Params: o.PasswordParams, OnCompute: o.OnPasswordCompute})
	if err != nil {
		return nil, err
	}
	rp, err := passkey.New(o.PublicURL)
	if err != nil {
		return nil, err
	}
	guard, err := csrf.New(o.PublicURL)
	if err != nil {
		return nil, err
	}
	return &Kit{
		Clock: o.Clock, IdleTimeout: o.IdleTimeout, Lifetime: o.Lifetime, SessionStore: store, Sessions: sm, Passwords: hasher, RP: rp, CSRF: guard,
		IPLimit:      throttle.New(PerIP, o.Clock, 50000),
		AccountLimit: throttle.New(PerAccount, o.Clock, 50000),
		logger:       o.Logger,
	}, nil
}

// NewTOTP generates an enrollment secret for account (RFC 6238, see package
// totp). The secret is shown once and stored sealed.
func (k *Kit) NewTOTP(account string) (totp.Key, error) { return totp.Generate(account) }

// VerifyTOTP checks code at the kit's clock, accepting only steps after
// lastStep (replay prevention); the caller persists the returned step.
func (k *Kit) VerifyTOTP(secret, code string, lastStep int64) (int64, bool, error) {
	return totp.Verify(secret, code, k.Clock.Now(), lastStep)
}

// RunHousekeeping deletes expired sessions every SweepInterval until ctx
// ends.
func (k *Kit) RunHousekeeping(ctx context.Context) {
	t := k.Clock.NewTicker(SweepInterval)
	defer t.Stop()
	for {
		k.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C():
		}
	}
}

func (k *Kit) sweep(ctx context.Context) {
	n, err := k.SessionStore.DeleteExpired(ctx)
	switch {
	case err != nil && ctx.Err() == nil:
		k.logger.Warn("delete expired sessions", "error", err)
	case n > 0:
		k.logger.Debug("deleted expired sessions", "count", n)
	}
}
