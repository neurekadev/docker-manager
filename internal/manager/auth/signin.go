package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/auth/password"
	"github.com/neurekadev/docker-manager/internal/manager/auth/throttle"
	"github.com/neurekadev/docker-manager/internal/manager/auth/totp"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

func ipKey(ctx context.Context) string { return throttle.IPKey(requestinfo.ClientIP(ctx)) }

func userKey(id string) string { return "user:" + id }

// limitKey is one bucket an attempt counts against.
type limitKey struct {
	l   *throttle.Limiter
	key string
}

// accountLimit is key in the per-account limiter (an account name from one
// client, or a user ID for the factors after the password).
func (s *Service) accountLimit(key string) limitKey { return limitKey{s.kit.AccountLimit, key} }

// attempt is one throttled credential check. begin takes a token from the
// client IP's bucket and every account bucket before anything is verified,
// so concurrent attempts cannot outrun the limits; fail keeps the tokens
// spent and release (deferred) refunds them otherwise, so only failures
// count.
type attempt struct {
	taken []limitKey
	ended bool
}

// begin starts an attempt, or answers 429 (taking nothing) when any of its
// buckets is empty.
func (s *Service) begin(ctx context.Context, keys ...limitKey) (*attempt, error) {
	keys = append([]limitKey{{s.kit.IPLimit, ipKey(ctx)}}, keys...)
	a := &attempt{}
	var wait time.Duration
	for _, k := range keys {
		if k.l.Take(k.key) {
			a.taken = append(a.taken, k)
		} else {
			wait = max(wait, k.l.RetryAfter(k.key))
		}
	}
	if wait > 0 || len(a.taken) < len(keys) {
		a.release()
		return nil, &domain.RateLimitedError{RetryAfter: max(wait, time.Second)}
	}
	return a, nil
}

// fail ends the attempt as a failed credential check: its tokens stay spent.
func (a *attempt) fail() { a.ended = true }

// release ends the attempt unless it failed, refunding its tokens. It is
// safe to call more than once.
func (a *attempt) release() {
	if a.ended {
		return
	}
	a.ended = true
	for _, k := range a.taken {
		k.l.Refund(k.key)
	}
}

func (s *Service) record(ctx context.Context, action, outcome, actor, targetType, targetID, reason string) {
	s.audit.Record(ctx, AuditEvent{Action: action, Outcome: outcome, ActorUserID: actor, TargetType: targetType, TargetID: targetID, Reason: reason})
}

// SignIn checks a username and password (POST /auth/session). Unknown
// accounts, wrong passwords, accounts without a password and disabled
// accounts fail identically, after the same single Argon2id computation.
// A following TOTP code (same request or a later one) continues a pending
// sign-in. stay asks for "Stay signed in" (when the policy allows it); a
// pending sign-in keeps the choice of its first step.
func (s *Service) SignIn(ctx context.Context, username, pw, totpCode string, stay bool) (domain.SessionState, error) {
	if username == "" {
		if totpCode == "" {
			return domain.SessionState{}, &domain.FieldError{Field: "username", Message: "username and password, or a TOTP code for a pending sign-in, are required"}
		}
		return s.continueWithTOTP(ctx, totpCode)
	}
	name := strings.TrimSpace(username)
	// The strict account bucket is per client, so failures from one client
	// never lock the account out for everyone; the account-wide ceiling
	// bounds guessing spread over many clients.
	fromClient := throttle.AccountClientKey(name, requestinfo.ClientIP(ctx))
	att, err := s.begin(ctx, s.accountLimit(fromClient), limitKey{s.kit.AccountCeiling, throttle.AccountKey(name)})
	if err != nil {
		s.record(ctx, "auth.sign_in", OutcomeFailure, "", "", "", "rate_limited")
		return domain.SessionState{}, err
	}
	defer att.release()
	user, creds, err := store.GetUserByUsername(ctx, s.db, name)
	found := err == nil
	if err != nil && !errors.Is(err, domain.ErrUserNotFound) {
		return domain.SessionState{}, err
	}
	ok, rehash, err := s.kit.Passwords.Verify(ctx, pw, creds.PasswordHash)
	if err != nil {
		return domain.SessionState{}, err
	}
	if !found || !ok || !user.Active() {
		att.fail()
		s.record(ctx, "auth.sign_in", OutcomeFailure, "", "", "", "invalid_credentials")
		return domain.SessionState{}, domain.ErrInvalidCredentials
	}
	// The password is right: its tokens come back and this client's earlier
	// failures for the account are forgotten, before any second factor
	// (which counts on its own).
	att.release()
	s.kit.AccountLimit.Reset(fromClient)
	if rehash {
		s.upgradeHash(ctx, user.ID, pw)
	}
	st, err := s.advance(ctx, user, fPassword, false, stay)
	if err != nil || totpCode == "" || st.Stage != domain.StageSecondFactor {
		return st, err
	}
	return s.continueWithTOTP(ctx, totpCode)
}

