package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/audit"
	"github.com/neurekadev/docker-manager/internal/manager/authz"
	"github.com/neurekadev/docker-manager/internal/manager/requestinfo"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validateAccount checks the profile fields of a new account.
func validateAccount(username, displayName, email string) error {
	if !usernameRE.MatchString(username) {
		return &domain.FieldError{Field: "username", Message: "1-64 characters: letters, digits, '.', '_' or '-', starting with a letter or digit"}
	}
	if utf8.RuneCountInString(displayName) > 128 {
		return &domain.FieldError{Field: "displayName", Message: "at most 128 characters"}
	}
	if email != "" && (len(email) > 254 || !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\r\n")) {
		return &domain.FieldError{Field: "email", Message: "not an email address"}
	}
	return nil
}

func newHandle() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

// SetupStatus reports whether first-run setup is complete and whether this
// request could complete it.
func (s *Service) SetupStatus(ctx context.Context) (domain.SetupStatus, error) {
	_, done, err := store.OwnerID(ctx, s.db)
	if err != nil {
		return domain.SetupStatus{}, err
	}
	set, err := s.settings(ctx)
	if err != nil {
		return domain.SetupStatus{}, err
	}
	st := domain.SetupStatus{Complete: done, SecureOrigin: true, StaySignedInAllowed: set.AllowStaySignedIn}
	if !done {
		if err := s.checkSecureOrigin(ctx); err != nil {
			var ie *domain.InsecureOriginError
			errors.As(err, &ie)
			st.SecureOrigin, st.Explanation = false, ie.Explanation
		}
	}
	return st, nil
}

func (s *Service) checkSecureOrigin(ctx context.Context) error {
	info, _ := requestinfo.From(ctx)
	if err := requestinfo.CheckSecureOrigin(s.publicURL, s.localDev, info); err != nil {
		var ie *requestinfo.InsecureOriginError
		if errors.As(err, &ie) {
			return &domain.InsecureOriginError{Reason: ie.Reason, Explanation: ie.Explanation}
		}
		return &domain.InsecureOriginError{Reason: "unknown", Explanation: err.Error()}
	}
	return nil
}

// SetupOpen admits a first-run setup request other than the owner's
// creation (the backup import routes, #24): no owner exists yet, the
// request arrived over HTTPS on the public origin, and the per-IP limit
// allows it. It returns domain.ErrSetupComplete once an owner exists.
func (s *Service) SetupOpen(ctx context.Context) error {
	if _, done, err := store.OwnerID(ctx, s.db); err != nil {
		return err
	} else if done {
		return domain.ErrSetupComplete
	}
	if err := s.checkSecureOrigin(ctx); err != nil {
		return err
	}
	if !s.kit.IPLimit.Take(ipKey(ctx)) {
		return &domain.RateLimitedError{RetryAfter: max(s.kit.IPLimit.RetryAfter(ipKey(ctx)), time.Second)}
	}
	return nil
}

