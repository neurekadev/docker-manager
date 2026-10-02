package auth

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/ids"
	"github.com/neurekadev/docker-manager/internal/manager/auth/passkey"
	"github.com/neurekadev/docker-manager/internal/manager/auth/totp"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

func newID() string { return ids.New() }

// BeginTOTPEnrollment creates a pending TOTP secret for the caller. The
// secret is returned once and stored sealed; it becomes active only after
// VerifyTOTPEnrollment.
func (s *Service) BeginTOTPEnrollment(ctx context.Context) (domain.TOTPEnrollment, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return domain.TOTPEnrollment{}, err
	}
	if cur.user.TOTPEnabled {
		return domain.TOTPEnrollment{}, domain.ErrTOTPAlreadyEnabled
	}
	key, err := s.kit.NewTOTP(cur.user.Username)
	if err != nil {
		return domain.TOTPEnrollment{}, err
	}
	sealed, err := s.keyring.Seal([]byte(key.Secret), totpPendingContext(cur.user.ID))
	if err != nil {
		return domain.TOTPEnrollment{}, err
	}
	expires := s.now().Add(TOTPEnrollmentTTL)
	if err := store.SetTOTPPending(ctx, s.db, cur.user.ID, sealed, expires, s.now()); err != nil {
		return domain.TOTPEnrollment{}, err
	}
	return domain.TOTPEnrollment{Secret: key.Secret, URI: key.URI, ExpiresAt: expires}, nil
}

// VerifyTOTPEnrollment activates the pending secret when code matches.
// In a limited enrollment session it may complete the required factors
// and upgrade the session.
func (s *Service) VerifyTOTPEnrollment(ctx context.Context, code string) (domain.SessionState, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return domain.SessionState{}, err
	}
	att, err := s.begin(ctx, s.accountLimit(userKey(cur.user.ID)))
	if err != nil {
		return domain.SessionState{}, err
	}
	defer att.release()
	_, creds, err := store.GetUserWithCredentials(ctx, s.db, cur.user.ID)
	if err != nil {
		return domain.SessionState{}, err
	}
	if creds.TOTPSeedSealed != "" {
		return domain.SessionState{}, domain.ErrTOTPAlreadyEnabled
	}
	if creds.TOTPPendingSealed == "" || creds.TOTPPendingExpiry == nil || !s.now().Before(*creds.TOTPPendingExpiry) {
		return domain.SessionState{}, domain.ErrNoPendingFlow
	}
	seed, err := s.keyring.Open(creds.TOTPPendingSealed, totpPendingContext(cur.user.ID))
	if err != nil {
		return domain.SessionState{}, fmt.Errorf("auth: open pending totp seed: %w", err)
	}
	step, ok, err := s.kit.VerifyTOTP(string(seed), code, 0)
	if err != nil && !errors.Is(err, totp.ErrMalformed) {
		return domain.SessionState{}, err
	}
	if !ok {
		att.fail()
		return domain.SessionState{}, &domain.FieldError{Field: "code", Message: "the code does not match; check the authenticator app's clock and try the current code"}
	}
	active, err := s.keyring.Seal(seed, totpSeedContext(cur.user.ID))
	if err != nil {
		return domain.SessionState{}, err
	}
	if err := store.ActivateTOTP(ctx, s.db, cur.user.ID, creds.TOTPPendingSealed, active, step, s.now()); err != nil {
		return domain.SessionState{}, err
	}
	s.record(ctx, "auth.totp_enable", OutcomeSuccess, cur.user.ID, "user", cur.user.ID, "")
	cur.user.TOTPEnabled = true
	return s.afterEnrollment(ctx, cur, fTOTP)
}

