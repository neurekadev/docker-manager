package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/clock"
	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
	"code.neureka.dev/docker-manager/docker-manager/internal/logging"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/api"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authsep"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/authz"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/requestinfo"
	"code.neureka.dev/docker-manager/docker-manager/internal/manager/store"
)

// Session data keys (SCS, gob-encoded basic types only).
const (
	kUserID   = "uid"
	kEpoch    = "epoch"
	kStage    = "stage"
	kProven   = "proven"
	kAuthAt   = "auth_at"
	kStepUpAt = "stepup_at"
	// kSessionID is the signed-in device (user_sessions row) of the session.
	kSessionID = "sid"
	// kSeenAt is the last activity of a session made before signed-in
	// devices existed (read once, when the session is adopted).
	kSeenAt    = "seen_at"
	kPendingID = "p_uid"
	kPendingAt = "p_at"
	kPendingPf = "p_proven"
	// kPendingStay is the "Stay signed in" choice of a pending sign-in.
	kPendingStay = "p_stay"
	kWAReg       = "wa_reg"
	kWARegName   = "wa_reg_name"
	kWALogin     = "wa_login"
	kWAPurpose   = "wa_purpose"
)

// seenGranularity bounds how often the last-activity time is written.
const seenGranularity = time.Minute

// current is the validated session of a request.
type current struct {
	user     domain.User
	stage    domain.SessionStage
	proven   factorSet
	authAt   time.Time
	stepUpAt time.Time
	// session is the signed-in device.
	session domain.UserSession
}

type currentKey struct{}

// tokenPrincipalKey carries an authenticated API-token principal from the
// bearer check to the inner handler.
type tokenPrincipalKey struct{}

func currentFrom(ctx context.Context) *current {
	c, _ := ctx.Value(currentKey{}).(*current)
	return c
}

func unixNano(n int64) time.Time { return time.Unix(0, n).UTC() }

// enrollmentPaths are the /api/v1 routes a limited enrollment session may
// use: its own account, sign-in and factor enrollment. Everything else
// answers 403 enrollment_required, so an account that still owes factors
// cannot browse resources.
func enrollmentAllowed(path string) bool {
	p := strings.TrimPrefix(path, api.BasePath)
	switch {
	case p == "/me", p == "/me/password", p == "/me/recovery-codes",
		p == "/me/passkeys", strings.HasPrefix(p, "/me/passkeys/"),
		strings.HasPrefix(p, "/auth/"),
		p == "/health", p == "/health/ready", p == "/capabilities", p == "/setup/status",
		strings.HasPrefix(p, "/openapi"):
		return true
	}
	return false
}

// Middleware authenticates /api/v1 requests. It
//
//   - ignores cookies on bearer-token requests (API tokens, #31);
//   - rejects cross-origin browser requests with unsafe methods (csrf);
//   - loads the SCS session and validates it against the account on every
//     request: the account exists and is active, the session's epoch is
//     the account's current one (disable, credential changes and
//     revocations bump it), and neither the idle timeout nor the absolute
//     lifetime passed (on the injected clock). Invalid sessions are
//     destroyed and the request continues anonymously (401 from the route);
//   - sets the authz principal for fully authenticated sessions only, and
//     answers 403 enrollment_required for limited enrollment sessions
//     outside the enrollment routes;
//   - registers the request context so ending the account's sessions also
//     ends its in-flight requests and streams;
//   - authenticates "Authorization: Bearer dy_…" API tokens (#31): the
//     Cookie header is dropped first (a bearer request is never also a
//     session request), CSRF checks do not apply, any token that does not
//     authenticate is refused with the same generic 401, and a valid one
//     sets an api_token principal (the token's user) registered under the
//     token so revoking it closes its streams.
func (s *Service) Middleware(next http.Handler) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if p, ok := ctx.Value(tokenPrincipalKey{}).(authz.Principal); ok {
			ctx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			defer s.hub.registerToken(p.UserID, p.TokenID, cancel)()
			ctx, err := authz.WithPrincipal(ctx, p)
			if err != nil {
				api.WriteError(w, r, api.Internal(err))
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		cur, err := s.resolve(ctx)
		if err != nil {
			api.WriteError(w, r, api.Internal(err))
			return
		}
		if cur == nil {
			next.ServeHTTP(w, r)
			return
		}
		if cur.stage == domain.StageEnrollment && !enrollmentAllowed(r.URL.Path) {
			api.WriteError(w, r, api.NewError(http.StatusForbidden, api.CodeEnrollmentRequired,
				"finish enrolling the sign-in factors the instance policy requires before using Docker Manager"))
			return
		}
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		entry, done := s.hub.register(cur.user.ID, cur.user.SessionEpoch, cur.session.ID, cancel)
		defer done()
		ctx = context.WithValue(context.WithValue(ctx, hubEntryKey{}, entry), currentKey{}, cur)
		if cur.stage == domain.StageAuthenticated {
			if ctx, err = authz.WithPrincipal(ctx, authz.Principal{Kind: authz.KindUser, UserID: cur.user.ID}); err != nil {
				api.WriteError(w, r, api.Internal(err))
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
	loaded := s.kit.Sessions.LoadAndSave(inner)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := authsep.BearerToken(r.Header); ok {
			// Bearer requests are never authenticated by cookies (and so
			// need no CSRF protection).
			r.Header.Del("Cookie")
			p, err := s.AuthenticateAPIToken(r.Context(), tok)
			if errors.Is(err, domain.ErrAPITokenInvalid) {
				logging.FromContext(r.Context()).Info("API token refused",
					slog.String("method", r.Method), slog.String("path", r.URL.Path))
				api.WriteError(w, r, api.Unauthenticated("the API token is not valid").WithHeader("WWW-Authenticate", `Bearer error="invalid_token"`))
				return
			}
			if err != nil {
				api.WriteError(w, r, api.Internal(err))
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), tokenPrincipalKey{}, p))
		} else if err := s.kit.CSRF.Check(r); err != nil {
			logging.FromContext(r.Context()).Warn("cross-origin request rejected",
				slog.String("method", r.Method), slog.String("path", r.URL.Path))
			api.WriteError(w, r, api.NewError(http.StatusForbidden, api.CodeCrossOriginRequest,
				"cross-origin requests are not allowed; use Docker Manager from its own origin"))
			return
		}
		loaded.ServeHTTP(w, r)
	})
}

