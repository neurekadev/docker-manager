package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/dockyard/internal/domain"
)

type identityAPI struct {
	svc IdentityService
	// backups reports a running backup import in the setup status (#24).
	backups BackupService
}

func (h *identityAPI) service() (IdentityService, error) {
	if h.svc == nil {
		return nil, Unavailable(CodeUnavailable, "the identity service is not available")
	}
	return h.svc, nil
}

// cookieOnly is the security requirement of browser-session routes: API
// tokens (#31) never reach sign-in, factor management or owner
// administration.
var cookieOnly = []map[string][]string{{SecurityCookie: {}}}

type emptyOutput struct{}

// errsSignIn are the errors of sign-in steps.
var errsSignIn = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests}

func registerIdentity(a huma.API, deps Deps) {
	h := &identityAPI{svc: deps.Identity, backups: deps.Backups}
	registerSetup(a, h)
	registerSignIn(a, h)
	registerFactors(a, h)
	registerAccountAdmin(a, h)
}

func registerSetup(a huma.API, h *identityAPI) {
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-setup-status", Method: http.MethodGet, Path: BasePath + "/setup/status",
			Summary: "First-run setup status",
			Description: "Whether the instance owner exists, and whether this request could complete setup " +
				"(it must reach DockYard over HTTPS on DOCKYARD_PUBLIC_URL; explanation says what to fix otherwise).",
			Tags: []string{tagSetup}, Errors: []int{http.StatusServiceUnavailable},
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*setupStatusOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.SetupStatus(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		out := &setupStatusOutput{}
		out.Body.SetupComplete, out.Body.SecureOrigin, out.Body.Explanation = st.Complete, st.SecureOrigin, st.Explanation
		if !st.Complete && h.backups != nil {
			if imp, err := h.backups.LatestImport(ctx); err == nil && imp != nil {
				out.Body.BackupImport = &SetupBackupImport{JobID: imp.JobID, State: string(imp.State), ErrorCode: imp.ErrorClass,
					Recovery: imp.Message, RestartPending: imp.RestartPending}
			}
		}
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-setup-owner", Method: http.MethodPost, Path: BasePath + "/setup/owner",
			Summary: "Create the instance owner (first-run setup)", DefaultStatus: http.StatusCreated,
			Description: "Single-use and race-safe: of concurrent requests exactly one creates the owner, the others get 409 setup_complete, " +
				"as does every later request. Refused with 403 insecure_origin unless the request reached DockYard over HTTPS on DOCKYARD_PUBLIC_URL " +
				"(through a trusted reverse proxy) or the explicit http://localhost development mode is used. " +
				"Signs the owner in: sets the session cookie and returns the session.",
			Tags: []string{tagSetup}, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *setupOwnerInput) (*sessionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.SetupOwner(ctx, domain.OwnerSetup{Username: in.Body.Username, DisplayName: in.Body.DisplayName, Email: in.Body.Email, Password: in.Body.Password})
		if err != nil {
			return nil, identityError(err)
		}
		return &sessionOutput{Body: newSession(st)}, nil
	})
}

