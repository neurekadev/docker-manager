package sessions

import (
	"errors"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"
)

// CookieName is the browser session cookie. The __Host- prefix makes
// browsers enforce Secure, Path=/ and no Domain, pinning the cookie to the
// single public origin (#27). api.SessionCookieName is the same value.
const CookieName = "__Host-docker_manager_session"

// Session lifetime defaults (DOCKER_MANAGER_SESSION_IDLE_TIMEOUT and
// DOCKER_MANAGER_SESSION_LIFETIME override them). They follow NIST SP 800-63B
// AAL2 reauthentication guidance: at most one hour of inactivity and a 24
// hour absolute lifetime, whatever the activity.
const (
	DefaultIdleTimeout = time.Hour
	DefaultLifetime    = 24 * time.Hour
)

// Options configures the session manager.
type Options struct {
	Store scs.Store
	// IdleTimeout ends a session after this much inactivity. Negative
	// disables SCS's idle handling (the caller enforces inactivity itself;
	// SCS would otherwise rewrite the session on every request).
	IdleTimeout time.Duration
	// Lifetime is the absolute limit from sign-in (or the last renewal).
	Lifetime time.Duration
	// ErrorFunc answers requests whose session cannot be loaded or saved
	// (database failures). Required: SCS's default writes plain text and
	// logs with the standard logger.
	ErrorFunc func(http.ResponseWriter, *http.Request, error)
}

// NewManager returns an SCS session manager with Docker Manager's cookie policy:
// HttpOnly, Secure, SameSite=Strict, Path=/, no Domain, the __Host- name,
// and tokens stored only as SHA-256 hashes. Handlers must call RenewToken
// on every privilege change (sign-in, second factor, step-up, enrollment
// completion) to prevent session fixation.
func NewManager(o Options) (*scs.SessionManager, error) {
	if o.Store == nil {
		return nil, errors.New("sessions: store is required")
	}
	if o.ErrorFunc == nil {
		return nil, errors.New("sessions: error func is required")
	}
	switch {
	case o.IdleTimeout < 0:
		o.IdleTimeout = 0
	case o.IdleTimeout == 0:
		o.IdleTimeout = DefaultIdleTimeout
	}
	if o.Lifetime <= 0 {
		o.Lifetime = DefaultLifetime
	}
	if o.IdleTimeout > o.Lifetime {
		return nil, errors.New("sessions: idle timeout exceeds the absolute lifetime")
	}
	sm := scs.New()
	sm.Store = o.Store
	sm.IdleTimeout = o.IdleTimeout
	sm.Lifetime = o.Lifetime
	sm.HashTokenInStore = true
	sm.ErrorFunc = o.ErrorFunc
	sm.Cookie = scs.SessionCookie{
		Name:     CookieName,
		HttpOnly: true,
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
		// Always Secure: the public origin is HTTPS, and browsers accept
		// Secure cookies from http://localhost (the only plain-http mode).
		Secure:  true,
		Persist: true,
	}
	return sm, nil
}