// resolve validates the request's session. It returns nil for anonymous
// and pending sessions.
func (s *Service) resolve(ctx context.Context) (*current, error) {
	sm := s.kit.Sessions
	now := s.now()
	if sm.Exists(ctx, kPendingID) && now.Sub(unixNano(sm.GetInt64(ctx, kPendingAt))) >= PendingSignInTTL {
		s.clearPending(ctx)
	}
	uid := sm.GetString(ctx, kUserID)
	if uid == "" {
		return nil, nil
	}
	user, err := store.GetUser(ctx, s.db, uid)
	if errors.Is(err, domain.ErrUserNotFound) {
		return nil, s.drop(ctx)
	}
	if err != nil {
		return nil, err
	}
	stage := domain.SessionStage(sm.GetString(ctx, kStage))
	if !user.Active() || user.SessionEpoch != sm.GetInt64(ctx, kEpoch) ||
		(stage != domain.StageAuthenticated && stage != domain.StageEnrollment) {
		return nil, s.drop(ctx)
	}
	authAt := unixNano(sm.GetInt64(ctx, kAuthAt))
	sess, ok, err := s.device(ctx, user, authAt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, s.drop(ctx)
	}
	idle, lifetime := s.limits(sess.StaySignedIn)
	if now.Sub(authAt) >= lifetime || now.Sub(sess.LastSeenAt) >= idle {
		return nil, s.drop(ctx)
	}
	if !sess.StaySignedIn && sm.GetBool(ctx, scsRememberKey) {
		// The owner no longer allows "Stay signed in": the cookie ends with
		// the browser again and the session gets the normal deadline.
		sm.RememberMe(ctx, false)
		s.applyDeadline(ctx, authAt, false)
	}
	if now.Sub(sess.LastSeenAt) >= seenGranularity {
		ip := clientIP(ctx)
		if err := store.TouchUserSession(ctx, s.db, sess.ID, now, ip); err != nil {
			logging.FromContext(ctx).Warn("record session activity", slog.String("error", err.Error()))
		} else {
			sess.LastSeenAt, sess.IP = now, ip
		}
	}
	return &current{
		user: user, stage: stage, proven: factorSet(sm.GetInt64(ctx, kProven)), //nolint:gosec // small bit set
		authAt: authAt, stepUpAt: unixNano(sm.GetInt64(ctx, kStepUpAt)), session: sess,
	}, nil
}

// scsRememberKey is where SCS keeps the RememberMe choice (scs v2.9.0).
const scsRememberKey = "__rememberMe"