// SetupOwner creates the instance owner (first-run setup) and signs them
// in. It is single-use and race-safe: the database admits one owner, so of
// concurrent requests exactly one succeeds and the others get
// domain.ErrSetupComplete. It refuses requests that did not arrive over
// HTTPS on the public origin.
func (s *Service) SetupOwner(ctx context.Context, in domain.OwnerSetup) (domain.SessionState, error) {
	if _, done, err := store.OwnerID(ctx, s.db); err != nil {
		return domain.SessionState{}, err
	} else if done {
		return domain.SessionState{}, domain.ErrSetupComplete
	}
	if err := s.checkSecureOrigin(ctx); err != nil {
		s.record(ctx, "setup.owner_create", OutcomeFailure, "", "", "", "insecure_origin")
		return domain.SessionState{}, err
	}
	if !s.kit.IPLimit.Take(ipKey(ctx)) {
		return domain.SessionState{}, &domain.RateLimitedError{RetryAfter: max(s.kit.IPLimit.RetryAfter(ipKey(ctx)), time.Second)}
	}
	in.Username = strings.TrimSpace(in.Username)
	if err := validateAccount(in.Username, in.DisplayName, in.Email); err != nil {
		return domain.SessionState{}, err
	}
	set, err := s.settings(ctx)
	if err != nil {
		return domain.SessionState{}, err
	}
	if err := s.checkPassword(set, "password", in.Password, in.Username, in.Email, in.DisplayName); err != nil {
		return domain.SessionState{}, err
	}
	hash, err := s.kit.Passwords.Hash(ctx, in.Password)
	if err != nil {
		return domain.SessionState{}, err
	}
	var owner domain.User
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Every account is in a group (users.group_id is NOT NULL), the
		// owner too; group rules never apply to the owner (owner bypass),
		// so groups do not count the owner and never keep a deletion from
		// happening (store.DeleteGroup moves the owner to the default).
		group, err := store.DefaultGroupID(ctx, tx)
		if err != nil {
			return err
		}
		owner, err = store.CreateUser(ctx, tx, domain.NewUser{
			ID: newID(), Username: in.Username, DisplayName: strings.TrimSpace(in.DisplayName), Email: strings.TrimSpace(in.Email),
			Owner: true, GroupID: group, PasswordHash: hash, WebAuthnHandle: newHandle(), CreatedAt: s.now(),
		})
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrSetupComplete) {
			s.record(ctx, "setup.owner_create", OutcomeFailure, "", "", "", "setup_complete")
		}
		return domain.SessionState{}, err
	}
	id := owner.ID
	s.ownerID.Store(&id)
	s.record(ctx, "setup.owner_create", OutcomeSuccess, owner.ID, "user", owner.ID, "")
	return s.advance(ctx, owner, fPassword, false, false)
}

// link builds a URL on the public origin with the code in the fragment
// (never sent to servers or proxies, never in access logs).
func (s *Service) link(path, code string) string {
	u := url.URL{Path: path, Fragment: "code=" + code}
	if s.publicURL != nil {
		u.Scheme, u.Host = s.publicURL.Scheme, s.publicURL.Host
	}
	return u.String()
}

// CreateInvitation issues a single-use invitation (owner, recent
// authentication). The code is returned once; only its verifier is kept.
func (s *Service) CreateInvitation(ctx context.Context, email string, ttlHours int) (domain.Invitation, domain.IssuedCode, error) {
	cur, err := s.requireOwner(ctx, true)
	if err != nil {
		return domain.Invitation{}, domain.IssuedCode{}, err
	}
	email = strings.TrimSpace(email)
	if err := validateAccount("x", "", email); err != nil {
		return domain.Invitation{}, domain.IssuedCode{}, err
	}
	set, err := s.settings(ctx)
	if err != nil {
		return domain.Invitation{}, domain.IssuedCode{}, err
	}
	if ttlHours <= 0 {
		ttlHours = set.InvitationTTLHours
	}
	code := newLinkCode(InvitationCodePrefix)
	now := s.now()
	inv := domain.Invitation{ID: newID(), Email: email, CreatedBy: cur.user.ID, CreatedAt: now, ExpiresAt: now.Add(time.Duration(ttlHours) * time.Hour)}
	if err := store.InsertInvitation(ctx, s.db, inv, verifier(code)); err != nil {
		return domain.Invitation{}, domain.IssuedCode{}, err
	}
	s.record(ctx, "invitation.create", OutcomeSuccess, cur.user.ID, "invitation", inv.ID, "")
	return inv, domain.IssuedCode{ID: inv.ID, Code: code, URL: s.link("/invitation", code), ExpiresAt: inv.ExpiresAt}, nil
}

// ListInvitations lists invitations, newest first (owner).
func (s *Service) ListInvitations(ctx context.Context, beforeID string, limit int) ([]domain.Invitation, error) {
	if _, err := s.requireOwner(ctx, false); err != nil {
		return nil, err
	}
	return store.ListInvitations(ctx, s.db, beforeID, limit)
}