// upgradeHash re-hashes a verified password with the current parameters
// (upgrade-on-login). Failures are logged; the sign-in proceeds.
func (s *Service) upgradeHash(ctx context.Context, userID, pw string) {
	h, err := s.kit.Passwords.Hash(ctx, pw)
	if err == nil {
		err = store.SetPasswordHash(ctx, s.db, userID, h, false, s.now())
	}
	if err != nil {
		s.log.Warn("upgrade password hash", "user_id", userID, "error", err)
	}
}

// advance applies the instance policy after factors were proven and moves
// the session to the resulting stage. stay asks for "Stay signed in"; it
// applies only when the policy allows it.
func (s *Service) advance(ctx context.Context, user domain.User, proven factorSet, recovery, stay bool) (domain.SessionState, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return domain.SessionState{}, err
	}
	stay = stay && set.AllowStaySignedIn
	enrolled, err := s.enrolled(ctx, user)
	if err != nil {
		return domain.SessionState{}, err
	}
	ev := evaluate(set.RequiredFactors, enrolled, proven, recovery)
	switch ev.stage {
	case domain.StageAuthenticated:
		if err := s.establish(ctx, user, domain.StageAuthenticated, proven, stay); err != nil {
			return domain.SessionState{}, err
		}
		if user.EnrollmentDeadline != nil && enrollmentComplete(set.RequiredFactors, enrolled) {
			_ = store.SetEnrollmentDeadline(ctx, s.db, user.ID, nil, s.now())
		}
		_ = store.SetLastSignIn(ctx, s.db, user.ID, s.now())
		reason := ""
		if recovery {
			reason = "recovery_code"
		}
		audit.SetDetail(ctx, "staySignedIn", stay)
		s.record(ctx, "auth.sign_in", OutcomeSuccess, user.ID, "user", user.ID, reason)
	case domain.StageSecondFactor:
		if ev.next.has(fPassword) {
			s.record(ctx, "auth.sign_in", OutcomeFailure, "", "user", user.ID, "method_not_allowed")
			return domain.SessionState{}, domain.ErrMethodNotAllowed
		}
		sm := s.kit.Sessions
		// A new sign-in replaces whatever this browser was signed in as.
		if err := s.endDevice(ctx); err != nil {
			return domain.SessionState{}, err
		}
		if err := sm.RenewToken(ctx); err != nil {
			return domain.SessionState{}, err
		}
		s.clearAuth(ctx)
		s.clearCeremonies(ctx)
		sm.Put(ctx, kPendingID, user.ID)
		sm.Put(ctx, kPendingAt, s.now().UnixNano())
		sm.Put(ctx, kPendingPf, int64(proven))
		sm.Put(ctx, kPendingStay, stay)
	case domain.StageEnrollment:
		if !user.Owner {
			// The owner is never locked out (no deadline); everyone else
			// gets a grace period to enroll, then needs an owner reset.
			switch {
			case user.EnrollmentDeadline == nil:
				d := s.now().Add(time.Duration(set.EnrollmentGraceHours) * time.Hour)
				if err := store.SetEnrollmentDeadline(ctx, s.db, user.ID, &d, s.now()); err != nil {
					return domain.SessionState{}, err
				}
			case !s.now().Before(*user.EnrollmentDeadline):
				s.record(ctx, "auth.sign_in", OutcomeFailure, user.ID, "user", user.ID, "enrollment_expired")
				return domain.SessionState{}, domain.ErrEnrollmentExpired
			}
		}
		if err := s.establish(ctx, user, domain.StageEnrollment, proven, stay); err != nil {
			return domain.SessionState{}, err
		}
		_ = store.SetLastSignIn(ctx, s.db, user.ID, s.now())
		audit.SetDetail(ctx, "staySignedIn", stay)
		s.record(ctx, "auth.sign_in", OutcomeSuccess, user.ID, "user", user.ID, "enrollment_session")
	}
	return s.state(ctx)
}

