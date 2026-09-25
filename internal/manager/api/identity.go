package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/neurekadev/dockyard/internal/domain"
)

// Identity routes (#16): first-run setup, sign-in, factors, invitations,
// users and the instance sign-in policy. The flows live in
// internal/manager/auth (built on the #18 libraries, ADR 0003); this file
// is the transport contract.

const (
	tagSetup    = "Setup"
	tagAuth     = "Authentication"
	tagMe       = "Account"
	tagInvites  = "Invitations"
	tagUsers    = "Users"
	tagSettings = "Settings"
)

// IdentityService is the identity layer as seen by the API
// (implemented by internal/manager/auth.Service). Every method reads the
// caller's session from ctx and enforces its own authorization (owner,
// recent authentication, enrollment stage).
type IdentityService interface {
	SetupStatus(ctx context.Context) (domain.SetupStatus, error)
	SetupOwner(ctx context.Context, in domain.OwnerSetup) (domain.SessionState, error)
	// SetupOpen admits the other first-run setup routes (backup import).
	SetupOpen(ctx context.Context) error

	SignIn(ctx context.Context, username, password, totpCode string) (domain.SessionState, error)
	CurrentSession(ctx context.Context) (domain.SessionState, error)
	SignOut(ctx context.Context) error
	StepUp(ctx context.Context, req domain.StepUp) (domain.SessionState, error)
	RedeemRecoveryCode(ctx context.Context, code string) (domain.SessionState, error)
	RedeemPasswordReset(ctx context.Context, code, newPassword string, revokeAPITokens bool) error

	BeginTOTPEnrollment(ctx context.Context) (domain.TOTPEnrollment, error)
	VerifyTOTPEnrollment(ctx context.Context, code string) (domain.SessionState, error)
	DeleteTOTP(ctx context.Context) error

	PasskeyRegistrationOptions(ctx context.Context) (json.RawMessage, error)
	PasskeyRegistrationVerification(ctx context.Context, name string, response []byte) (domain.Passkey, domain.SessionState, error)
	PasskeyAuthenticationOptions(ctx context.Context, purpose domain.PasskeyPurpose) (json.RawMessage, error)
	PasskeyAuthenticationVerification(ctx context.Context, response []byte) (domain.SessionState, error)
	ListMyPasskeys(ctx context.Context) ([]domain.Passkey, error)
	RenameMyPasskey(ctx context.Context, id, name string) (domain.Passkey, error)
	DeleteMyPasskey(ctx context.Context, id string) error

	Me(ctx context.Context) (domain.Account, error)
	ChangePassword(ctx context.Context, currentPassword, newPassword string, revokeAPITokens bool) error
	RecoveryCodeStatus(ctx context.Context) (domain.RecoveryCodeStatus, error)
	RotateRecoveryCodes(ctx context.Context) ([]string, error)

	CreateInvitation(ctx context.Context, email string, ttlHours int) (domain.Invitation, domain.IssuedCode, error)
	ListInvitations(ctx context.Context, beforeID string, limit int) ([]domain.Invitation, error)
	RevokeInvitation(ctx context.Context, id string) error
	RedeemInvitation(ctx context.Context, in domain.InvitationRedemption) (domain.SessionState, error)

	ListUsers(ctx context.Context, afterID string, limit int) ([]domain.Account, error)
	GetUser(ctx context.Context, id string) (domain.Account, error)
	PatchUser(ctx context.Context, id string, revision int64, p domain.UserPatch) (domain.Account, error)
	DeleteUser(ctx context.Context, id string) error
	RevokeUserSessions(ctx context.Context, id string) error
	ResetUserFactors(ctx context.Context, id string, revokeAPITokens bool) (domain.Account, error)
	CreatePasswordReset(ctx context.Context, id string, revokeAPITokens bool) (domain.IssuedCode, error)

	SecuritySettings(ctx context.Context) (domain.SecuritySettings, error)
	UpdateSecuritySettings(ctx context.Context, revision int64, p domain.SecuritySettingsPatch) (domain.SecuritySettings, error)

	Now() time.Time
}

// Transport types.