func registerSignIn(a huma.API, h *identityAPI) {
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-auth-session", Method: http.MethodPost, Path: BasePath + "/auth/session",
			Summary: "Sign in",
			Description: "Starts a sign-in with username and password, or continues a pending one with totpCode. " +
				"Answers the session: authenticated, second_factor_required (send one of factors) or enrollment_required " +
				"(a limited session that may only enroll the factors the policy requires). Unknown accounts, wrong passwords and disabled " +
				"accounts all answer 401 invalid_credentials with the same cost. Failed attempts are throttled per client IP and per account (429). " +
				"Sets the session cookie (__Host-dockyard_session, HttpOnly, Secure, SameSite=Strict); the token changes at every privilege change.",
			Tags: []string{tagAuth}, Errors: errsSignIn,
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *createSessionInput) (*sessionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.SignIn(ctx, in.Body.Username, in.Body.Password, in.Body.TOTPCode)
		if err != nil {
			return nil, identityError(err)
		}
		return &sessionOutput{Body: newSession(st)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-auth-session", Method: http.MethodGet, Path: BasePath + "/auth/session",
			Summary: "Current session", Description: "The caller's session, including a pending sign-in or an enrollment session. 401 without one.",
			Tags: []string{tagAuth}, Security: cookieOnly,
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*sessionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.CurrentSession(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		return &sessionOutput{Body: newSession(st)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-auth-session", Method: http.MethodDelete, Path: BasePath + "/auth/session",
			Summary: "Sign out", Description: "Ends the caller's session (also a pending sign-in) and clears the cookie.",
			Tags: []string{tagAuth}, Security: cookieOnly, DefaultStatus: http.StatusNoContent,
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.SignOut(ctx); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-auth-step-up", Method: http.MethodPost, Path: BasePath + "/auth/step-ups",
			Summary: "Re-authenticate (step-up)",
			Description: "Proves the caller's identity again before sensitive changes (security settings, invitations, password, factor removal and resets, " +
				"which answer 403 step_up_required otherwise): the password plus a TOTP code when TOTP is enabled, or a passkey assertion from " +
				"authentication options requested with purpose step_up. Valid for 10 minutes; renews the session token.",
			Tags: []string{tagAuth}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, in *stepUpInput) (*sessionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.StepUp(ctx, domain.StepUp{Password: in.Body.Password, TOTPCode: in.Body.TOTPCode, PasskeyResponse: in.Body.Credential})
		if err != nil {
			return nil, identityError(err)
		}
		return &sessionOutput{Body: newSession(st)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-recovery-code-redemption", Method: http.MethodPost, Path: BasePath + "/auth/recovery-codes/redemptions",
			Summary: "Complete a sign-in with a recovery code",
			Description: "Uses one of the account's one-time recovery codes instead of TOTP or a passkey to complete a pending password sign-in. " +
				"Each code works once. 409 no_pending_flow without a pending sign-in.",
			Tags: []string{tagAuth}, Errors: errsSignIn,
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *codeInput) (*sessionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.RedeemRecoveryCode(ctx, in.Body.Code)
		if err != nil {
			return nil, identityError(err)
		}
		return &sessionOutput{Body: newSession(st)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-password-reset-redemption", Method: http.MethodPost, Path: BasePath + "/auth/password-resets/redemptions",
			Summary: "Set a new password with a reset code",
			Description: "Redeems an owner-issued password-reset code, or an owner-recovery code from `dockyard-manager owner-recovery` (which also removes " +
				"the owner's TOTP, passkeys and recovery codes). Codes are single use; unknown, expired and used codes answer 400 invalid_code alike. " +
				"A password that fails the policy answers 422 and leaves the code valid. Every session of the account ends; sign in afterwards.",
			Tags: []string{tagAuth}, DefaultStatus: http.StatusNoContent,
			Errors: []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *passwordResetRedemptionInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.RedeemPasswordReset(ctx, in.Body.Code, in.Body.NewPassword, in.Body.RevokeAPITokens); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-passkey-authentication-options", Method: http.MethodPost, Path: BasePath + "/auth/passkeys/authentication-options",
			Summary: "Begin a passkey sign-in",
			Description: "Returns WebAuthn request options for navigator.credentials.get(). Without a session: a username-less sign-in with any passkey " +
				"(the options list no credentials). During a pending password sign-in: the account's passkeys as second factor. With purpose step_up: " +
				"re-authentication of the signed-in user. The challenge is single use and expires after 5 minutes.",
			Tags: []string{tagAuth}, Errors: []int{http.StatusUnauthorized, http.StatusConflict, http.StatusTooManyRequests},
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *passkeyAuthOptionsInput) (*passkeyOptionsOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		purpose := domain.PasskeySignIn
		if in.Body != nil && in.Body.Purpose == string(domain.PasskeyStepUp) {
			purpose = domain.PasskeyStepUp
		}
		raw, err := svc.PasskeyAuthenticationOptions(ctx, purpose)
		if err != nil {
			return nil, identityError(err)
		}
		return &passkeyOptionsOutput{Body: raw}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-passkey-authentication-verification", Method: http.MethodPost, Path: BasePath + "/auth/passkeys/authentication-verifications",
			Summary: "Finish a passkey sign-in",
			Description: "Verifies the assertion against the pending challenge, DockYard's RP ID and origin (DOCKYARD_PUBLIC_URL), user verification and " +
				"the signature counter, then signs in (or completes the pending sign-in or step-up). Failures answer 401 invalid_credentials.",
			Tags: []string{tagAuth}, Errors: errsSignIn,
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *passkeyAssertionInput) (*sessionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.PasskeyAuthenticationVerification(ctx, in.Body.Credential)
		if err != nil {
			return nil, identityError(err)
		}
		return &sessionOutput{Body: newSession(st)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-invitation-redemption", Method: http.MethodPost, Path: BasePath + "/invitations/redemptions",
			Summary: "Create an account from an invitation",
			Description: "The only way to create an account besides first-run setup. The code is single use: unknown, expired, revoked and used codes " +
				"(and a mismatching email for a bound invitation) answer 400 invalid_code alike, and failures are throttled per client IP. " +
				"The account joins the default group (initially Restricted: no access until the owner grants some) and is signed in; " +
				"when the policy requires factors the session is limited to enrolling them.",
			Tags: []string{tagInvites}, DefaultStatus: http.StatusCreated,
			Errors: []int{http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
		},
		Capability: CapabilityPublic, Scope: ScopeNone,
	}, func(ctx context.Context, in *redeemInvitationInput) (*sessionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.RedeemInvitation(ctx, domain.InvitationRedemption{Code: in.Body.Code, Username: in.Body.Username,
			DisplayName: in.Body.DisplayName, Email: in.Body.Email, Password: in.Body.Password})
		if err != nil {
			return nil, identityError(err)
		}
		return &sessionOutput{Body: newSession(st)}, nil
	})
}

func registerFactors(a huma.API, h *identityAPI) {
	enroll := "Available to limited enrollment sessions."
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-totp-enrollment", Method: http.MethodPost, Path: BasePath + "/auth/totp/enrollments",
			Summary: "Start TOTP enrollment", DefaultStatus: http.StatusCreated,
			Description: "Creates a new TOTP secret (RFC 6238, SHA-1, 6 digits, 30 s) and returns it once, as base32 and otpauth:// URI. It becomes active " +
				"only after a correct code is confirmed within 10 minutes. The secret is stored encrypted and never shown again. " + enroll,
			Tags: []string{tagAuth}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusConflict},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*totpEnrollmentOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		e, err := svc.BeginTOTPEnrollment(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		out := &totpEnrollmentOutput{}
		out.Body.Secret, out.Body.URI, out.Body.ExpiresAt = e.Secret, e.URI, e.ExpiresAt
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-totp-enrollment-verification", Method: http.MethodPost, Path: BasePath + "/auth/totp/enrollments/verifications",
			Summary: "Confirm TOTP enrollment",
			Description: "Activates the pending secret when the code matches (±30 s). In an enrollment session this may complete the required " +
				"factors and upgrade the session to full access. " + enroll,
			Tags: []string{tagAuth}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, in *codeInput) (*sessionOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.VerifyTOTPEnrollment(ctx, in.Body.Code)
		if err != nil {
			return nil, identityError(err)
		}
		return &sessionOutput{Body: newSession(st)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-totp", Method: http.MethodDelete, Path: BasePath + "/auth/totp",
			Summary: "Remove TOTP", DefaultStatus: http.StatusNoContent,
			Description: "Requires a recent step-up. 409 factor_required when the sign-in policy needs TOTP and no alternative is enrolled. " +
				"The caller's other sessions end.",
			Tags: []string{tagAuth}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusConflict},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.DeleteTOTP(ctx); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-passkey-registration-options", Method: http.MethodPost, Path: BasePath + "/auth/passkeys/registration-options",
			Summary: "Begin passkey registration",
			Description: "Returns WebAuthn creation options for navigator.credentials.create(): a discoverable credential with user verification for " +
				"the relying party of DOCKYARD_PUBLIC_URL (RP ID = its host name). Passkeys stop working if that host name changes. " + enroll,
			Tags: []string{tagAuth}, Security: cookieOnly, Errors: []int{http.StatusForbidden},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*passkeyOptionsOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		raw, err := svc.PasskeyRegistrationOptions(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		return &passkeyOptionsOutput{Body: raw}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-passkey-registration-verification", Method: http.MethodPost, Path: BasePath + "/auth/passkeys/registration-verifications",
			Summary: "Finish passkey registration", DefaultStatus: http.StatusCreated,
			Description: "Verifies the attestation (challenge, origin, RP ID, user verification) and stores the passkey. In an enrollment session " +
				"this may complete the required factors. " + enroll,
			Tags: []string{tagAuth}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, in *passkeyRegistrationInput) (*passkeyRegistrationOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		pk, st, err := svc.PasskeyRegistrationVerification(ctx, in.Body.Name, in.Body.Credential)
		if err != nil {
			return nil, identityError(err)
		}
		out := &passkeyRegistrationOutput{}
		out.Body.Passkey, out.Body.Session = newPasskey(pk), newSession(st)
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-my-passkeys", Method: http.MethodGet, Path: BasePath + "/me/passkeys",
			Summary: "List my passkeys", Description: "All of the caller's passkeys (a short list; no pagination). " + enroll,
			Tags: []string{tagMe}, Security: cookieOnly,
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*passkeyListOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		pks, err := svc.ListMyPasskeys(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		items := make([]Passkey, 0, len(pks))
		for _, p := range pks {
			items = append(items, newPasskey(p))
		}
		return &passkeyListOutput{Body: NewPage(items, "", Total(int64(len(items))))}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-my-passkey", Method: http.MethodPatch, Path: BasePath + "/me/passkeys/{credentialId}",
			Summary:     "Rename one of my passkeys",
			Description: "Changes only the label shown in the passkey list; the credential is unchanged (no step-up, no session ends). " + enroll,
			Tags:        []string{tagMe}, Security: cookieOnly, Errors: []int{http.StatusNotFound, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, in *passkeyRenameInput) (*passkeyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		pk, err := svc.RenameMyPasskey(ctx, in.CredentialID, in.Body.Name)
		if err != nil {
			return nil, identityError(err)
		}
		return &passkeyOutput{Body: newPasskey(pk)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-my-passkey", Method: http.MethodDelete, Path: BasePath + "/me/passkeys/{credentialId}",
			Summary: "Revoke one of my passkeys", DefaultStatus: http.StatusNoContent,
			Description: "Requires a recent step-up. 409 factor_required when the sign-in policy needs a passkey and this is the last one. " +
				"The caller's other sessions end.",
			Tags: []string{tagMe}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, in *passkeyIDInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.DeleteMyPasskey(ctx, in.CredentialID); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-me", Method: http.MethodGet, Path: BasePath + "/me",
			Summary: "My account", Description: "The caller's account and factor summary. " + enroll,
			Tags: []string{tagMe},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*accountOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		acct, err := svc.Me(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		return &accountOutput{ETagHeader: ETagHeader{ETag: RevisionETag(acct.Revision)}, Body: newAccount(acct)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-my-password", Method: http.MethodPatch, Path: BasePath + "/me/password",
			Summary: "Change my password", DefaultStatus: http.StatusNoContent,
			Description: "Requires a recent step-up and the current password (when one is set). The new password must meet the policy. " +
				"The caller's other sessions end; this session continues with a new token. " + enroll,
			Tags: []string{tagMe}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusTooManyRequests},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, in *changePasswordInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.ChangePassword(ctx, in.Body.CurrentPassword, in.Body.NewPassword, in.Body.RevokeAPITokens); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-my-recovery-codes", Method: http.MethodGet, Path: BasePath + "/me/recovery-codes",
			Summary: "My recovery code status", Description: "How many one-time recovery codes remain. Codes are only shown when generated. " + enroll,
			Tags: []string{tagMe}, Security: cookieOnly,
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone,
	}, func(ctx context.Context, _ *struct{}) (*recoveryStatusOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		st, err := svc.RecoveryCodeStatus(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		out := &recoveryStatusOutput{}
		out.Body.Remaining, out.Body.GeneratedAt = st.Remaining, st.GeneratedAt
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-my-recovery-codes", Method: http.MethodPost, Path: BasePath + "/me/recovery-codes",
			Summary: "Generate new recovery codes", DefaultStatus: http.StatusCreated,
			Description: "Replaces the caller's recovery codes with ten new one-time codes, returned only in this response (stored as verifiers). " +
				"Requires a recent step-up (a fresh sign-in counts). Recovery codes complete a password sign-in when TOTP or the passkey is lost. " + enroll,
			Tags: []string{tagMe}, Security: cookieOnly, Errors: []int{http.StatusForbidden},
		},
		Capability: CapabilityAuthenticated, Scope: ScopeNone, Idempotency: IdempotencyStored,
	}, func(ctx context.Context, _ *recoveryCodesInput) (*recoveryCodesOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		codes, err := svc.RotateRecoveryCodes(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		out := &recoveryCodesOutput{}
		out.Body.Codes = codes
		return out, nil
	})
}
