package domain

import (
	"errors"
	"fmt"
	"time"
)

// Identity (#16): the instance owner, invited users, groups (#17 extends
// them with rules), sign-in factors, invitations and the instance sign-in
// policy.

// UserStatus is an account's status.
type UserStatus string

// User statuses.
const (
	UserActive   UserStatus = "active"
	UserDisabled UserStatus = "disabled"
)

// User is an account. The owner is a distinct protected principal: never
// disabled or deleted (enforced by the database as well).
type User struct {
	ID          string
	Username    string
	DisplayName string
	Email       string
	Owner       bool
	GroupID     string
	Status      UserStatus
	// WebAuthnHandle is the random WebAuthn user handle.
	WebAuthnHandle []byte
	// SessionEpoch changes whenever the account's sessions must end
	// (disable, credential change, revocation); sessions record the epoch
	// they were created in.
	SessionEpoch       int64
	HasPassword        bool
	PasswordChangedAt  *time.Time
	TOTPEnabled        bool
	EnrollmentDeadline *time.Time
	InvitationID       string
	Revision           int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DisabledAt         *time.Time
	LastSignInAt       *time.Time
}

// Active reports whether the account may sign in.
func (u User) Active() bool { return u.Status == UserActive }

// UserCredentials are the stored secrets of an account: the Argon2id hash
// and the sealed TOTP seeds (secrets.Keyring). Never serialized.
type UserCredentials struct {
	PasswordHash      string
	TOTPSeedSealed    string
	TOTPLastStep      int64
	TOTPPendingSealed string
	TOTPPendingExpiry *time.Time
}

// NewUser is the input of user creation.
type NewUser struct {
	ID                 string
	Username           string
	DisplayName        string
	Email              string
	Owner              bool
	GroupID            string
	PasswordHash       string
	WebAuthnHandle     []byte
	EnrollmentDeadline *time.Time
	InvitationID       string
	CreatedAt          time.Time
}

// UserPatch is an owner edit of a user (nil fields stay unchanged).
type UserPatch struct {
	DisplayName *string
	Email       *string
	GroupID     *string
	Status      *UserStatus
}