// afterEnrollment renews the caller's token after a new factor and, for a
// limited enrollment session, upgrades it once the policy is satisfied.
func (s *Service) afterEnrollment(ctx context.Context, cur *current, added factorSet) (domain.SessionState, error) {
	proven := cur.proven | added
	stage := cur.stage
	if stage == domain.StageEnrollment {
		set, err := s.settings(ctx)
		if err != nil {
			return domain.SessionState{}, err
		}
		enrolled, err := s.enrolled(ctx, cur.user)
		if err != nil {
			return domain.SessionState{}, err
		}
		if evaluate(set.RequiredFactors, enrolled, proven, false).stage == domain.StageAuthenticated ||
			enrollmentComplete(set.RequiredFactors, enrolled) {
			stage = domain.StageAuthenticated
			_ = store.SetEnrollmentDeadline(ctx, s.db, cur.user.ID, nil, s.now())
			s.record(ctx, "auth.enrollment_complete", OutcomeSuccess, cur.user.ID, "user", cur.user.ID, "")
		}
	}
	if err := s.rotate(ctx, cur, stage, cur.user.SessionEpoch, proven); err != nil {
		return domain.SessionState{}, err
	}
	return s.state(ctx)
}

// DeleteTOTP removes the caller's TOTP (recent authentication; refused when
// the policy would become unsatisfiable). Other sessions end.
func (s *Service) DeleteTOTP(ctx context.Context) error {
	cur, err := s.session(ctx, false)
	if err != nil {
		return err
	}
	if err := s.requireRecent(cur); err != nil {
		return err
	}
	if !cur.user.TOTPEnabled {
		return nil
	}
	if err := s.checkRemoval(ctx, cur.user, fTOTP); err != nil {
		return err
	}
	var epoch int64
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.ClearTOTP(ctx, tx, cur.user.ID, s.now()); err != nil {
			return err
		}
		epoch, err = store.BumpSessionEpoch(ctx, tx, cur.user.ID, s.now())
		return err
	})
	if err != nil {
		return err
	}
	return s.afterRemoval(ctx, cur, epoch, fTOTP, "auth.totp_disable")
}

// checkRemoval refuses removing factor when the remaining factors could not
// satisfy the policy (or the account could not sign in at all).
func (s *Service) checkRemoval(ctx context.Context, u domain.User, factor factorSet) error {
	set, err := s.settings(ctx)
	if err != nil {
		return err
	}
	enrolled, err := s.enrolled(ctx, u)
	if err != nil {
		return err
	}
	if !enrollmentComplete(set.RequiredFactors, enrolled&^factor) {
		return domain.ErrFactorRequired
	}
	return nil
}

func (s *Service) afterRemoval(ctx context.Context, cur *current, epoch int64, removed factorSet, action string) error {
	if err := s.rotate(ctx, cur, cur.stage, epoch, cur.proven&^removed); err != nil {
		return err
	}
	s.record(ctx, action, OutcomeSuccess, cur.user.ID, "user", cur.user.ID, "")
	s.forget(ctx, cur.user.ID)
	s.hub.revoke(cur.user.ID, epoch)
	return nil
}

// webauthnUser loads u's passkeys as a go-webauthn user.
func (s *Service) webauthnUser(ctx context.Context, u domain.User) (*passkey.User, []domain.Passkey, error) {
	pks, err := store.ListPasskeys(ctx, s.db, u.ID)
	if err != nil {
		return nil, nil, err
	}
	wu := &passkey.User{Handle: u.WebAuthnHandle, Name: u.Username, DisplayName: u.DisplayName}
	for _, p := range pks {
		var c webauthn.Credential
		if err := json.Unmarshal(p.Credential, &c); err != nil {
			return nil, nil, fmt.Errorf("auth: decode passkey %s: %w", p.ID, err)
		}
		wu.Credentials = append(wu.Credentials, c)
	}
	return wu, pks, nil
}