// verifyTOTP checks code for user (throttled, replay-safe: the accepted
// time step is stored with compare-and-set before the factor counts).
func (s *Service) verifyTOTP(ctx context.Context, user domain.User, creds domain.UserCredentials, code string) error {
	att, err := s.begin(ctx, s.accountLimit(userKey(user.ID)))
	if err != nil {
		return err
	}
	defer att.release()
	if creds.TOTPSeedSealed == "" {
		att.fail()
		return domain.ErrInvalidCredentials
	}
	seed, err := s.keyring.Open(creds.TOTPSeedSealed, totpSeedContext(user.ID))
	if err != nil {
		return fmt.Errorf("auth: open totp seed: %w", err)
	}
	step, ok, err := s.kit.VerifyTOTP(string(seed), code, creds.TOTPLastStep)
	if err != nil && !errors.Is(err, totp.ErrMalformed) {
		return err
	}
	if ok {
		if ok, err = store.AdvanceTOTPStep(ctx, s.db, user.ID, step); err != nil {
			return err
		}
	}
	if !ok {
		att.fail()
		return domain.ErrInvalidCredentials
	}
	return nil
}

func totpSeedContext(userID string) string    { return "users/" + userID + "/totp_seed" }
func totpPendingContext(userID string) string { return "users/" + userID + "/totp_pending_seed" }

func (s *Service) continueWithTOTP(ctx context.Context, code string) (domain.SessionState, error) {
	user, creds, proven, err := s.pending(ctx)
	if err != nil {
		return domain.SessionState{}, err
	}
	if err := s.verifyTOTP(ctx, user, creds, code); err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			s.record(ctx, "auth.sign_in", OutcomeFailure, "", "user", user.ID, "totp")
		}
		return domain.SessionState{}, err
	}
	return s.advance(ctx, user, proven|fTOTP, false, s.pendingStay(ctx))
}

// RedeemRecoveryCode completes a pending password sign-in with a one-time
// recovery code instead of TOTP or a passkey.
func (s *Service) RedeemRecoveryCode(ctx context.Context, code string) (domain.SessionState, error) {
	user, _, proven, err := s.pending(ctx)
	if err != nil {
		return domain.SessionState{}, err
	}
	att, err := s.begin(ctx, s.accountLimit(userKey(user.ID)))
	if err != nil {
		return domain.SessionState{}, err
	}
	defer att.release()
	if !proven.has(fPassword) {
		return domain.SessionState{}, domain.ErrNoPendingFlow
	}
	used, err := store.UseRecoveryCode(ctx, s.db, user.ID, recoveryVerifier(user.ID, code), s.now())
	if err != nil {
		return domain.SessionState{}, err
	}
	if !used {
		att.fail()
		s.record(ctx, "auth.recovery_code_use", OutcomeFailure, "", "user", user.ID, "invalid_code")
		return domain.SessionState{}, domain.ErrInvalidCredentials
	}
	s.record(ctx, "auth.recovery_code_use", OutcomeSuccess, user.ID, "user", user.ID, "")
	return s.advance(ctx, user, proven, true, s.pendingStay(ctx))
}