// device returns the signed-in device of the request's session. ok is
// false when the session was signed out: its device is gone, belongs to
// someone else or to an ended session epoch. A session made before
// signed-in devices existed is adopted: it gets a device with the normal
// limits.
func (s *Service) device(ctx context.Context, user domain.User, authAt time.Time) (domain.UserSession, bool, error) {
	sm := s.kit.Sessions
	sid := sm.GetString(ctx, kSessionID)
	if sid == "" {
		seenAt := unixNano(sm.GetInt64(ctx, kSeenAt))
		if s.now().Sub(authAt) >= s.lifetime || s.now().Sub(seenAt) >= s.idle {
			return domain.UserSession{}, false, nil
		}
		sess := domain.UserSession{
			ID: newID(), UserID: user.ID, Epoch: user.SessionEpoch, CreatedAt: authAt, LastSeenAt: seenAt,
			IP: clientIP(ctx), UserAgent: requestinfo.UserAgent(ctx),
		}
		if err := store.InsertUserSession(ctx, s.db, sess); err != nil {
			return domain.UserSession{}, false, err
		}
		sm.Put(ctx, kSessionID, sess.ID)
		sm.Remove(ctx, kSeenAt)
		return sess, true, nil
	}
	sess, err := store.GetUserSession(ctx, s.db, sid)
	if errors.Is(err, domain.ErrUserSessionNotFound) {
		return domain.UserSession{}, false, nil
	}
	if err != nil {
		return domain.UserSession{}, false, err
	}
	return sess, sess.UserID == user.ID && sess.Epoch == user.SessionEpoch, nil
}

// limits returns the idle timeout and absolute lifetime of a session.
func (s *Service) limits(stay bool) (idle, lifetime time.Duration) {
	if stay {
		return s.stayIdle, s.stayLife
	}
	return s.idle, s.lifetime
}

// fill sets the expiry times of a signed-in device.
func (s *Service) fill(sess *domain.UserSession) {
	idle, lifetime := s.limits(sess.StaySignedIn)
	sess.ExpiresAt = sess.CreatedAt.Add(lifetime)
	sess.IdleExpiresAt = sess.LastSeenAt.Add(idle)
	if sess.IdleExpiresAt.After(sess.ExpiresAt) {
		sess.IdleExpiresAt = sess.ExpiresAt
	}
}

// applyDeadline sets the stored session's expiry (row and cookie) to the
// end of its lifetime. SCS keeps deadlines on the wall clock, so the time
// left on the injected clock is added to the wall clock.
func (s *Service) applyDeadline(ctx context.Context, authAt time.Time, stay bool) {
	_, lifetime := s.limits(stay)
	s.kit.Sessions.SetDeadline(ctx, clock.Real().Now().Add(authAt.Add(lifetime).Sub(s.now())))
}

func clientIP(ctx context.Context) string {
	if ip := requestinfo.ClientIP(ctx); ip.IsValid() {
		return ip.Unmap().String()
	}
	return ""
}

// drop destroys an invalid session (row deleted, cookie cleared) and its
// signed-in device.
func (s *Service) drop(ctx context.Context) error {
	if err := s.endDevice(ctx); err != nil {
		return err
	}
	return s.kit.Sessions.Destroy(ctx)
}

// endDevice deletes the signed-in device of the request's session, if any.
func (s *Service) endDevice(ctx context.Context) error {
	sm := s.kit.Sessions
	sid := sm.GetString(ctx, kSessionID)
	if sid == "" {
		return nil
	}
	err := store.DeleteUserSession(ctx, s.db, sid, sm.GetString(ctx, kUserID))
	if errors.Is(err, domain.ErrUserSessionNotFound) {
		return nil
	}
	return err
}

func (s *Service) clearPending(ctx context.Context) {
	sm := s.kit.Sessions
	for _, k := range []string{kPendingID, kPendingAt, kPendingPf, kPendingStay} {
		sm.Remove(ctx, k)
	}
}

func (s *Service) clearAuth(ctx context.Context) {
	sm := s.kit.Sessions
	for _, k := range []string{kUserID, kEpoch, kStage, kProven, kAuthAt, kSeenAt, kStepUpAt, kSessionID} {
		sm.Remove(ctx, k)
	}
}

func (s *Service) clearCeremonies(ctx context.Context) {
	sm := s.kit.Sessions
	for _, k := range []string{kWAReg, kWARegName, kWALogin, kWAPurpose} {
		sm.Remove(ctx, k)
	}
}