// RevokeInvitation revokes an unredeemed invitation (owner).
func (s *Service) RevokeInvitation(ctx context.Context, id string) error {
	cur, err := s.requireOwner(ctx, false)
	if err != nil {
		return err
	}
	if err := store.RevokeInvitation(ctx, s.db, id, s.now()); err != nil {
		return err
	}
	s.record(ctx, "invitation.revoke", OutcomeSuccess, cur.user.ID, "invitation", id, "")
	return nil
}

// RedeemInvitation creates an account in the default group from an
// invitation and signs it in (an enrollment session when the policy
// requires factors). Unknown, expired, revoked and used codes fail alike;
// failures are throttled per client IP. Consuming the invitation and
// creating the account happen in one transaction.
func (s *Service) RedeemInvitation(ctx context.Context, in domain.InvitationRedemption) (domain.SessionState, error) {
	att, err := s.begin(ctx)
	if err != nil {
		return domain.SessionState{}, err
	}
	defer att.release()
	now := s.now()
	inv, err := store.FindRedeemableInvitation(ctx, s.db, verifier(in.Code), now)
	if err == nil && inv.Email != "" && !strings.EqualFold(inv.Email, strings.TrimSpace(in.Email)) {
		err = domain.ErrCodeInvalid // bound to another address: indistinguishable from an unknown code
	}
	if errors.Is(err, domain.ErrCodeInvalid) {
		att.fail()
		s.record(ctx, "invitation.redeem", OutcomeFailure, "", "", "", "invalid_code")
		return domain.SessionState{}, err
	}
	if err != nil {
		return domain.SessionState{}, err
	}
	in.Username = strings.TrimSpace(in.Username)
	if err := validateAccount(in.Username, in.DisplayName, in.Email); err != nil {
		return domain.SessionState{}, err
	}
	set, err := s.settings(ctx)
	if err != nil {
		return domain.SessionState{}, err
	}
	var hash string
	proven := factorSet(0)
	switch {
	case in.Password != "":
		if err := s.checkPassword(set, "password", in.Password, in.Username, in.Email, in.DisplayName); err != nil {
			return domain.SessionState{}, err
		}
		if hash, err = s.kit.Passwords.Hash(ctx, in.Password); err != nil {
			return domain.SessionState{}, err
		}
		proven = fPassword
	case set.RequiredFactors != domain.FactorsPasskey:
		return domain.SessionState{}, &domain.FieldError{Field: "password", Message: "a password is required by the sign-in policy"}
	}
	var deadline *time.Time
	if set.RequiredFactors != domain.FactorsNone || hash == "" {
		d := now.Add(time.Duration(set.EnrollmentGraceHours) * time.Hour)
		deadline = &d
	}
	var user domain.User
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.ConsumeInvitation(ctx, tx, inv.ID, now); err != nil {
			return err
		}
		group, err := store.DefaultGroupID(ctx, tx)
		if err != nil {
			return err
		}
		user, err = store.CreateUser(ctx, tx, domain.NewUser{
			ID: newID(), Username: in.Username, DisplayName: strings.TrimSpace(in.DisplayName), Email: strings.TrimSpace(in.Email),
			GroupID: group, PasswordHash: hash, WebAuthnHandle: newHandle(), EnrollmentDeadline: deadline,
			InvitationID: inv.ID, CreatedAt: now,
		})
		if err != nil {
			return err
		}
		return store.SetInvitationUser(ctx, tx, inv.ID, user.ID)
	})
	if err != nil {
		if errors.Is(err, domain.ErrCodeInvalid) {
			att.fail()
		}
		return domain.SessionState{}, err
	}
	s.record(ctx, "invitation.redeem", OutcomeSuccess, user.ID, "invitation", inv.ID, "")
	return s.advance(ctx, user, proven, false, false)
}