// CurrentSession describes the caller's session (401 when there is none).
func (s *Service) CurrentSession(ctx context.Context) (domain.SessionState, error) {
	if currentFrom(ctx) == nil && !s.kit.Sessions.Exists(ctx, kPendingID) {
		return domain.SessionState{}, domain.ErrNotAuthenticated
	}
	return s.state(ctx)
}

// SignOut ends the caller's session (also a pending one) and its signed-in
// device; the device's other open streams close.
func (s *Service) SignOut(ctx context.Context) error {
	cur := currentFrom(ctx)
	if cur == nil && !s.kit.Sessions.Exists(ctx, kPendingID) {
		return domain.ErrNotAuthenticated
	}
	if err := s.drop(ctx); err != nil {
		return err
	}
	if cur != nil {
		s.record(ctx, "auth.sign_out", OutcomeSuccess, cur.user.ID, "user", cur.user.ID, "")
		s.hub.revokeSessions([]string{cur.session.ID}, authz.ErrSessionEnded, hubEntryFrom(ctx))
	}
	return nil
}

// StepUp re-authenticates the caller for sensitive changes with exactly
// one factor: a passkey assertion (options with purpose step_up), else
// the TOTP code when TOTP is enabled, else the password. An account with
// a second factor never steps up with its password alone, and no step-up
// asks for two factors (the session already proved the sign-in).
func (s *Service) StepUp(ctx context.Context, req domain.StepUp) (domain.SessionState, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return domain.SessionState{}, err
	}
	enrolled, err := s.enrolled(ctx, cur.user)
	if err != nil {
		return domain.SessionState{}, err
	}
	var proven factorSet
	switch {
	case len(req.PasskeyResponse) > 0:
		proven, err = fPasskey, s.stepUpWithPasskey(ctx, cur, req.PasskeyResponse)
	case enrolled.has(fTOTP):
		proven, err = fTOTP, s.stepUpWithTOTP(ctx, cur, req.TOTPCode)
	case enrolled.has(fPasskey):
		err = &domain.FieldError{Field: "credential", Message: "this account confirms its identity with a passkey"}
	default:
		proven, err = fPassword, s.stepUpWithPassword(ctx, cur, req.Password)
	}
	if err != nil {
		return domain.SessionState{}, err
	}
	if err := s.rotate(ctx, cur, cur.stage, cur.user.SessionEpoch, cur.proven|proven); err != nil {
		return domain.SessionState{}, err
	}
	s.record(ctx, "auth.step_up", OutcomeSuccess, cur.user.ID, "user", cur.user.ID, "")
	return s.state(ctx)
}

// stepUpWithPasskey verifies a step-up passkey assertion; a failed one
// counts against the account's limit.
func (s *Service) stepUpWithPasskey(ctx context.Context, cur *current, response []byte) error {
	att, err := s.begin(ctx, s.accountLimit(userKey(cur.user.ID)))
	if err != nil {
		return err
	}
	defer att.release()
	if err := s.finishStepUpPasskey(ctx, cur, response); err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			att.fail()
		}
		return err
	}
	return nil
}

// stepUpWithTOTP verifies a step-up TOTP code (verifyTOTP counts the
// failures).
func (s *Service) stepUpWithTOTP(ctx context.Context, cur *current, code string) error {
	if strings.TrimSpace(code) == "" {
		return &domain.FieldError{Field: "totpCode", Message: "the authenticator code is required"}
	}
	_, creds, err := store.GetUserWithCredentials(ctx, s.db, cur.user.ID)
	if err != nil {
		return err
	}
	if err := s.verifyTOTP(ctx, cur.user, creds, code); err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			s.record(ctx, "auth.step_up", OutcomeFailure, cur.user.ID, "user", cur.user.ID, "totp")
		}
		return err
	}
	return nil
}