// session returns the caller's validated session. Limited enrollment
// sessions are accepted only when allowEnrollment is true.
func (s *Service) session(ctx context.Context, allowEnrollment bool) (*current, error) {
	cur := currentFrom(ctx)
	if cur == nil {
		return nil, domain.ErrNotAuthenticated
	}
	if cur.stage == domain.StageEnrollment && !allowEnrollment {
		return nil, domain.ErrEnrollmentRequired
	}
	return cur, nil
}

// requireRecent demands a sign-in or step-up within StepUpWindow.
func (s *Service) requireRecent(cur *current) error {
	if s.now().Sub(cur.stepUpAt) > StepUpWindow {
		return domain.ErrStepUpRequired
	}
	return nil
}

// requireOwner returns the owner's full session (step-up when recent).
func (s *Service) requireOwner(ctx context.Context, recent bool) (*current, error) {
	cur, err := s.session(ctx, false)
	if err != nil {
		return nil, err
	}
	if !cur.user.Owner || !s.isOwner(cur.user.ID) {
		return nil, domain.ErrForbidden
	}
	if recent {
		if err := s.requireRecent(cur); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// establish starts a session of stage for user after a privilege change:
// the token is renewed (session fixation), pending and ceremony state is
// cleared, and a signed-in device is recorded (replacing the one this
// browser had). With stay ("Stay signed in") the session gets the longer
// limits and a persistent cookie; otherwise the cookie ends with the
// browser.
func (s *Service) establish(ctx context.Context, user domain.User, stage domain.SessionStage, proven factorSet, stay bool) error {
	sm := s.kit.Sessions
	if err := s.endDevice(ctx); err != nil {
		return err
	}
	if err := sm.RenewToken(ctx); err != nil {
		return err
	}
	s.clearPending(ctx)
	s.clearCeremonies(ctx)
	sm.Remove(ctx, kSeenAt)
	at := s.now()
	sess := domain.UserSession{
		ID: newID(), UserID: user.ID, Epoch: user.SessionEpoch, StaySignedIn: stay, CreatedAt: at, LastSeenAt: at,
		IP: clientIP(ctx), UserAgent: requestinfo.UserAgent(ctx),
	}
	if err := store.InsertUserSession(ctx, s.db, sess); err != nil {
		return err
	}
	now := at.UnixNano()
	sm.Put(ctx, kSessionID, sess.ID)
	sm.Put(ctx, kUserID, user.ID)
	sm.Put(ctx, kEpoch, user.SessionEpoch)
	sm.Put(ctx, kStage, string(stage))
	sm.Put(ctx, kProven, int64(proven))
	sm.Put(ctx, kAuthAt, now)
	sm.Put(ctx, kStepUpAt, now)
	sm.RememberMe(ctx, stay)
	s.applyDeadline(ctx, at, stay)
	return nil
}

// rotate renews the caller's token and records a fresh authentication
// (after a step-up or a credential change), optionally moving it to a new
// stage/epoch. The signed-in device stays the same.
func (s *Service) rotate(ctx context.Context, cur *current, stage domain.SessionStage, epoch int64, proven factorSet) error {
	sm := s.kit.Sessions
	if err := sm.RenewToken(ctx); err != nil {
		return err
	}
	// RenewToken restarts the deadline; the session keeps its lifetime.
	s.applyDeadline(ctx, cur.authAt, cur.session.StaySignedIn)
	if epoch != cur.session.Epoch {
		if err := store.SetUserSessionEpoch(ctx, s.db, cur.session.ID, epoch); err != nil {
			return err
		}
		cur.session.Epoch = epoch
	}
	now := s.now()
	sm.Put(ctx, kStage, string(stage))
	sm.Put(ctx, kEpoch, epoch)
	sm.Put(ctx, kProven, int64(proven))
	sm.Put(ctx, kStepUpAt, now.UnixNano())
	cur.stage, cur.user.SessionEpoch, cur.proven, cur.stepUpAt = stage, epoch, proven, now
	return nil
}

// pending returns the account of a sign-in waiting for more factors.
func (s *Service) pending(ctx context.Context) (domain.User, domain.UserCredentials, factorSet, error) {
	sm := s.kit.Sessions
	uid := sm.GetString(ctx, kPendingID)
	if uid == "" {
		return domain.User{}, domain.UserCredentials{}, 0, domain.ErrNoPendingFlow
	}
	u, creds, err := store.GetUserWithCredentials(ctx, s.db, uid)
	if errors.Is(err, domain.ErrUserNotFound) || (err == nil && !u.Active()) {
		s.clearPending(ctx)
		return domain.User{}, domain.UserCredentials{}, 0, domain.ErrNoPendingFlow
	}
	return u, creds, factorSet(sm.GetInt64(ctx, kPendingPf)), err //nolint:gosec // small bit set
}

// state describes the caller's session for the API.
func (s *Service) state(ctx context.Context) (domain.SessionState, error) {
	sm := s.kit.Sessions
	set, err := s.settings(ctx)
	if err != nil {
		return domain.SessionState{}, err
	}
	if cur := currentFrom(ctx); cur != nil && sm.GetString(ctx, kUserID) == cur.user.ID && sm.GetString(ctx, kSessionID) == cur.session.ID {
		return s.stateOf(ctx, cur.user, domain.SessionStage(sm.GetString(ctx, kStage)), cur.session, set)
	}
	if uid := sm.GetString(ctx, kUserID); uid != "" {
		// Established during this request (sign-in, setup, redemption).
		u, err := store.GetUser(ctx, s.db, uid)
		if err != nil {
			return domain.SessionState{}, err
		}
		sess, err := store.GetUserSession(ctx, s.db, sm.GetString(ctx, kSessionID))
		if err != nil {
			return domain.SessionState{}, err
		}
		return s.stateOf(ctx, u, domain.SessionStage(sm.GetString(ctx, kStage)), sess, set)
	}
	u, _, proven, err := s.pending(ctx)
	if err != nil {
		return domain.SessionState{}, domain.ErrNotAuthenticated
	}
	enrolled, err := s.enrolled(ctx, u)
	if err != nil {
		return domain.SessionState{}, err
	}
	ev := evaluate(set.RequiredFactors, enrolled, proven, false)
	st := domain.SessionState{Stage: domain.StageSecondFactor, Factors: ev.next.list(), RequiredFactors: set.RequiredFactors}
	if proven.has(fPassword) && (ev.next.has(fTOTP) || ev.next.has(fPasskey)) {
		if rc, err := store.RecoveryCodes(ctx, s.db, u.ID); err == nil && rc.Remaining > 0 {
			st.Factors = append(st.Factors, domain.FactorRecoveryCode)
		}
	}
	return st, nil
}

func (s *Service) stateOf(ctx context.Context, u domain.User, stage domain.SessionStage, sess domain.UserSession, set domain.SecuritySettings) (domain.SessionState, error) {
	sm := s.kit.Sessions
	acct, err := s.account(ctx, u)
	if err != nil {
		return domain.SessionState{}, err
	}
	authAt := unixNano(sm.GetInt64(ctx, kAuthAt))
	idleTimeout, lifetime := s.limits(sess.StaySignedIn)
	expires := authAt.Add(lifetime)
	idle := sess.LastSeenAt.Add(idleTimeout)
	if idle.After(expires) {
		idle = expires
	}
	recent := unixNano(sm.GetInt64(ctx, kStepUpAt)).Add(StepUpWindow)
	st := domain.SessionState{
		Stage: stage, Account: &acct, RequiredFactors: set.RequiredFactors,
		AuthenticatedAt: &authAt, ExpiresAt: &expires, IdleExpiresAt: &idle,
		SessionID: sess.ID, StaySignedIn: sess.StaySignedIn,
	}
	if recent.After(s.now()) {
		st.RecentAuthUntil = &recent
	}
	if stage == domain.StageEnrollment {
		enrolled := s.enrolledFrom(acct)
		ev := evaluate(set.RequiredFactors, enrolled, enrolled, false)
		st.MissingFactors = ev.missing.list()
		if !u.Owner {
			st.EnrollmentDeadline = u.EnrollmentDeadline
		}
	}
	return st, nil
}

func (s *Service) account(ctx context.Context, u domain.User) (domain.Account, error) {
	n, err := store.CountPasskeys(ctx, s.db, u.ID)
	if err != nil {
		return domain.Account{}, err
	}
	rc, err := store.RecoveryCodes(ctx, s.db, u.ID)
	if err != nil {
		return domain.Account{}, err
	}
	return domain.Account{User: u, PasskeyCount: n, RecoveryCodesRemaining: rc.Remaining}, nil
}

func (s *Service) enrolledFrom(a domain.Account) factorSet {
	var f factorSet
	if a.HasPassword {
		f |= fPassword
	}
	if a.TOTPEnabled {
		f |= fTOTP
	}
	if a.PasskeyCount > 0 {
		f |= fPasskey
	}
	return f
}

func (s *Service) enrolled(ctx context.Context, u domain.User) (factorSet, error) {
	a, err := s.account(ctx, u)
	if err != nil {
		return 0, err
	}
	return s.enrolledFrom(a), nil
}