// PasskeyRegistrationOptions begins adding a passkey for the caller.
func (s *Service) PasskeyRegistrationOptions(ctx context.Context) (json.RawMessage, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return nil, err
	}
	wu, _, err := s.webauthnUser(ctx, cur.user)
	if err != nil {
		return nil, err
	}
	creation, sd, err := s.kit.RP.BeginRegistration(wu)
	if err != nil {
		return nil, err
	}
	return s.storeCeremony(ctx, kWAReg, sd, creation, "")
}

func (s *Service) storeCeremony(ctx context.Context, key string, sd *webauthn.SessionData, options any, purpose string) (json.RawMessage, error) {
	raw, err := json.Marshal(sd)
	if err != nil {
		return nil, err
	}
	s.kit.Sessions.Put(ctx, key, raw)
	if purpose != "" {
		s.kit.Sessions.Put(ctx, kWAPurpose, purpose)
	}
	return json.Marshal(options)
}

func (s *Service) takeCeremony(ctx context.Context, key string) (webauthn.SessionData, error) {
	raw := s.kit.Sessions.PopBytes(ctx, key)
	var sd webauthn.SessionData
	if len(raw) == 0 {
		return sd, domain.ErrNoPendingFlow
	}
	if err := json.Unmarshal(raw, &sd); err != nil {
		return sd, domain.ErrNoPendingFlow
	}
	return sd, nil
}