// stepUpWithPassword verifies the password of an account without a
// second factor; a wrong one counts against the account's limit.
func (s *Service) stepUpWithPassword(ctx context.Context, cur *current, pw string) error {
	att, err := s.begin(ctx, s.accountLimit(userKey(cur.user.ID)))
	if err != nil {
		return err
	}
	defer att.release()
	_, creds, err := store.GetUserWithCredentials(ctx, s.db, cur.user.ID)
	if err != nil {
		return err
	}
	ok, _, err := s.kit.Passwords.Verify(ctx, pw, creds.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		att.fail()
		s.record(ctx, "auth.step_up", OutcomeFailure, cur.user.ID, "user", cur.user.ID, "invalid_credentials")
		return domain.ErrInvalidCredentials
	}
	return nil
}

// passwordPolicy returns the instance password policy.
func (s *Service) passwordPolicy(set domain.SecuritySettings) password.Policy {
	return password.Policy{Strict: set.StrictPasswords, MinLength: set.MinPasswordLength}
}

// checkPassword validates a new password for an account.
func (s *Service) checkPassword(set domain.SecuritySettings, field, pw string, context ...string) error {
	v := s.passwordPolicy(set).Check(pw, context...)
	if len(v) == 0 {
		return nil
	}
	e := &domain.PasswordPolicyError{Field: field}
	for _, x := range v {
		e.Violations = append(e.Violations, domain.PasswordViolation{Code: x.Code, Message: x.Message})
	}
	return e
}

// ChangePassword sets the caller's password (recent authentication; the
// current password when one is set). Other sessions end; the caller's
// session continues with a new token. With revokeAPITokens every API token
// of the account is revoked too (#31).
func (s *Service) ChangePassword(ctx context.Context, currentPW, newPW string, revokeAPITokens bool) error {
	cur, err := s.session(ctx, true)
	if err != nil {
		return err
	}
	if err := s.requireRecent(cur); err != nil {
		return err
	}
	_, creds, err := store.GetUserWithCredentials(ctx, s.db, cur.user.ID)
	if err != nil {
		return err
	}
	if creds.PasswordHash != "" {
		att, err := s.begin(ctx, s.accountLimit(userKey(cur.user.ID)))
		if err != nil {
			return err
		}
		ok, _, err := s.kit.Passwords.Verify(ctx, currentPW, creds.PasswordHash)
		if !ok && err == nil {
			att.fail()
		}
		att.release()
		if err != nil {
			return err
		}
		if !ok {
			return domain.ErrInvalidCredentials
		}
	}
	set, err := s.settings(ctx)
	if err != nil {
		return err
	}
	if err := s.checkPassword(set, "newPassword", newPW, cur.user.Username, cur.user.Email, cur.user.DisplayName); err != nil {
		return err
	}
	hash, err := s.kit.Passwords.Hash(ctx, newPW)
	if err != nil {
		return err
	}
	var epoch int64
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.SetPasswordHash(ctx, tx, cur.user.ID, hash, true, s.now()); err != nil {
			return err
		}
		epoch, err = store.BumpSessionEpoch(ctx, tx, cur.user.ID, s.now())
		return err
	})
	if err != nil {
		return err
	}
	if err := s.rotate(ctx, cur, cur.stage, epoch, cur.proven|fPassword); err != nil {
		return err
	}
	if revokeAPITokens {
		if err := s.revokeUserTokens(ctx, cur.user.ID, cur.user.ID, domain.RevokedCredentialReset); err != nil {
			return err
		}
	}
	s.record(ctx, "auth.password_change", OutcomeSuccess, cur.user.ID, "user", cur.user.ID, "")
	s.forget(ctx, cur.user.ID)
	s.hub.revoke(cur.user.ID, epoch)
	return nil
}