// ListUsers lists accounts by ID (owner).
func (s *Service) ListUsers(ctx context.Context, afterID string, limit int) ([]domain.Account, error) {
	if _, err := s.requireOwner(ctx, false); err != nil {
		return nil, err
	}
	users, err := store.ListUsers(ctx, s.db, afterID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Account, 0, len(users))
	for _, u := range users {
		a, err := s.account(ctx, u)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// GetUser returns one account (owner).
func (s *Service) GetUser(ctx context.Context, id string) (domain.Account, error) {
	if _, err := s.requireOwner(ctx, false); err != nil {
		return domain.Account{}, err
	}
	u, err := store.GetUser(ctx, s.db, id)
	if err != nil {
		return domain.Account{}, err
	}
	return s.account(ctx, u)
}

// PatchUser edits an account (owner; optimistic revision). Disabling ends
// all sessions and streams of the account; the owner cannot be disabled.
// Moving the account to another group (#17: exactly one group per user)
// needs a recent step-up, is audited with the before/after group, and
// ends the account's open requests and streams (its access changed).
func (s *Service) PatchUser(ctx context.Context, id string, revision int64, p domain.UserPatch) (domain.Account, error) {
	cur, err := s.requireOwner(ctx, false)
	if err != nil {
		return domain.Account{}, err
	}
	target, err := store.GetUser(ctx, s.db, id)
	if err != nil {
		return domain.Account{}, err
	}
	moving := p.GroupID != nil && *p.GroupID != target.GroupID
	if moving {
		if err := s.requireRecent(cur); err != nil {
			return domain.Account{}, err
		}
	}
	if p.Status != nil && *p.Status != target.Status && target.Owner {
		return domain.Account{}, domain.ErrOwnerProtected
	}
	if p.DisplayName != nil {
		*p.DisplayName = strings.TrimSpace(*p.DisplayName)
	}
	if p.Email != nil {
		*p.Email = strings.TrimSpace(*p.Email)
	}
	if err := validateAccount("x", deref(p.DisplayName), deref(p.Email)); err != nil {
		return domain.Account{}, err
	}
	u, err := store.PatchUser(ctx, s.db, id, revision, p, s.now())
	if err != nil {
		return domain.Account{}, err
	}
	switch {
	case p.Status != nil && *p.Status != target.Status:
		action := "user.enable"
		if *p.Status == domain.UserDisabled {
			action = "user.disable"
			// Disabling revokes the account's API tokens for good (#31):
			// re-enabling the account does not bring them back.
			if err := s.revokeUserTokens(ctx, id, cur.user.ID, domain.RevokedUserDisabled); err != nil {
				return domain.Account{}, err
			}
			if err := s.endSessions(ctx, id, false); err != nil {
				return domain.Account{}, err
			}
		}
		s.record(ctx, action, OutcomeSuccess, cur.user.ID, "user", id, "")
	case moving:
		// A group move changes permissions (#17): end open streams and
		// stored responses so nothing keeps the old access.
		audit.SetDiff(ctx, map[string]string{"groupId": target.GroupID}, map[string]string{"groupId": u.GroupID})
		s.record(ctx, "user.group_change", OutcomeSuccess, cur.user.ID, "user", id, "")
		s.AccessChanged(ctx, []string{id})
	default:
		s.record(ctx, "user.update", OutcomeSuccess, cur.user.ID, "user", id, "")
	}
	return s.account(ctx, u)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// DeleteUser deletes an account (owner; never the owner).
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	cur, err := s.requireOwner(ctx, false)
	if err != nil {
		return err
	}
	if s.isOwner(id) {
		return domain.ErrOwnerProtected
	}
	tokens, err := store.UserAPITokenIDs(ctx, s.db, id)
	if err != nil {
		return err
	}
	// The account's API tokens are deleted with it (#31).
	if err := store.DeleteUser(ctx, s.db, id); err != nil {
		return err
	}
	s.record(ctx, "user.delete", OutcomeSuccess, cur.user.ID, "user", id, "")
	s.endTokens(ctx, tokens)
	return s.endSessions(ctx, id, false)
}

// RevokeUserSessions ends every session and stream of an account (owner).
func (s *Service) RevokeUserSessions(ctx context.Context, id string) error {
	cur, err := s.requireOwner(ctx, false)
	if err != nil {
		return err
	}
	if _, err := store.GetUser(ctx, s.db, id); err != nil {
		return err
	}
	s.record(ctx, "user.session_revoke", OutcomeSuccess, cur.user.ID, "user", id, "")
	return s.endSessions(ctx, id, true)
}

// ResetUserFactors removes an account's TOTP, passkeys and recovery codes
// (owner, recent authentication; not the owner, who uses owner recovery).
// Its sessions end and a new enrollment grace period starts. With
// revokeAPITokens the account's API tokens are revoked too (#31).
func (s *Service) ResetUserFactors(ctx context.Context, id string, revokeAPITokens bool) (domain.Account, error) {
	cur, err := s.requireOwner(ctx, true)
	if err != nil {
		return domain.Account{}, err
	}
	if s.isOwner(id) {
		return domain.Account{}, domain.ErrOwnerProtected
	}
	set, err := s.settings(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	now := s.now()
	deadline := now.Add(time.Duration(set.EnrollmentGraceHours) * time.Hour)
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := store.GetUser(ctx, tx, id); err != nil {
			return err
		}
		if err := clearFactors(ctx, tx, id, now); err != nil {
			return err
		}
		if err := store.SetEnrollmentDeadline(ctx, tx, id, &deadline, now); err != nil {
			return err
		}
		_, err := store.BumpSessionEpoch(ctx, tx, id, now)
		return err
	})
	if err != nil {
		return domain.Account{}, err
	}
	if revokeAPITokens {
		if err := s.revokeUserTokens(ctx, id, cur.user.ID, domain.RevokedCredentialReset); err != nil {
			return domain.Account{}, err
		}
	}
	s.record(ctx, "user.factor_reset", OutcomeSuccess, cur.user.ID, "user", id, "")
	if err := s.endSessions(ctx, id, false); err != nil {
		return domain.Account{}, err
	}
	u, err := store.GetUser(ctx, s.db, id)
	if err != nil {
		return domain.Account{}, err
	}
	return s.account(ctx, u)
}

// CreatePasswordReset issues a one-time password reset code for an account
// (owner, recent authentication; not the owner). Earlier unused codes of
// the account stop working. With revokeAPITokens the account's API tokens
// are revoked at once (#31).
func (s *Service) CreatePasswordReset(ctx context.Context, id string, revokeAPITokens bool) (domain.IssuedCode, error) {
	cur, err := s.requireOwner(ctx, true)
	if err != nil {
		return domain.IssuedCode{}, err
	}
	if s.isOwner(id) {
		return domain.IssuedCode{}, domain.ErrOwnerProtected
	}
	if _, err := store.GetUser(ctx, s.db, id); err != nil {
		return domain.IssuedCode{}, err
	}
	set, err := s.settings(ctx)
	if err != nil {
		return domain.IssuedCode{}, err
	}
	now := s.now()
	code := newLinkCode(PasswordResetCodePrefix)
	r := domain.AccountReset{ID: newID(), UserID: id, Kind: domain.ResetPassword, CreatedBy: cur.user.ID, CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(set.PasswordResetTTLHours) * time.Hour)}
	if err := store.InsertAccountReset(ctx, s.db, r, verifier(code)); err != nil {
		return domain.IssuedCode{}, err
	}
	if revokeAPITokens {
		if err := s.revokeUserTokens(ctx, id, cur.user.ID, domain.RevokedCredentialReset); err != nil {
			return domain.IssuedCode{}, err
		}
	}
	s.record(ctx, "user.password_reset_issue", OutcomeSuccess, cur.user.ID, "user", id, "")
	return domain.IssuedCode{ID: r.ID, Code: code, URL: s.link("/password-reset", code), ExpiresAt: r.ExpiresAt}, nil
}