// Group is a permission group (#17). Exactly one group is the default for
// new users; it cannot be deleted while it is the default.
type Group struct {
	ID        string
	Name      string
	Default   bool
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RestrictedGroupName is the initial default group: no grants.
const RestrictedGroupName = "Restricted"

// Passkey is a registered WebAuthn credential.
type Passkey struct {
	ID           string
	UserID       string
	CredentialID []byte
	Name         string
	// Credential is the go-webauthn credential record (JSON): public key,
	// flags, sign counter, transports.
	Credential     []byte
	SignCount      uint32
	BackupEligible bool
	BackupState    bool
	AAGUID         string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

// RecoveryCodeStatus summarizes a user's one-time recovery codes.
type RecoveryCodeStatus struct {
	Remaining   int
	GeneratedAt *time.Time
}

// InvitationStatus is derived from an invitation's timestamps.
type InvitationStatus string

// Invitation statuses.
const (
	InvitationPending  InvitationStatus = "pending"
	InvitationRedeemed InvitationStatus = "redeemed"
	InvitationExpired  InvitationStatus = "expired"
	InvitationRevoked  InvitationStatus = "revoked"
)

// Invitation is an owner-issued, single-use sign-up code. Only a verifier
// of the code is stored.
type Invitation struct {
	ID             string
	Email          string
	CreatedBy      string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RedeemedAt     *time.Time
	RedeemedUserID string
	RevokedAt      *time.Time
}

// Status at now.
func (i Invitation) Status(now time.Time) InvitationStatus {
	switch {
	case i.RedeemedAt != nil:
		return InvitationRedeemed
	case i.RevokedAt != nil:
		return InvitationRevoked
	case !now.Before(i.ExpiresAt):
		return InvitationExpired
	}
	return InvitationPending
}

// AccountResetKind distinguishes owner-issued password resets from the
// owner-recovery CLI's codes.
type AccountResetKind string

// Account reset kinds.
const (
	// ResetPassword lets a user set a new password (owner-issued).
	ResetPassword AccountResetKind = "password_reset"
	// ResetOwnerRecovery resets the owner's password and factors
	// (docker-manager owner-recovery).
	ResetOwnerRecovery AccountResetKind = "owner_recovery"
)

// AccountReset is a one-time reset code record (verifier only).
type AccountReset struct {
	ID        string
	UserID    string
	Kind      AccountResetKind
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// RequiredFactors is the instance sign-in policy.
type RequiredFactors string

// Required factor policies.
const (
	// FactorsNone: a password, or a passkey. A user who enrolled TOTP must
	// still enter it after the password.
	FactorsNone RequiredFactors = "none"
	// FactorsTOTP: password plus TOTP code.
	FactorsTOTP RequiredFactors = "totp"
	// FactorsPasskey: a passkey (user-verifying, so multi-factor itself).
	FactorsPasskey RequiredFactors = "passkey"
	// FactorsEither: password plus TOTP, or a passkey.
	FactorsEither RequiredFactors = "either"
	// FactorsBoth: password plus TOTP plus passkey.
	FactorsBoth RequiredFactors = "both"
)

// Valid reports whether f is a known policy.
func (f RequiredFactors) Valid() bool {
	switch f {
	case FactorsNone, FactorsTOTP, FactorsPasskey, FactorsEither, FactorsBoth:
		return true
	}
	return false
}

// SecuritySettings is the instance-wide sign-in policy (owner only).
type SecuritySettings struct {
	StrictPasswords       bool
	MinPasswordLength     int
	RequiredFactors       RequiredFactors
	EnrollmentGraceHours  int
	InvitationTTLHours    int
	PasswordResetTTLHours int
	// API tokens (#31): enabled instance-wide, the maximum lifetime of new
	// tokens in days, and whether tokens without expiry may be created.
	APITokensEnabled     bool
	APITokenMaxDays      int
	APITokensNonExpiring bool
	// AllowStaySignedIn offers "Stay signed in" at sign-in (#16).
	AllowStaySignedIn bool
	Revision          int64
	UpdatedAt         time.Time
}

// Factor is a sign-in factor kind.
type Factor string

// Factors.
const (
	FactorPassword     Factor = "password"
	FactorTOTP         Factor = "totp"
	FactorPasskey      Factor = "passkey"
	FactorRecoveryCode Factor = "recovery_code"
)

// Account is a user plus a summary of its sign-in factors.
type Account struct {
	User
	PasskeyCount           int
	RecoveryCodesRemaining int
}

// SessionStage is how far a browser session got through sign-in.
type SessionStage string

// Session stages.
const (
	// StageSecondFactor: the first credential was accepted; one of Factors
	// must follow.
	StageSecondFactor SessionStage = "second_factor_required"
	// StageEnrollment: signed in, but the policy requires factors the
	// account has not enrolled; only enrollment routes are available.
	StageEnrollment SessionStage = "enrollment_required"
	// StageAuthenticated: full access (subject to authorization).
	StageAuthenticated SessionStage = "authenticated"
)

// SessionState describes the caller's browser session.
type SessionState struct {
	Stage SessionStage
	// Account is set for enrollment and authenticated sessions.
	Account *Account
	// Factors are the factors that can complete a pending sign-in.
	Factors []Factor
	// MissingFactors are the factors to enroll (enrollment stage); with the
	// "either" policy enrolling any one of them is enough.
	MissingFactors     []Factor
	RequiredFactors    RequiredFactors
	EnrollmentDeadline *time.Time
	AuthenticatedAt    *time.Time
	// ExpiresAt is the absolute end of the session; IdleExpiresAt the end
	// if there is no further activity.
	ExpiresAt       *time.Time
	IdleExpiresAt   *time.Time
	RecentAuthUntil *time.Time
	// SessionID identifies the session among the user's devices;
	// StaySignedIn reports the longer "Stay signed in" limits.
	SessionID    string
	StaySignedIn bool
}

// SetupStatus is the first-run state.
type SetupStatus struct {
	Complete bool
	// SecureOrigin reports whether this request could complete setup
	// (HTTPS on the public origin); Explanation says why not.
	SecureOrigin bool
	Explanation  string
	// StaySignedInAllowed reports whether the sign-in page offers "Stay
	// signed in" (the instance policy).
	StaySignedInAllowed bool
}

// OwnerSetup is the first-run owner account.
type OwnerSetup struct {
	Username    string
	DisplayName string
	Email       string
	Password    string
}

// InvitationRedemption creates an account from an invitation.
type InvitationRedemption struct {
	Code        string
	Username    string
	DisplayName string
	Email       string
	Password    string
}

// StepUp re-proves the caller's identity with one factor: a passkey
// assertion prepared with the step_up purpose, else a TOTP code, else the
// password.
type StepUp struct {
	Password        string
	TOTPCode        string
	PasskeyResponse []byte
}

// TOTPEnrollment is a pending TOTP secret, shown once.
type TOTPEnrollment struct {
	Secret    string
	URI       string
	ExpiresAt time.Time
}

// IssuedCode is a one-time code shown exactly once (invitation, password
// reset, owner recovery) with the link that redeems it.
type IssuedCode struct {
	ID        string
	Code      string
	URL       string
	ExpiresAt time.Time
}

// SecuritySettingsPatch changes the policy (nil fields stay).
type SecuritySettingsPatch struct {
	StrictPasswords       *bool
	MinPasswordLength     *int
	RequiredFactors       *RequiredFactors
	EnrollmentGraceHours  *int
	InvitationTTLHours    *int
	PasswordResetTTLHours *int
	APITokensEnabled      *bool
	APITokenMaxDays       *int
	APITokensNonExpiring  *bool
	AllowStaySignedIn     *bool
}

// PasskeyPurpose selects what a passkey assertion is for.
type PasskeyPurpose string

// Passkey purposes.
const (
	PasskeySignIn PasskeyPurpose = "sign_in"
	PasskeyStepUp PasskeyPurpose = "step_up"
)

// Identity errors. The API maps them to stable error codes.
var (
	ErrUserNotFound         = errors.New("user not found")
	ErrUsernameTaken        = errors.New("username taken")
	ErrSetupComplete        = errors.New("setup already completed")
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrCodeInvalid          = errors.New("code invalid, expired or used")
	ErrOwnerProtected       = errors.New("the instance owner is protected")
	ErrStepUpRequired       = errors.New("recent authentication required")
	ErrEnrollmentExpired    = errors.New("factor enrollment period expired")
	ErrEnrollmentRequired   = errors.New("factor enrollment required")
	ErrTOTPAlreadyEnabled   = errors.New("totp already enabled")
	ErrFactorRequired       = errors.New("factor required by the sign-in policy")
	ErrPasskeyVerification  = errors.New("passkey verification failed")
	ErrInvitationRedeemed   = errors.New("invitation already redeemed")
	ErrGroupNotFound        = errors.New("group not found")
	ErrInvitationNotFound   = errors.New("invitation not found")
	ErrPasskeyNotFound      = errors.New("passkey not found")
	ErrRevisionConflict     = errors.New("revision conflict")
	ErrSecondFactorRequired = errors.New("second factor required")
	ErrNotAuthenticated     = errors.New("not signed in")
	ErrForbidden            = errors.New("not permitted")
	ErrNoPendingFlow        = errors.New("no sign-in, enrollment or ceremony in progress")
	ErrMethodNotAllowed     = errors.New("sign-in method not allowed by the policy")
	ErrIdentityUnavailable  = errors.New("identity service unavailable")
)

// RateLimitedError is returned when throttling refuses an attempt.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("too many attempts; retry after %s", e.RetryAfter.Round(time.Second))
}

// PasswordPolicyError lists why a new password was refused.
type PasswordPolicyError struct {
	// Field is the request field ("newPassword", "password").
	Field      string
	Violations []PasswordViolation
}

// PasswordViolation is one policy violation.
type PasswordViolation struct {
	Code    string
	Message string
}

func (e *PasswordPolicyError) Error() string { return "password does not meet the policy" }

// FieldError is an invalid input field.
type FieldError struct {
	// Field is the JSON member name (e.g. "username").
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// InsecureOriginError wraps requestinfo.CheckSecureOrigin failures.
type InsecureOriginError struct {
	Reason      string
	Explanation string
}

func (e *InsecureOriginError) Error() string { return "insecure origin: " + e.Reason }