// RedeemPasswordReset sets a new password with an owner-issued reset code
// or an owner-recovery code (which also clears the owner's TOTP, passkeys
// and recovery codes). All sessions of the account end; the user then
// signs in normally. With revokeAPITokens every API token of the account
// is revoked too (#31).
func (s *Service) RedeemPasswordReset(ctx context.Context, code, newPW string, revokeAPITokens bool) error {
	att, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer att.release()
	reset, err := store.FindAccountReset(ctx, s.db, verifier(code), s.now())
	if errors.Is(err, domain.ErrCodeInvalid) {
		att.fail()
		s.record(ctx, "auth.password_reset_redeem", OutcomeFailure, "", "", "", "invalid_code")
		return err
	}
	if err != nil {
		return err
	}
	user, err := store.GetUser(ctx, s.db, reset.UserID)
	if err != nil {
		return err
	}
	set, err := s.settings(ctx)
	if err != nil {
		return err
	}
	if err := s.checkPassword(set, "newPassword", newPW, user.Username, user.Email, user.DisplayName); err != nil {
		return err // the code stays valid: nothing was consumed
	}
	hash, err := s.kit.Passwords.Hash(ctx, newPW)
	if err != nil {
		return err
	}
	now := s.now()
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.ConsumeAccountReset(ctx, tx, reset.ID, now); err != nil {
			return err
		}
		if err := store.SetPasswordHash(ctx, tx, user.ID, hash, true, now); err != nil {
			return err
		}
		if reset.Kind == domain.ResetOwnerRecovery {
			if err := clearFactors(ctx, tx, user.ID, now); err != nil {
				return err
			}
		}
		_, err := store.BumpSessionEpoch(ctx, tx, user.ID, now)
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrCodeInvalid) {
			att.fail()
		}
		return err
	}
	if revokeAPITokens {
		if err := s.revokeUserTokens(ctx, user.ID, user.ID, domain.RevokedCredentialReset); err != nil {
			return err
		}
	}
	s.record(ctx, "auth.password_reset_redeem", OutcomeSuccess, user.ID, "user", user.ID, string(reset.Kind))
	return s.endSessions(ctx, user.ID, false)
}

// clearFactors removes TOTP, passkeys and recovery codes.
func clearFactors(ctx context.Context, tx bun.IDB, userID string, now time.Time) error {
	if err := store.ClearTOTP(ctx, tx, userID, now); err != nil {
		return err
	}
	if err := store.DeleteUserPasskeys(ctx, tx, userID); err != nil {
		return err
	}
	return store.DeleteRecoveryCodes(ctx, tx, userID)
}

// Me returns the caller's account (enrollment sessions and API tokens
// included).
func (s *Service) Me(ctx context.Context) (domain.Account, error) {
	if a, ok, err := s.tokenAccount(ctx); ok {
		return a, err
	}
	cur, err := s.session(ctx, true)
	if err != nil {
		return domain.Account{}, err
	}
	return s.account(ctx, cur.user)
}

// RecoveryCodeStatus reports the caller's remaining recovery codes.
func (s *Service) RecoveryCodeStatus(ctx context.Context) (domain.RecoveryCodeStatus, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return domain.RecoveryCodeStatus{}, err
	}
	return store.RecoveryCodes(ctx, s.db, cur.user.ID)
}

// RotateRecoveryCodes replaces the caller's recovery codes and returns the
// new set once (recent authentication required). Only a verifier of each
// code is stored.
func (s *Service) RotateRecoveryCodes(ctx context.Context) ([]string, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return nil, err
	}
	if err := s.requireRecent(cur); err != nil {
		return nil, err
	}
	codes := make([]string, RecoveryCodeCount)
	ids := make([]string, RecoveryCodeCount)
	hashes := make([]string, RecoveryCodeCount)
	for i := range codes {
		codes[i] = newRecoveryCode()
		ids[i] = newID()
		hashes[i] = recoveryVerifier(cur.user.ID, codes[i])
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return store.ReplaceRecoveryCodes(ctx, tx, cur.user.ID, ids, hashes, s.now())
	})
	if err != nil {
		return nil, err
	}
	s.record(ctx, "auth.recovery_codes_rotate", OutcomeSuccess, cur.user.ID, "user", cur.user.ID, "")
	return codes, nil
}
