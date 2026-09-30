package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// ownerOnly documents owner routes; they are cookie-only (API tokens never
// reach owner administration, #31).
const ownerOnly = "Instance owner only (never delegable, never with an API token)."

func registerAccountAdmin(a huma.API, h *identityAPI) {
	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-invitations", Method: http.MethodGet, Path: BasePath + "/invitations",
			Summary: "List invitations", Description: "Newest first, with their status. Codes are never listed. " + ownerOnly,
			Tags: []string{tagInvites}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *struct{ PageParams }) (*invitationListOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		fp := QueryFingerprint("invitations")
		var pos idCursor
		if in.Cursor != "" {
			if err := DecodeCursorFor(in.Cursor, fp, &pos); err != nil {
				return nil, err
			}
		}
		limit := in.PageLimit()
		invs, err := svc.ListInvitations(ctx, pos.After, limit+1)
		if err != nil {
			return nil, identityError(err)
		}
		next := ""
		if len(invs) > limit {
			invs = invs[:limit]
			if next, err = CursorFor(fp, idCursor{After: invs[limit-1].ID}); err != nil {
				return nil, Internal(err)
			}
		}
		now := svc.Now()
		items := make([]Invitation, 0, len(invs))
		for _, i := range invs {
			items = append(items, newInvitation(i, now))
		}
		return &invitationListOutput{Body: NewPage(items, next, nil)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-invitation", Method: http.MethodPost, Path: BasePath + "/invitations",
			Summary: "Invite a user", DefaultStatus: http.StatusCreated,
			Description: "Issues a single-use invitation with a 256-bit code, returned only in this response (Docker Manager stores a verifier). " +
				"Expires after expiresInHours (default from the security settings); optionally bound to an email address. No email is sent: " +
				"hand the link over yourself. Requires a recent step-up. " + ownerOnly,
			Tags: []string{tagInvites}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, Idempotency: IdempotencyStored,
	}, func(ctx context.Context, in *createInvitationInput) (*createInvitationOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		email, ttl := "", 0
		if in.Body != nil {
			email, ttl = in.Body.Email, in.Body.ExpiresInHours
		}
		inv, code, err := svc.CreateInvitation(ctx, email, ttl)
		if err != nil {
			return nil, identityError(err)
		}
		out := &createInvitationOutput{}
		out.Body.Invitation = newInvitation(inv, svc.Now())
		out.Body.IssuedCode = IssuedCode{Code: code.Code, URL: code.URL, ExpiresAt: code.ExpiresAt}
		return out, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-invitation", Method: http.MethodDelete, Path: BasePath + "/invitations/{invitationId}",
			Summary: "Revoke an invitation", DefaultStatus: http.StatusNoContent,
			Description: "The code stops working at once. Revoking twice is harmless; 409 invitation_redeemed after redemption. " + ownerOnly,
			Tags:        []string{tagInvites}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *invitationIDInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.RevokeInvitation(ctx, in.InvitationID); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "list-users", Method: http.MethodGet, Path: BasePath + "/users",
			Summary: "List users", Description: "All accounts ordered by ID, with status and factor summary. " + ownerOnly,
			Tags: []string{tagUsers}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *struct{ PageParams }) (*userListOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		fp := QueryFingerprint("users")
		var pos idCursor
		if in.Cursor != "" {
			if err := DecodeCursorFor(in.Cursor, fp, &pos); err != nil {
				return nil, err
			}
		}
		limit := in.PageLimit()
		accts, err := svc.ListUsers(ctx, pos.After, limit+1)
		if err != nil {
			return nil, identityError(err)
		}
		next := ""
		if len(accts) > limit {
			accts = accts[:limit]
			if next, err = CursorFor(fp, idCursor{After: accts[limit-1].ID}); err != nil {
				return nil, Internal(err)
			}
		}
		items := make([]Account, 0, len(accts))
		for _, a := range accts {
			items = append(items, newAccount(a))
		}
		return &userListOutput{Body: NewPage(items, next, nil)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-user", Method: http.MethodGet, Path: BasePath + "/users/{userId}",
			Summary: "Get a user", Description: ownerOnly,
			Tags: []string{tagUsers}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *userIDInput) (*accountOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		acct, err := svc.GetUser(ctx, in.UserID)
		if err != nil {
			return nil, identityError(err)
		}
		return &accountOutput{ETagHeader: ETagHeader{ETag: RevisionETag(acct.Revision)}, Body: newAccount(acct)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-user", Method: http.MethodPatch, Path: BasePath + "/users/{userId}",
			Summary: "Update a user",
			Description: "Edit profile fields, move the account to exactly one group (#17), or disable/reactivate it. A group move needs a recent " +
				"step-up (403 step_up_required), changes the account's access at once and ends its open requests and streams. Disabling ends " +
				"every session and open stream of the account immediately; the owner cannot be disabled (409 owner_protected). Requires If-Match. " + ownerOnly,
			Tags: []string{tagUsers}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *patchUserInput) (*accountOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		cur, err := svc.GetUser(ctx, in.UserID)
		if err != nil {
			return nil, identityError(err)
		}
		if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
			return nil, err
		}
		p := domain.UserPatch{DisplayName: in.Body.DisplayName, Email: in.Body.Email, GroupID: in.Body.GroupID}
		if in.Body.Status != nil {
			st := domain.UserStatus(*in.Body.Status)
			p.Status = &st
		}
		acct, err := svc.PatchUser(ctx, in.UserID, cur.Revision, p)
		if errors.Is(err, domain.ErrRevisionConflict) {
			latest, gerr := svc.GetUser(ctx, in.UserID)
			if gerr != nil {
				return nil, identityError(gerr)
			}
			return nil, CheckIfMatch(RevisionETag(cur.Revision), RevisionETag(latest.Revision), true)
		}
		if err != nil {
			return nil, identityError(err)
		}
		return &accountOutput{ETagHeader: ETagHeader{ETag: RevisionETag(acct.Revision)}, Body: newAccount(acct)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "delete-user", Method: http.MethodDelete, Path: BasePath + "/users/{userId}",
			Summary: "Delete a user", DefaultStatus: http.StatusNoContent,
			Description: "Deletes the account and ends its sessions and streams; its jobs and audit history stay. The owner cannot be deleted " +
				"(409 owner_protected). If-Match is optional. " + ownerOnly,
			Tags: []string{tagUsers}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *deleteUserInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if in.IfMatch != "" {
			cur, err := svc.GetUser(ctx, in.UserID)
			if err != nil {
				return nil, identityError(err)
			}
			if err := CheckIfMatch(in.IfMatch, RevisionETag(cur.Revision), false); err != nil {
				return nil, err
			}
		}
		if err := svc.DeleteUser(ctx, in.UserID); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-user-session-revocation", Method: http.MethodPost, Path: BasePath + "/users/{userId}/session-revocations",
			Summary: "Sign a user out everywhere", DefaultStatus: http.StatusNoContent,
			Description: "Ends every session and open stream of the account immediately. " + ownerOnly,
			Tags:        []string{tagUsers}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *userIDInput) (*emptyOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		if err := svc.RevokeUserSessions(ctx, in.UserID); err != nil {
			return nil, identityError(err)
		}
		return &emptyOutput{}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-user-factor-reset", Method: http.MethodPost, Path: BasePath + "/users/{userId}/factor-resets",
			Summary: "Reset a user's sign-in factors",
			Description: "Removes the account's TOTP, passkeys and recovery codes (for a lost device), ends its sessions and streams, and starts a " +
				"new enrollment grace period: the user signs in with the password and enrolls again. Passkey-only accounts also need a password " +
				"reset. Optionally revokes the account's API tokens (revokeApiTokens). Requires a recent step-up; not for the owner " +
				"(409 owner_protected; the owner uses owner recovery). " + ownerOnly,
			Tags: []string{tagUsers}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *userFactorResetInput) (*accountOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		acct, err := svc.ResetUserFactors(ctx, in.UserID, in.Body.revoke())
		if err != nil {
			return nil, identityError(err)
		}
		return &accountOutput{ETagHeader: ETagHeader{ETag: RevisionETag(acct.Revision)}, Body: newAccount(acct)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "create-user-password-reset", Method: http.MethodPost, Path: BasePath + "/users/{userId}/password-resets",
			Summary: "Issue a password reset", DefaultStatus: http.StatusCreated,
			Description: "Returns a one-time password-reset code and link, only in this response; earlier unused codes of the account stop working. " +
				"The user redeems it with POST /api/v1/auth/password-resets/redemptions, which ends all of their sessions. " +
				"revokeApiTokens also revokes the account's API tokens at once. " +
				"Requires a recent step-up; not for the owner. " + ownerOnly,
			Tags: []string{tagUsers}, Security: cookieOnly, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance, Idempotency: IdempotencyStored,
	}, func(ctx context.Context, in *userPasswordResetInput) (*issuedCodeOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		code, err := svc.CreatePasswordReset(ctx, in.UserID, in.Body.revoke())
		if err != nil {
			return nil, identityError(err)
		}
		return &issuedCodeOutput{Body: IssuedCode{Code: code.Code, URL: code.URL, ExpiresAt: code.ExpiresAt}}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "get-security-settings", Method: http.MethodGet, Path: BasePath + "/settings/security",
			Summary: "Get the sign-in policy", Description: ownerOnly,
			Tags: []string{tagSettings}, Security: cookieOnly, Errors: []int{http.StatusForbidden},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, _ *struct{}) (*securitySettingsOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		s, err := svc.SecuritySettings(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		return &securitySettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(s.Revision)}, Body: newSecuritySettings(s)}, nil
	})

	Register(a, Operation{
		Operation: huma.Operation{
			OperationID: "update-security-settings", Method: http.MethodPatch, Path: BasePath + "/settings/security",
			Summary: "Change the sign-in policy",
			Description: "Requires a recent step-up and If-Match. Changing requiredFactors ends every other session (none keeps access the new policy " +
				"would not grant): accounts sign in again and get a limited enrollment session, with enrollmentGraceHours to add the required " +
				"factors (the owner has no deadline and cannot be locked out). The caller's session continues, limited to enrollment when the " +
				"owner lacks the new factors. " + ownerOnly,
			Tags: []string{tagSettings}, Security: cookieOnly,
			Errors: []int{http.StatusForbidden, http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity},
		},
		Capability: CapabilityOwner, Scope: ScopeInstance,
	}, func(ctx context.Context, in *patchSecuritySettingsInput) (*securitySettingsOutput, error) {
		svc, err := h.service()
		if err != nil {
			return nil, err
		}
		cur, err := svc.SecuritySettings(ctx)
		if err != nil {
			return nil, identityError(err)
		}
		if err := in.CheckIfMatch(RevisionETag(cur.Revision)); err != nil {
			return nil, err
		}
		p := domain.SecuritySettingsPatch{
			StrictPasswords: in.Body.StrictPasswords, MinPasswordLength: in.Body.MinPasswordLength,
			EnrollmentGraceHours: in.Body.EnrollmentGraceHours, InvitationTTLHours: in.Body.InvitationTTLHours,
			PasswordResetTTLHours: in.Body.PasswordResetTTLHours, APITokensEnabled: in.Body.APITokensEnabled,
			APITokenMaxDays: in.Body.APITokenMaxDays, APITokensNonExpiring: in.Body.APITokensNonExpiring,
			AllowStaySignedIn: in.Body.AllowStaySignedIn,
		}
		if in.Body.RequiredFactors != nil {
			rf := domain.RequiredFactors(*in.Body.RequiredFactors)
			p.RequiredFactors = &rf
		}
		s, err := svc.UpdateSecuritySettings(ctx, cur.Revision, p)
		if errors.Is(err, domain.ErrRevisionConflict) {
			latest, gerr := svc.SecuritySettings(ctx)
			if gerr != nil {
				return nil, identityError(gerr)
			}
			return nil, CheckIfMatch(RevisionETag(cur.Revision), RevisionETag(latest.Revision), true)
		}
		if err != nil {
			return nil, identityError(err)
		}
		return &securitySettingsOutput{ETagHeader: ETagHeader{ETag: RevisionETag(s.Revision)}, Body: newSecuritySettings(s)}, nil
	})
}