// SecuritySettings returns the instance sign-in policy (owner).
func (s *Service) SecuritySettings(ctx context.Context) (domain.SecuritySettings, error) {
	if _, err := s.requireOwner(ctx, false); err != nil {
		return domain.SecuritySettings{}, err
	}
	return s.settings(ctx)
}

// UpdateSecuritySettings changes the sign-in policy (owner, recent
// authentication, optimistic revision). When the required factors change,
// every session ends so none keeps access the new policy would not grant:
// users sign in again and get a limited enrollment session (with a grace
// period; the owner has no deadline and cannot be locked out) until they
// enroll. The owner's own session continues, limited to enrollment when
// the owner lacks the new factors.
func (s *Service) UpdateSecuritySettings(ctx context.Context, revision int64, p domain.SecuritySettingsPatch) (domain.SecuritySettings, error) {
	cur, err := s.requireOwner(ctx, true)
	if err != nil {
		return domain.SecuritySettings{}, err
	}
	old, err := s.settings(ctx)
	if err != nil {
		return domain.SecuritySettings{}, err
	}
	next := old
	if p.StrictPasswords != nil {
		next.StrictPasswords = *p.StrictPasswords
	}
	if p.MinPasswordLength != nil {
		next.MinPasswordLength = *p.MinPasswordLength
	}
	if p.RequiredFactors != nil {
		next.RequiredFactors = *p.RequiredFactors
	}
	if p.EnrollmentGraceHours != nil {
		next.EnrollmentGraceHours = *p.EnrollmentGraceHours
	}
	if p.InvitationTTLHours != nil {
		next.InvitationTTLHours = *p.InvitationTTLHours
	}
	if p.PasswordResetTTLHours != nil {
		next.PasswordResetTTLHours = *p.PasswordResetTTLHours
	}
	if p.APITokensEnabled != nil {
		next.APITokensEnabled = *p.APITokensEnabled
	}
	if p.APITokenMaxDays != nil {
		next.APITokenMaxDays = *p.APITokenMaxDays
	}
	if p.APITokensNonExpiring != nil {
		next.APITokensNonExpiring = *p.APITokensNonExpiring
	}
	if p.AllowStaySignedIn != nil {
		next.AllowStaySignedIn = *p.AllowStaySignedIn
	}
	if err := validateSettings(next); err != nil {
		return domain.SecuritySettings{}, err
	}
	now := s.now()
	factorsChanged := next.RequiredFactors != old.RequiredFactors
	var saved domain.SecuritySettings
	var ended []string
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if saved, err = store.UpdateSecuritySettings(ctx, tx, revision, next, now); err != nil {
			return err
		}
		if old.AllowStaySignedIn && !next.AllowStaySignedIn {
			// Devices that stayed signed in fall back to the normal limits
			// (sessions past them end on their next request).
			if err := store.EndStaySignedIn(ctx, tx); err != nil {
				return err
			}
		}
		if !factorsChanged {
			return nil
		}
		if next.RequiredFactors != domain.FactorsNone {
			if err := store.SetEnrollmentDeadlineWhereUnset(ctx, tx, now.Add(time.Duration(next.EnrollmentGraceHours)*time.Hour), now); err != nil {
				return err
			}
		}
		ended, err = store.BumpAllSessionEpochs(ctx, tx, now)
		return err
	})
	if err != nil {
		return domain.SecuritySettings{}, err
	}
	s.record(ctx, "settings.security_update", OutcomeSuccess, cur.user.ID, "settings", "security", "")
	if (old.APITokensEnabled && !next.APITokensEnabled) || factorsChanged {
		// API tokens (#31) stop working instance-wide, or must meet the new
		// factor policy: end their open streams; the next request is
		// authenticated (or refused) under the new settings.
		s.hub.revokeAllTokens(authz.ErrSessionEnded)
	}
	if factorsChanged {
		owner, err := store.GetUser(ctx, s.db, cur.user.ID)
		if err != nil {
			return domain.SecuritySettings{}, err
		}
		enrolled, err := s.enrolled(ctx, owner)
		if err != nil {
			return domain.SecuritySettings{}, err
		}
		// The owner's session continues when this sign-in already satisfies
		// the new policy, or as an enrollment session when factors are
		// missing; when the owner has the factors but did not prove them in
		// this session, it ends like every other (sign in again).
		keepOwner := int64(-1)
		switch ev := evaluate(next.RequiredFactors, enrolled, cur.proven, false); ev.stage {
		case domain.StageAuthenticated, domain.StageEnrollment:
			if err := s.rotate(ctx, cur, ev.stage, owner.SessionEpoch, cur.proven); err != nil {
				return domain.SecuritySettings{}, err
			}
			keepOwner = owner.SessionEpoch
		}
		for _, id := range ended {
			s.forget(ctx, id)
			keep := int64(-1)
			if id == owner.ID {
				keep = keepOwner
			}
			s.hub.revoke(id, keep)
		}
	}
	return saved, nil
}