// PasskeyRegistrationVerification verifies the browser's attestation and
// stores the passkey under name.
func (s *Service) PasskeyRegistrationVerification(ctx context.Context, name string, response []byte) (domain.Passkey, domain.SessionState, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return domain.Passkey{}, domain.SessionState{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	sd, err := s.takeCeremony(ctx, kWAReg)
	if err != nil {
		return domain.Passkey{}, domain.SessionState{}, err
	}
	wu, _, err := s.webauthnUser(ctx, cur.user)
	if err != nil {
		return domain.Passkey{}, domain.SessionState{}, err
	}
	cred, err := s.kit.RP.FinishRegistration(wu, sd, response)
	if err != nil {
		s.record(ctx, "auth.passkey_add", OutcomeFailure, cur.user.ID, "user", cur.user.ID, "verification")
		return domain.Passkey{}, domain.SessionState{}, domain.ErrPasskeyVerification
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return domain.Passkey{}, domain.SessionState{}, err
	}
	pk := domain.Passkey{
		ID: newID(), UserID: cur.user.ID, CredentialID: cred.ID, Name: name, Credential: raw,
		SignCount: cred.Authenticator.SignCount, BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState,
		AAGUID: aaguidString(cred.Authenticator.AAGUID), CreatedAt: s.now(),
	}
	if err := store.InsertPasskey(ctx, s.db, pk); err != nil {
		return domain.Passkey{}, domain.SessionState{}, err
	}
	s.record(ctx, "auth.passkey_add", OutcomeSuccess, cur.user.ID, "passkey", pk.ID, "")
	st, err := s.afterEnrollment(ctx, cur, fPasskey)
	return pk, st, err
}

func aaguidString(b []byte) string {
	if u, err := uuid.FromBytes(b); err == nil {
		return u.String()
	}
	return hex.EncodeToString(b)
}

// PasskeyAuthenticationOptions begins a passkey assertion: a username-less
// sign-in, the second factor of a pending password sign-in, or a step-up
// (purpose step_up, signed-in callers).
func (s *Service) PasskeyAuthenticationOptions(ctx context.Context, purpose domain.PasskeyPurpose) (json.RawMessage, error) {
	if purpose == domain.PasskeyStepUp {
		cur, err := s.session(ctx, true)
		if err != nil {
			return nil, err
		}
		wu, _, err := s.webauthnUser(ctx, cur.user)
		if err != nil {
			return nil, err
		}
		if len(wu.Credentials) == 0 {
			return nil, domain.ErrFactorRequired
		}
		a, sd, err := s.kit.RP.BeginUserLogin(wu)
		if err != nil {
			return nil, err
		}
		return s.storeCeremony(ctx, kWALogin, sd, a, string(domain.PasskeyStepUp))
	}
	// Anonymous ceremonies create session state: bound them per client IP.
	if !s.kit.IPLimit.Take(ipKey(ctx)) {
		return nil, &domain.RateLimitedError{RetryAfter: max(s.kit.IPLimit.RetryAfter(ipKey(ctx)), time.Second)}
	}
	if u, _, _, err := s.pending(ctx); err == nil {
		wu, _, err := s.webauthnUser(ctx, u)
		if err != nil {
			return nil, err
		}
		if len(wu.Credentials) > 0 {
			a, sd, err := s.kit.RP.BeginUserLogin(wu)
			if err != nil {
				return nil, err
			}
			return s.storeCeremony(ctx, kWALogin, sd, a, "second_factor")
		}
	}
	a, sd, err := s.kit.RP.BeginLogin()
	if err != nil {
		return nil, err
	}
	return s.storeCeremony(ctx, kWALogin, sd, a, string(domain.PasskeySignIn))
}

// lookupPasskeyUser resolves the account of a discoverable assertion.
func (s *Service) lookupPasskeyUser(ctx context.Context) passkey.Lookup {
	return func(credentialID, handle []byte) (*passkey.User, error) {
		u, err := store.GetUserByHandle(ctx, s.db, handle)
		if err != nil || !u.Active() {
			return nil, passkey.ErrUnknownCredential
		}
		wu, _, err := s.webauthnUser(ctx, u)
		if err != nil {
			return nil, err
		}
		for _, c := range wu.Credentials {
			if bytes.Equal(c.ID, credentialID) {
				return wu, nil
			}
		}
		return nil, passkey.ErrUnknownCredential
	}
}

// recordPasskeyUse stores the updated counter, flags and use time.
func (s *Service) recordPasskeyUse(ctx context.Context, userID string, cred *webauthn.Credential) error {
	pks, err := store.ListPasskeys(ctx, s.db, userID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	for _, p := range pks {
		if bytes.Equal(p.CredentialID, cred.ID) {
			return store.UpdatePasskeyUse(ctx, s.db, p.ID, raw, cred.Authenticator.SignCount, cred.Flags.BackupState, s.now())
		}
	}
	return domain.ErrPasskeyNotFound
}

// PasskeyAuthenticationVerification verifies an assertion begun by
// PasskeyAuthenticationOptions and signs in (or completes the pending
// sign-in, or records a step-up). stay asks for "Stay signed in" on a
// passkey sign-in; a pending sign-in keeps the choice of its first step.
func (s *Service) PasskeyAuthenticationVerification(ctx context.Context, response []byte, stay bool) (domain.SessionState, error) {
	purpose := s.kit.Sessions.GetString(ctx, kWAPurpose)
	if purpose == string(domain.PasskeyStepUp) {
		// A step-up ceremony finished here counts like POST /auth/step-ups.
		return s.StepUp(ctx, domain.StepUp{PasskeyResponse: response})
	}
	att, err := s.begin(ctx)
	if err != nil {
		return domain.SessionState{}, err
	}
	defer att.release()
	sd, err := s.takeCeremony(ctx, kWALogin)
	s.kit.Sessions.Remove(ctx, kWAPurpose)
	if err != nil {
		return domain.SessionState{}, err
	}
	var (
		user   domain.User
		proven factorSet
		want   *passkey.User
	)
	if purpose == "second_factor" {
		pu, _, pproven, err := s.pending(ctx)
		if err != nil {
			return domain.SessionState{}, err
		}
		if want, _, err = s.webauthnUser(ctx, pu); err != nil {
			return domain.SessionState{}, err
		}
		user, proven, stay = pu, pproven, s.pendingStay(ctx)
	}
	wu, cred, err := s.kit.RP.FinishLogin(sd, response, want, s.lookupPasskeyUser(ctx))
	if err != nil {
		att.fail()
		s.record(ctx, "auth.sign_in", OutcomeFailure, "", "", "", "passkey")
		return domain.SessionState{}, domain.ErrInvalidCredentials
	}
	if want == nil {
		if user, err = store.GetUserByHandle(ctx, s.db, wu.Handle); err != nil {
			return domain.SessionState{}, domain.ErrInvalidCredentials
		}
	}
	if err := s.recordPasskeyUse(ctx, user.ID, cred); err != nil {
		return domain.SessionState{}, err
	}
	return s.advance(ctx, user, proven|fPasskey, false, stay)
}

// finishStepUpPasskey verifies a step-up assertion for cur's account.
func (s *Service) finishStepUpPasskey(ctx context.Context, cur *current, response []byte) error {
	if s.kit.Sessions.GetString(ctx, kWAPurpose) != string(domain.PasskeyStepUp) {
		return domain.ErrNoPendingFlow
	}
	sd, err := s.takeCeremony(ctx, kWALogin)
	s.kit.Sessions.Remove(ctx, kWAPurpose)
	if err != nil {
		return err
	}
	wu, _, err := s.webauthnUser(ctx, cur.user)
	if err != nil {
		return err
	}
	_, cred, err := s.kit.RP.FinishLogin(sd, response, wu, nil)
	if err != nil { // StepUp counts the failure
		s.record(ctx, "auth.step_up", OutcomeFailure, cur.user.ID, "user", cur.user.ID, "passkey")
		return domain.ErrInvalidCredentials
	}
	return s.recordPasskeyUse(ctx, cur.user.ID, cred)
}

// ListMyPasskeys lists the caller's passkeys.
func (s *Service) ListMyPasskeys(ctx context.Context) ([]domain.Passkey, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return nil, err
	}
	return store.ListPasskeys(ctx, s.db, cur.user.ID)
}

// RenameMyPasskey changes the label of one of the caller's passkeys (an
// empty name becomes "Passkey", as at registration). Renaming changes no
// credential, so it needs no step-up and ends no session.
func (s *Service) RenameMyPasskey(ctx context.Context, id, name string) (domain.Passkey, error) {
	cur, err := s.session(ctx, true)
	if err != nil {
		return domain.Passkey{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	if err := store.RenamePasskey(ctx, s.db, cur.user.ID, id, name); err != nil {
		return domain.Passkey{}, err
	}
	pks, err := store.ListPasskeys(ctx, s.db, cur.user.ID)
	if err != nil {
		return domain.Passkey{}, err
	}
	for _, p := range pks {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.Passkey{}, domain.ErrPasskeyNotFound
}

// DeleteMyPasskey revokes one of the caller's passkeys (recent
// authentication; refused when the policy would become unsatisfiable).
// Other sessions end.
func (s *Service) DeleteMyPasskey(ctx context.Context, id string) error {
	cur, err := s.session(ctx, false)
	if err != nil {
		return err
	}
	if err := s.requireRecent(cur); err != nil {
		return err
	}
	pks, err := store.ListPasskeys(ctx, s.db, cur.user.ID)
	if err != nil {
		return err
	}
	found := false
	for _, p := range pks {
		found = found || p.ID == id
	}
	if !found {
		return domain.ErrPasskeyNotFound
	}
	removed := factorSet(0)
	if len(pks) == 1 {
		removed = fPasskey
		if err := s.checkRemoval(ctx, cur.user, fPasskey); err != nil {
			return err
		}
	}
	var epoch int64
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := store.DeletePasskey(ctx, tx, cur.user.ID, id); err != nil {
			return err
		}
		epoch, err = store.BumpSessionEpoch(ctx, tx, cur.user.ID, s.now())
		return err
	})
	if err != nil {
		return err
	}
	return s.afterRemoval(ctx, cur, epoch, removed, "auth.passkey_remove")
}