// AccountFactors summarizes an account's sign-in factors (never secrets).
type AccountFactors struct {
	Password               bool `json:"password" doc:"A password is set."`
	TOTP                   bool `json:"totp" doc:"TOTP (authenticator app) is enabled."`
	Passkeys               int  `json:"passkeys" doc:"Number of registered passkeys."`
	RecoveryCodesRemaining int  `json:"recoveryCodesRemaining" doc:"Unused one-time recovery codes."`
}

// Account is a DockYard account.
type Account struct {
	ID                 string         `json:"id" example:"0190a6e0-0000-7000-8000-000000000001"`
	Username           string         `json:"username" example:"alice"`
	DisplayName        string         `json:"displayName"`
	Email              string         `json:"email,omitempty"`
	Owner              bool           `json:"owner" doc:"The instance owner: a protected principal that cannot be disabled, deleted or have its grants removed."`
	GroupID            string         `json:"groupId" doc:"The account's permission group (#17). New accounts join the default group (initially Restricted, no grants)."`
	Status             string         `json:"status" enum:"active,disabled"`
	Factors            AccountFactors `json:"factors"`
	EnrollmentDeadline *time.Time     `json:"enrollmentDeadline,omitempty" doc:"Until when the account may still sign in to enroll factors the policy requires."`
	Revision           int64          `json:"revision"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
	DisabledAt         *time.Time     `json:"disabledAt,omitempty"`
	LastSignInAt       *time.Time     `json:"lastSignInAt,omitempty"`
}

func newAccount(a domain.Account) Account {
	return Account{
		ID: a.ID, Username: a.Username, DisplayName: a.DisplayName, Email: a.Email, Owner: a.Owner, GroupID: a.GroupID,
		Status: string(a.Status), Revision: a.Revision, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		DisabledAt: a.DisabledAt, LastSignInAt: a.LastSignInAt, EnrollmentDeadline: a.EnrollmentDeadline,
		Factors: AccountFactors{Password: a.HasPassword, TOTP: a.TOTPEnabled, Passkeys: a.PasskeyCount, RecoveryCodesRemaining: a.RecoveryCodesRemaining},
	}
}

// Session describes the caller's browser session.
type Session struct {
	State string   `json:"state" enum:"second_factor_required,enrollment_required,authenticated" doc:"authenticated: full access (subject to permissions). second_factor_required: send one of factors. enrollment_required: a limited session that may only enroll missingFactors."`
	User  *Account `json:"user,omitempty" doc:"The signed-in account (enrollment_required and authenticated)."`
	// Factors are the ways to complete a pending sign-in.
	Factors            []string   `json:"factors" doc:"Factors that complete a pending sign-in: totp (POST /auth/session with totpCode), passkey (authentication options + verification), recovery_code (POST /auth/recovery-codes/redemptions)." enum:"password,totp,passkey,recovery_code"`
	MissingFactors     []string   `json:"missingFactors" doc:"Factors to enroll before full access. With requiredFactors=either, one of them is enough." enum:"password,totp,passkey,recovery_code"`
	RequiredFactors    string     `json:"requiredFactors" enum:"none,totp,passkey,either,both" doc:"The instance sign-in policy."`
	EnrollmentDeadline *time.Time `json:"enrollmentDeadline,omitempty"`
	AuthenticatedAt    *time.Time `json:"authenticatedAt,omitempty"`
	ExpiresAt          *time.Time `json:"expiresAt,omitempty" doc:"Absolute end of the session."`
	IdleExpiresAt      *time.Time `json:"idleExpiresAt,omitempty" doc:"End of the session without further activity."`
	RecentAuthUntil    *time.Time `json:"recentAuthUntil,omitempty" doc:"Until when sensitive changes are allowed without a new step-up."`
}

func factorStrings(fs []domain.Factor) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, string(f))
	}
	return out
}

func newSession(st domain.SessionState) Session {
	out := Session{
		State: string(st.Stage), Factors: factorStrings(st.Factors), MissingFactors: factorStrings(st.MissingFactors),
		RequiredFactors: string(st.RequiredFactors), EnrollmentDeadline: st.EnrollmentDeadline, AuthenticatedAt: st.AuthenticatedAt,
		ExpiresAt: st.ExpiresAt, IdleExpiresAt: st.IdleExpiresAt, RecentAuthUntil: st.RecentAuthUntil,
	}
	if st.Account != nil {
		a := newAccount(*st.Account)
		out.User = &a
	}
	return out
}

// Passkey is a registered passkey (no key material).
type Passkey struct {
	ID             string     `json:"id"`
	Name           string     `json:"name" example:"YubiKey 5C"`
	BackupEligible bool       `json:"backupEligible" doc:"The passkey can be synced/backed up by its provider."`
	BackedUp       bool       `json:"backedUp" doc:"The provider reported the passkey as backed up."`
	AAGUID         string     `json:"aaguid,omitempty" doc:"Authenticator model identifier, when the provider reports one."`
	CreatedAt      time.Time  `json:"createdAt"`
	LastUsedAt     *time.Time `json:"lastUsedAt,omitempty"`
}

func newPasskey(p domain.Passkey) Passkey {
	aaguid := p.AAGUID
	if aaguid == "00000000-0000-0000-0000-000000000000" {
		aaguid = ""
	}
	return Passkey{ID: p.ID, Name: p.Name, BackupEligible: p.BackupEligible, BackedUp: p.BackupState, AAGUID: aaguid,
		CreatedAt: p.CreatedAt, LastUsedAt: p.LastUsedAt}
}

// Invitation is an issued invitation (never its code).
type Invitation struct {
	ID             string     `json:"id"`
	Email          string     `json:"email,omitempty" example:"ada@example.com" doc:"Only this email address may redeem the invitation."`
	Status         string     `json:"status" enum:"pending,redeemed,expired,revoked"`
	CreatedBy      string     `json:"createdBy,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	RedeemedAt     *time.Time `json:"redeemedAt,omitempty"`
	RedeemedUserID string     `json:"redeemedUserId,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
}

func newInvitation(i domain.Invitation, now time.Time) Invitation {
	return Invitation{ID: i.ID, Email: i.Email, Status: string(i.Status(now)), CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt,
		ExpiresAt: i.ExpiresAt, RedeemedAt: i.RedeemedAt, RedeemedUserID: i.RedeemedUserID, RevokedAt: i.RevokedAt}
}

// IssuedCode is a one-time code, shown only in this response.
type IssuedCode struct {
	Code      string    `json:"code" doc:"The one-time code. Shown only here; DockYard stores a verifier, not the code."`
	URL       string    `json:"url" example:"https://docker.example.com/reset#code=R7QK-2M4P-X9WT" doc:"Link on the public origin that redeems the code (the code is in the URL fragment)."`
	ExpiresAt time.Time `json:"expiresAt"`
}

// SecuritySettings is the instance sign-in policy.
type SecuritySettings struct {
	StrictPasswords       bool      `json:"strictPasswords" doc:"Enforce minPasswordLength (otherwise at least 8 characters). Common and breached passwords are always refused; there are no composition rules or forced rotation."`
	MinPasswordLength     int       `json:"minPasswordLength" example:"12" minimum:"8" maximum:"64"`
	RequiredFactors       string    `json:"requiredFactors" example:"either" enum:"none,totp,passkey,either,both" doc:"none: a password (plus TOTP if the user enabled it) or a passkey. totp: password + TOTP. passkey: a passkey with user verification. either: password + TOTP, or a passkey. both: password + TOTP + passkey."`
	EnrollmentGraceHours  int       `json:"enrollmentGraceHours" minimum:"1" maximum:"720" doc:"How long accounts may sign in to a limited enrollment session to add required factors."`
	InvitationTTLHours    int       `json:"invitationTtlHours" minimum:"1" maximum:"720"`
	PasswordResetTTLHours int       `json:"passwordResetTtlHours" minimum:"1" maximum:"168"`
	APITokensEnabled      bool      `json:"apiTokensEnabled" doc:"API tokens (#31) may be created and used. false: every token stops working at once (they work again when re-enabled; revoke them to end them for good)."`
	APITokenMaxDays       int       `json:"apiTokenMaxLifetimeDays" minimum:"1" maximum:"3650" doc:"Longest lifetime of a new API token (default 90 days)."`
	APITokensNonExpiring  bool      `json:"apiTokensNonExpiring" doc:"API tokens without an expiry may be created (off by default)."`
	Revision              int64     `json:"revision"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

func newSecuritySettings(s domain.SecuritySettings) SecuritySettings {
	return SecuritySettings{StrictPasswords: s.StrictPasswords, MinPasswordLength: s.MinPasswordLength,
		RequiredFactors: string(s.RequiredFactors), EnrollmentGraceHours: s.EnrollmentGraceHours,
		InvitationTTLHours: s.InvitationTTLHours, PasswordResetTTLHours: s.PasswordResetTTLHours,
		APITokensEnabled: s.APITokensEnabled, APITokenMaxDays: s.APITokenMaxDays, APITokensNonExpiring: s.APITokensNonExpiring,
		Revision: s.Revision, UpdatedAt: s.UpdatedAt}
}

// identityError maps identity errors to API errors with stable codes.
func identityError(err error) error {
	var (
		fe *domain.FieldError
		pe *domain.PasswordPolicyError
		re *domain.RateLimitedError
		ie *domain.InsecureOriginError
	)
	switch {
	case err == nil:
		return nil
	case errors.As(err, &fe):
		return Invalid("invalid input", Field("body."+fe.Field, fe.Message))
	case errors.As(err, &pe):
		details := make([]ErrorDetail, 0, len(pe.Violations))
		for _, v := range pe.Violations {
			details = append(details, Field("body."+pe.Field, v.Message))
		}
		return Invalid("the password does not meet the password policy", details...)
	case errors.As(err, &re):
		secs := int(re.RetryAfter / time.Second)
		return RateLimited("too many attempts; try again later").WithHeader("Retry-After", strconv.Itoa(max(secs, 1)))
	case errors.As(err, &ie):
		return NewError(http.StatusForbidden, CodeInsecureOrigin, ie.Explanation)
	case errors.Is(err, domain.ErrNotAuthenticated):
		return Unauthenticated("sign in first")
	case errors.Is(err, domain.ErrInvalidCredentials):
		return NewError(http.StatusUnauthorized, CodeInvalidCredentials, "the sign-in details are not valid")
	case errors.Is(err, domain.ErrForbidden):
		return Forbidden("only the instance owner may do this")
	case errors.Is(err, domain.ErrStepUpRequired):
		return NewError(http.StatusForbidden, CodeStepUpRequired, "confirm your identity again (POST /api/v1/auth/step-ups) and retry")
	case errors.Is(err, domain.ErrEnrollmentRequired):
		return NewError(http.StatusForbidden, CodeEnrollmentRequired, "finish enrolling the sign-in factors the instance policy requires first")
	case errors.Is(err, domain.ErrEnrollmentExpired):
		return NewError(http.StatusForbidden, CodeEnrollmentExpired, "the time to set up the required sign-in factors has passed; ask the instance owner to reset your sign-in factors or password")
	case errors.Is(err, domain.ErrMethodNotAllowed):
		return NewError(http.StatusForbidden, CodeSignInMethodNotAllowed, "the instance sign-in policy does not accept this sign-in method; sign in with your password")
	case errors.Is(err, domain.ErrSetupComplete):
		return Conflict(CodeSetupComplete, "DockYard is already set up; sign in instead")
	case errors.Is(err, domain.ErrUsernameTaken):
		return Conflict(CodeUsernameTaken, "this username is taken")
	case errors.Is(err, domain.ErrOwnerProtected):
		return Conflict(CodeOwnerProtected, "the instance owner cannot be changed this way; the owner uses owner recovery (dockyard-manager owner-recovery)")
	case errors.Is(err, domain.ErrFactorRequired):
		return Conflict(CodeFactorRequired, "the sign-in policy needs this factor; add another one first")
	case errors.Is(err, domain.ErrTOTPAlreadyEnabled):
		return Conflict(CodeTOTPAlreadyEnabled, "TOTP is already enabled")
	case errors.Is(err, domain.ErrInvitationRedeemed):
		return Conflict(CodeInvitationRedeemed, "the invitation was already redeemed")
	case errors.Is(err, domain.ErrNoPendingFlow):
		return Conflict(CodeNoPendingFlow, "nothing is in progress in this session, or it expired; start again")
	case errors.Is(err, domain.ErrCodeInvalid):
		return NewError(http.StatusBadRequest, CodeInvalidCode, "the code is not valid")
	case errors.Is(err, domain.ErrPasskeyVerification):
		return Invalid("the passkey could not be verified", Field("body.credential", "verification failed"))
	case errors.Is(err, domain.ErrGroupNotFound):
		return Invalid("unknown group", Field("body.groupId", "no such group"))
	case errors.Is(err, domain.ErrUserNotFound):
		return NotFound("user not found")
	case errors.Is(err, domain.ErrInvitationNotFound):
		return NotFound("invitation not found")
	case errors.Is(err, domain.ErrPasskeyNotFound):
		return NotFound("passkey not found")
	case errors.Is(err, domain.ErrIdentityUnavailable):
		return Unavailable(CodeUnavailable, "the identity service is not available")
	}
	var busy interface{ Busy() bool }
	if errors.As(err, &busy) && busy.Busy() {
		return Unavailable(CodeUnavailable, "the manager is busy; retry shortly").WithHeader("Retry-After", "1")
	}
	return Internal(err)
}

// Inputs and outputs.

type sessionOutput struct{ Body Session }

type setupStatusOutput struct {
	Body struct {
		SetupComplete bool               `json:"setupComplete" example:"false" doc:"The instance owner exists; setup routes are closed."`
		SecureOrigin  bool               `json:"secureOrigin" doc:"This request reached DockYard over HTTPS on its public URL, so setup can complete."`
		Explanation   string             `json:"explanation,omitempty" doc:"Why setup cannot complete over this request, and how to fix it."`
		BackupImport  *SetupBackupImport `json:"backupImport,omitempty" doc:"The newest backup import (#24) while setup is open."`
	}
}

type setupOwnerInput struct {
	Body struct {
		Username    string `json:"username" minLength:"1" maxLength:"64" example:"admin"`
		DisplayName string `json:"displayName,omitempty" maxLength:"128"`
		Email       string `json:"email,omitempty" maxLength:"254"`
		Password    string `json:"password" minLength:"1" maxLength:"1024" doc:"Checked against the password policy (strict by default: at least 15 characters, no common or breached passwords). Long passphrases are welcome."`
	}
}

type createSessionInput struct {
	Body struct {
		Username string `json:"username,omitempty" example:"olga" maxLength:"64" doc:"Start a sign-in with username and password."`
		Password string `json:"password,omitempty" maxLength:"1024"`
		TOTPCode string `json:"totpCode,omitempty" maxLength:"16" doc:"TOTP code: continues a pending sign-in, or completes one started in the same request."`
	}
}

type stepUpInput struct {
	Body struct {
		Password   string          `json:"password,omitempty" example:"correct-horse-battery-staple" maxLength:"1024"`
		TOTPCode   string          `json:"totpCode,omitempty" maxLength:"16" doc:"Required with password when TOTP is enabled."`
		Credential json.RawMessage `json:"credential,omitempty" doc:"A passkey assertion (PublicKeyCredential JSON) for options requested with purpose step_up."`
	}
}

type codeInput struct {
	Body struct {
		Code string `json:"code" example:"123456" minLength:"1" maxLength:"128"`
	}
}

type passwordResetRedemptionInput struct {
	Body struct {
		Code            string `json:"code" example:"R7QK-2M4P-X9WT" minLength:"1" maxLength:"128" doc:"Password-reset or owner-recovery code."`
		NewPassword     string `json:"newPassword" example:"correct-horse-battery-staple" minLength:"1" maxLength:"1024"`
		RevokeAPITokens bool   `json:"revokeApiTokens,omitempty" doc:"Also revoke every API token of the account (#31)."`
	}
}

type totpEnrollmentOutput struct {
	Body struct {
		Secret    string    `json:"secret" example:"JBSWY3DPEHPK3PXP" doc:"Base32 secret for manual entry. Shown only in this response."`
		URI       string    `json:"uri" example:"otpauth://totp/DockYard:olga?secret=JBSWY3DPEHPK3PXP&issuer=DockYard" doc:"otpauth:// URI for a QR code. Shown only in this response."`
		ExpiresAt time.Time `json:"expiresAt" doc:"Confirm a code before this time."`
	}
}

type passkeyOptionsOutput struct {
	Body json.RawMessage `doc:"PublicKeyCredentialCreationOptions / RequestOptions JSON ({\"publicKey\": {...}}) for navigator.credentials.create()/get()." example:"{\"publicKey\":{\"challenge\":\"dGVzdC1jaGFsbGVuZ2U\",\"rpId\":\"docker.example.com\",\"timeout\":60000,\"userVerification\":\"required\"}}"`
}

type passkeyRegistrationInput struct {
	Body struct {
		Name       string          `json:"name,omitempty" example:"YubiKey 5C" maxLength:"64" doc:"Label shown in the passkey list."`
		Credential json.RawMessage `json:"credential" doc:"The PublicKeyCredential JSON returned by navigator.credentials.create()."`
	}
}

type passkeyRegistrationOutput struct {
	Body struct {
		Passkey Passkey `json:"passkey"`
		Session Session `json:"session"`
	}
}

type passkeyAuthOptionsInput struct {
	Body *struct {
		Purpose string `json:"purpose,omitempty" example:"sign_in" enum:"sign_in,step_up" doc:"sign_in (default): a username-less sign-in, or the second factor of a pending password sign-in. step_up: re-authentication of the signed-in user."`
	}
}

type passkeyAssertionInput struct {
	Body struct {
		Credential json.RawMessage `json:"credential" example:"{\"id\":\"q2Xs1Q\",\"rawId\":\"q2Xs1Q\",\"type\":\"public-key\",\"response\":{\"clientDataJSON\":\"eyJ0eXBlIjoid2ViYXV0aG4uZ2V0In0\",\"authenticatorData\":\"SZYN5YgO\",\"signature\":\"MEUCIQ\",\"userHandle\":\"AAAB\"}}" doc:"The PublicKeyCredential JSON returned by navigator.credentials.get()."`
	}
}

type passkeyListOutput struct{ Body Page[Passkey] }

type passkeyIDInput struct {
	CredentialID string `path:"credentialId" maxLength:"64" doc:"Passkey ID (from GET /api/v1/me/passkeys)."`
}

type passkeyRenameInput struct {
	CredentialID string `path:"credentialId" maxLength:"64" doc:"Passkey ID (from GET /api/v1/me/passkeys)."`
	Body         struct {
		Name string `json:"name" example:"Laptop passkey" minLength:"1" maxLength:"64" doc:"Label shown in the passkey list."`
	}
}

type passkeyOutput struct {
	Body Passkey
}

type accountOutput struct {
	ETagHeader
	Body Account
}

type changePasswordInput struct {
	Body struct {
		CurrentPassword string `json:"currentPassword,omitempty" maxLength:"1024" doc:"Required when the account has a password."`
		NewPassword     string `json:"newPassword" example:"correct-horse-battery-staple" minLength:"1" maxLength:"1024"`
		RevokeAPITokens bool   `json:"revokeApiTokens,omitempty" doc:"Also revoke every API token of the account (#31)."`
	}
}

type recoveryStatusOutput struct {
	Body struct {
		Remaining   int        `json:"remaining" example:"10"`
		GeneratedAt *time.Time `json:"generatedAt,omitempty"`
	}
}

type recoveryCodesInput struct{ IdempotencyKeyParam }

type recoveryCodesOutput struct {
	Body struct {
		Codes []string `json:"codes" example:"7Q2M-KX4P,9WRT-3HJD" doc:"Ten one-time recovery codes. Shown only in this response; earlier codes stop working."`
	}
}

type createInvitationInput struct {
	IdempotencyKeyParam
	Body *struct {
		Email          string `json:"email,omitempty" example:"ada@example.com" maxLength:"254" doc:"Bind the invitation to this address (it must be entered when redeeming). No email is sent."`
		ExpiresInHours int    `json:"expiresInHours,omitempty" example:"72" minimum:"1" maximum:"720" doc:"Defaults to the security settings' invitationTtlHours."`
	}
}

type createInvitationOutput struct {
	Body struct {
		Invitation Invitation `json:"invitation"`
		IssuedCode
	}
}

type invitationListOutput struct{ Body Page[Invitation] }

type invitationIDInput struct {
	InvitationID string `path:"invitationId" maxLength:"64"`
}

type redeemInvitationInput struct {
	Body struct {
		Code        string `json:"code" minLength:"1" maxLength:"128"`
		Username    string `json:"username" example:"ada" minLength:"1" maxLength:"64"`
		DisplayName string `json:"displayName,omitempty" example:"Ada Lovelace" maxLength:"128"`
		Email       string `json:"email,omitempty" maxLength:"254" doc:"Required when the invitation is bound to an address."`
		Password    string `json:"password,omitempty" maxLength:"1024" doc:"Required unless the policy is passkey-only."`
	}
}

type userListOutput struct{ Body Page[Account] }

type userIDInput struct {
	UserID string `path:"userId" maxLength:"64"`
}

type patchUserInput struct {
	UserID string `path:"userId" maxLength:"64"`
	IfMatchParam
	Body struct {
		DisplayName *string `json:"displayName,omitempty" example:"Ada Lovelace" maxLength:"128"`
		Email       *string `json:"email,omitempty" maxLength:"254"`
		GroupID     *string `json:"groupId,omitempty" maxLength:"64" doc:"Move the account to exactly one group (#17)."`
		Status      *string `json:"status,omitempty" enum:"active,disabled" doc:"disabled ends every session and stream of the account at once."`
	}
}

type deleteUserInput struct {
	UserID string `path:"userId" maxLength:"64"`
	IfMatchParam
}

type userPasswordResetInput struct {
	UserID string `path:"userId" maxLength:"64"`
	IdempotencyKeyParam
	Body *revokeTokensBody
}

// revokeTokensBody offers revoking the account's API tokens (#31) with a
// credential reset.
type revokeTokensBody struct {
	RevokeAPITokens bool `json:"revokeApiTokens,omitempty" example:"true" doc:"Also revoke every API token of the account (#31), for example when the account may be compromised."`
}

type userFactorResetInput struct {
	UserID string `path:"userId" maxLength:"64"`
	Body   *revokeTokensBody
}

func (b *revokeTokensBody) revoke() bool { return b != nil && b.RevokeAPITokens }

type issuedCodeOutput struct{ Body IssuedCode }

type securitySettingsOutput struct {
	ETagHeader
	Body SecuritySettings
}

type patchSecuritySettingsInput struct {
	IfMatchParam
	Body struct {
		StrictPasswords       *bool   `json:"strictPasswords,omitempty"`
		MinPasswordLength     *int    `json:"minPasswordLength,omitempty" example:"12" minimum:"8" maximum:"64"`
		RequiredFactors       *string `json:"requiredFactors,omitempty" example:"either" enum:"none,totp,passkey,either,both"`
		EnrollmentGraceHours  *int    `json:"enrollmentGraceHours,omitempty" minimum:"1" maximum:"720"`
		InvitationTTLHours    *int    `json:"invitationTtlHours,omitempty" minimum:"1" maximum:"720"`
		PasswordResetTTLHours *int    `json:"passwordResetTtlHours,omitempty" minimum:"1" maximum:"168"`
		APITokensEnabled      *bool   `json:"apiTokensEnabled,omitempty" doc:"false stops every API token at once (and closes their streams)."`
		APITokenMaxDays       *int    `json:"apiTokenMaxLifetimeDays,omitempty" minimum:"1" maximum:"3650" doc:"Applies to tokens created afterwards."`
		APITokensNonExpiring  *bool   `json:"apiTokensNonExpiring,omitempty" doc:"Allow new API tokens without expiry."`
	}
}

type idCursor struct {
	After string `json:"a"`
}