func validateSettings(s domain.SecuritySettings) error {
	switch {
	case !s.RequiredFactors.Valid():
		return &domain.FieldError{Field: "requiredFactors", Message: "one of none, totp, passkey, either, both"}
	case s.MinPasswordLength < 8 || s.MinPasswordLength > 64:
		return &domain.FieldError{Field: "minPasswordLength", Message: "8..64"}
	case s.EnrollmentGraceHours < 1 || s.EnrollmentGraceHours > 720:
		return &domain.FieldError{Field: "enrollmentGraceHours", Message: "1..720"}
	case s.InvitationTTLHours < 1 || s.InvitationTTLHours > 720:
		return &domain.FieldError{Field: "invitationTtlHours", Message: "1..720"}
	case s.PasswordResetTTLHours < 1 || s.PasswordResetTTLHours > 168:
		return &domain.FieldError{Field: "passwordResetTtlHours", Message: "1..168"}
	case s.APITokenMaxDays < 1 || s.APITokenMaxDays > 3650:
		return &domain.FieldError{Field: "apiTokenMaxLifetimeDays", Message: "1..3650"}
	}
	return nil
}

// IssueOwnerRecovery is the break-glass path for a locked-out owner
// (docker-manager owner-recovery, run with access to the data volume):
// it issues a one-time owner-recovery code, valid for OwnerRecoveryTTL,
// and ends every owner session (the running manager rejects them on the
// next request and closes open streams within StreamSweepInterval).
// Redeeming the code (POST /api/v1/auth/password-resets/redemptions) sets a
// new password and removes the owner's TOTP, passkeys and recovery codes;
// the owner then signs in and enrolls factors again.
func IssueOwnerRecovery(ctx context.Context, db *bun.DB, audit Auditor, now time.Time, publicURL *url.URL) (domain.IssuedCode, error) {
	id, ok, err := store.OwnerID(ctx, db)
	if err != nil {
		return domain.IssuedCode{}, err
	}
	if !ok {
		return domain.IssuedCode{}, errors.New("first-run setup has not created an owner yet; open Docker Manager in a browser to set it up")
	}
	code := newLinkCode(OwnerRecoveryCodePrefix)
	r := domain.AccountReset{ID: newID(), UserID: id, Kind: domain.ResetOwnerRecovery, CreatedAt: now.UTC(), ExpiresAt: now.UTC().Add(OwnerRecoveryTTL)}
	err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.InsertAccountReset(ctx, tx, r, verifier(code)); err != nil {
			return err
		}
		_, err := store.BumpSessionEpoch(ctx, tx, id, now)
		return err
	})
	if err != nil {
		return domain.IssuedCode{}, err
	}
	if audit != nil {
		audit.Record(ctx, AuditEvent{Action: "owner.recovery_issue", Outcome: OutcomeSuccess, TargetType: "user", TargetID: id, Reason: "cli"})
	}
	svc := &Service{publicURL: publicURL}
	return domain.IssuedCode{ID: r.ID, Code: code, URL: svc.link("/password-reset", code), ExpiresAt: r.ExpiresAt}, nil
}
