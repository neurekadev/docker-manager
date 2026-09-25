package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/logging"
	"github.com/neurekadev/dockyard/internal/manager/api"
	"github.com/neurekadev/dockyard/internal/manager/authsep"
	"github.com/neurekadev/dockyard/internal/manager/authz"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// Session data keys (SCS, gob-encoded basic types only).
const (
	kUserID    = "uid"
	kEpoch     = "epoch"
	kStage     = "stage"
	kProven    = "proven"
	kAuthAt    = "auth_at"
	kSeenAt    = "seen_at"
	kStepUpAt  = "stepup_at"
	kPendingID = "p_uid"
	kPendingAt = "p_at"
	kPendingPf = "p_proven"
	kWAReg     = "wa_reg"
	kWARegName = "wa_reg_name"
	kWALogin   = "wa_login"
	kWAPurpose = "wa_purpose"
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
}

type currentKey struct{}

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
//     ends its in-flight requests and streams.
func (s *Service) Middleware(next http.Handler) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
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
				"finish enrolling the sign-in factors the instance policy requires before using DockYard"))
			return
		}
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		defer s.hub.register(cur.user.ID, cur.user.SessionEpoch, cancel)()
		ctx = context.WithValue(ctx, currentKey{}, cur)
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
		if _, ok := authsep.BearerToken(r.Header); ok {
			// Bearer requests are never authenticated by cookies (and so
			// need no CSRF protection).
			r.Header.Del("Cookie")
		} else if err := s.kit.CSRF.Check(r); err != nil {
			logging.FromContext(r.Context()).Warn("cross-origin request rejected",
				slog.String("method", r.Method), slog.String("path", r.URL.Path))
			api.WriteError(w, r, api.NewError(http.StatusForbidden, api.CodeCrossOriginRequest,
				"cross-origin requests are not allowed; use DockYard from its own origin"))
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
	authAt := unixNano(sm.GetInt64(ctx, kAuthAt))
	seenAt := unixNano(sm.GetInt64(ctx, kSeenAt))
	stage := domain.SessionStage(sm.GetString(ctx, kStage))
	switch {
	case !user.Active(),
		user.SessionEpoch != sm.GetInt64(ctx, kEpoch),
		now.Sub(authAt) >= s.lifetime,
		now.Sub(seenAt) >= s.idle,
		stage != domain.StageAuthenticated && stage != domain.StageEnrollment:
		return nil, s.drop(ctx)
	}
	if now.Sub(seenAt) >= seenGranularity {
		sm.Put(ctx, kSeenAt, now.UnixNano())
	}
	return &current{
		user: user, stage: stage, proven: factorSet(sm.GetInt64(ctx, kProven)), //nolint:gosec // small bit set
		authAt: authAt, stepUpAt: unixNano(sm.GetInt64(ctx, kStepUpAt)),
	}, nil
}

// drop destroys an invalid session (row deleted, cookie cleared).
func (s *Service) drop(ctx context.Context) error {
	return s.kit.Sessions.Destroy(ctx)
}

func (s *Service) clearPending(ctx context.Context) {
	sm := s.kit.Sessions
	for _, k := range []string{kPendingID, kPendingAt, kPendingPf} {
		sm.Remove(ctx, k)
	}
}

func (s *Service) clearAuth(ctx context.Context) {
	sm := s.kit.Sessions
	for _, k := range []string{kUserID, kEpoch, kStage, kProven, kAuthAt, kSeenAt, kStepUpAt} {
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
// cleared.
func (s *Service) establish(ctx context.Context, user domain.User, stage domain.SessionStage, proven factorSet) error {
	sm := s.kit.Sessions
	if err := sm.RenewToken(ctx); err != nil {
		return err
	}
	s.clearPending(ctx)
	s.clearCeremonies(ctx)
	now := s.now().UnixNano()
	sm.Put(ctx, kUserID, user.ID)
	sm.Put(ctx, kEpoch, user.SessionEpoch)
	sm.Put(ctx, kStage, string(stage))
	sm.Put(ctx, kProven, int64(proven))
	sm.Put(ctx, kAuthAt, now)
	sm.Put(ctx, kSeenAt, now)
	sm.Put(ctx, kStepUpAt, now)
	return nil
}

// rotate renews the caller's token and records a fresh authentication
// (after a step-up or a credential change), optionally moving it to a new
// stage/epoch.
func (s *Service) rotate(ctx context.Context, cur *current, stage domain.SessionStage, epoch int64, proven factorSet) error {
	sm := s.kit.Sessions
	if err := sm.RenewToken(ctx); err != nil {
		return err
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
	if cur := currentFrom(ctx); cur != nil && sm.GetString(ctx, kUserID) == cur.user.ID {
		return s.stateOf(ctx, cur.user, domain.SessionStage(sm.GetString(ctx, kStage)), set)
	}
	if uid := sm.GetString(ctx, kUserID); uid != "" {
		// Established during this request (sign-in, setup, redemption).
		u, err := store.GetUser(ctx, s.db, uid)
		if err != nil {
			return domain.SessionState{}, err
		}
		return s.stateOf(ctx, u, domain.SessionStage(sm.GetString(ctx, kStage)), set)
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

func (s *Service) stateOf(ctx context.Context, u domain.User, stage domain.SessionStage, set domain.SecuritySettings) (domain.SessionState, error) {
	sm := s.kit.Sessions
	acct, err := s.account(ctx, u)
	if err != nil {
		return domain.SessionState{}, err
	}
	authAt := unixNano(sm.GetInt64(ctx, kAuthAt))
	seenAt := unixNano(sm.GetInt64(ctx, kSeenAt))
	expires := authAt.Add(s.lifetime)
	idle := seenAt.Add(s.idle)
	if idle.After(expires) {
		idle = expires
	}
	recent := unixNano(sm.GetInt64(ctx, kStepUpAt)).Add(StepUpWindow)
	st := domain.SessionState{
		Stage: stage, Account: &acct, RequiredFactors: set.RequiredFactors,
		AuthenticatedAt: &authAt, ExpiresAt: &expires, IdleExpiresAt: &idle,
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
