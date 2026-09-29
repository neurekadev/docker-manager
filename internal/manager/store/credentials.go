package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

// Passkeys, recovery codes, invitations, account resets, groups and the
// security settings (#16).

type passkeyRow struct {
	bun.BaseModel `bun:"table:passkeys"`

	ID             string     `bun:"id,pk"`
	UserID         string     `bun:"user_id,notnull"`
	CredentialID   []byte     `bun:"credential_id,notnull"`
	Name           string     `bun:"name,notnull"`
	Credential     string     `bun:"credential,notnull"`
	SignCount      int64      `bun:"sign_count,notnull"`
	BackupEligible int        `bun:"backup_eligible,notnull"`
	BackupState    int        `bun:"backup_state,notnull"`
	AAGUID         string     `bun:"aaguid,notnull"`
	CreatedAt      time.Time  `bun:"created_at,notnull"`
	LastUsedAt     *time.Time `bun:"last_used_at"`
}

func (r passkeyRow) toDomain() domain.Passkey {
	return domain.Passkey{
		ID: r.ID, UserID: r.UserID, CredentialID: r.CredentialID, Name: r.Name, Credential: []byte(r.Credential),
		SignCount: uint32(r.SignCount), BackupEligible: r.BackupEligible == 1, BackupState: r.BackupState == 1, //nolint:gosec // counter fits uint32
		AAGUID: r.AAGUID, CreatedAt: r.CreatedAt.UTC(), LastUsedAt: utcPtr(r.LastUsedAt),
	}
}

// InsertPasskey stores a registered passkey.
func InsertPasskey(ctx context.Context, db bun.IDB, p domain.Passkey) error {
	row := passkeyRow{
		ID: p.ID, UserID: p.UserID, CredentialID: p.CredentialID, Name: p.Name, Credential: string(p.Credential),
		SignCount: int64(p.SignCount), BackupEligible: boolInt(p.BackupEligible), BackupState: boolInt(p.BackupState),
		AAGUID: p.AAGUID, CreatedAt: p.CreatedAt.UTC(),
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "passkeys.credential_id") {
			return domain.ErrPasskeyVerification
		}
		return fmt.Errorf("store: insert passkey: %w", err)
	}
	return nil
}

// ListPasskeys returns a user's passkeys, oldest first.
func ListPasskeys(ctx context.Context, db bun.IDB, userID string) ([]domain.Passkey, error) {
	var rows []passkeyRow
	if err := db.NewSelect().Model(&rows).Where("user_id = ?", userID).OrderExpr("created_at ASC, id ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list passkeys: %w", err)
	}
	out := make([]domain.Passkey, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// CountPasskeys counts a user's passkeys.
func CountPasskeys(ctx context.Context, db bun.IDB, userID string) (int, error) {
	n, err := db.NewSelect().Model((*passkeyRow)(nil)).Where("user_id = ?", userID).Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: count passkeys: %w", err)
	}
	return n, nil
}

// UpdatePasskeyUse records a successful assertion: the updated credential
// record, counter, backup state and time.
func UpdatePasskeyUse(ctx context.Context, db bun.IDB, id string, credential []byte, signCount uint32, backupState bool, now time.Time) error {
	_, err := db.NewUpdate().Model((*passkeyRow)(nil)).
		Set("credential = ?", string(credential)).Set("sign_count = ?", int64(signCount)).
		Set("backup_state = ?", boolInt(backupState)).Set("last_used_at = ?", now.UTC()).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update passkey: %w", err)
	}
	return nil
}

// RenamePasskey changes the label of one of the user's passkeys.
func RenamePasskey(ctx context.Context, db bun.IDB, userID, id, name string) error {
	res, err := db.NewUpdate().Model((*passkeyRow)(nil)).Set("name = ?", name).Where("id = ? AND user_id = ?", id, userID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: rename passkey: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrPasskeyNotFound
	}
	return nil
}

// DeletePasskey deletes one of the user's passkeys.
func DeletePasskey(ctx context.Context, db bun.IDB, userID, id string) error {
	res, err := db.NewDelete().Model((*passkeyRow)(nil)).Where("id = ? AND user_id = ?", id, userID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete passkey: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrPasskeyNotFound
	}
	return nil
}

// DeleteUserPasskeys deletes all of a user's passkeys.
func DeleteUserPasskeys(ctx context.Context, db bun.IDB, userID string) error {
	_, err := db.NewDelete().Model((*passkeyRow)(nil)).Where("user_id = ?", userID).Exec(ctx)
	return err
}

type recoveryCodeRow struct {
	bun.BaseModel `bun:"table:recovery_codes"`

	ID        string     `bun:"id,pk"`
	UserID    string     `bun:"user_id,notnull"`
	CodeHash  string     `bun:"code_hash,notnull"`
	CreatedAt time.Time  `bun:"created_at,notnull"`
	UsedAt    *time.Time `bun:"used_at"`
}

// ReplaceRecoveryCodes replaces a user's recovery codes with the given
// hashes (ids pairs index-wise).
func ReplaceRecoveryCodes(ctx context.Context, db bun.IDB, userID string, ids, hashes []string, now time.Time) error {
	if err := DeleteRecoveryCodes(ctx, db, userID); err != nil {
		return err
	}
	rows := make([]recoveryCodeRow, len(hashes))
	for i, h := range hashes {
		rows[i] = recoveryCodeRow{ID: ids[i], UserID: userID, CodeHash: h, CreatedAt: now.UTC()}
	}
	if len(rows) == 0 {
		return nil
	}
	if _, err := db.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert recovery codes: %w", err)
	}
	return nil
}

// UseRecoveryCode marks the user's unused code with this hash as used and
// reports whether there was one (single use, atomic).
func UseRecoveryCode(ctx context.Context, db bun.IDB, userID, hash string, now time.Time) (bool, error) {
	res, err := db.NewUpdate().Model((*recoveryCodeRow)(nil)).Set("used_at = ?", now.UTC()).
		Where("user_id = ? AND code_hash = ? AND used_at IS NULL", userID, hash).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: use recovery code: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoveryCodes returns how many unused codes a user has and when the set
// was generated.
func RecoveryCodes(ctx context.Context, db bun.IDB, userID string) (domain.RecoveryCodeStatus, error) {
	var rows []recoveryCodeRow
	if err := db.NewSelect().Model(&rows).Where("user_id = ?", userID).Scan(ctx); err != nil {
		return domain.RecoveryCodeStatus{}, fmt.Errorf("store: read recovery codes: %w", err)
	}
	var st domain.RecoveryCodeStatus
	for _, r := range rows {
		if r.UsedAt == nil {
			st.Remaining++
		}
		if st.GeneratedAt == nil {
			t := r.CreatedAt.UTC()
			st.GeneratedAt = &t
		}
	}
	return st, nil
}

// DeleteRecoveryCodes deletes a user's recovery codes.
func DeleteRecoveryCodes(ctx context.Context, db bun.IDB, userID string) error {
	_, err := db.NewDelete().Model((*recoveryCodeRow)(nil)).Where("user_id = ?", userID).Exec(ctx)
	return err
}

type invitationRow struct {
	bun.BaseModel `bun:"table:invitations"`

	ID             string     `bun:"id,pk"`
	Verifier       string     `bun:"verifier,notnull"`
	Email          *string    `bun:"email"`
	CreatedBy      *string    `bun:"created_by"`
	CreatedAt      time.Time  `bun:"created_at,notnull"`
	ExpiresAt      time.Time  `bun:"expires_at,notnull"`
	RedeemedAt     *time.Time `bun:"redeemed_at"`
	RedeemedUserID *string    `bun:"redeemed_user_id"`
	RevokedAt      *time.Time `bun:"revoked_at"`
}

func (r invitationRow) toDomain() domain.Invitation {
	return domain.Invitation{
		ID: r.ID, Email: deref(r.Email), CreatedBy: deref(r.CreatedBy), CreatedAt: r.CreatedAt.UTC(),
		ExpiresAt: r.ExpiresAt.UTC(), RedeemedAt: utcPtr(r.RedeemedAt), RedeemedUserID: deref(r.RedeemedUserID),
		RevokedAt: utcPtr(r.RevokedAt),
	}
}

// InsertInvitation stores an invitation with the verifier of its code.
func InsertInvitation(ctx context.Context, db bun.IDB, inv domain.Invitation, verifier string) error {
	row := invitationRow{
		ID: inv.ID, Verifier: verifier, Email: nullable(inv.Email), CreatedBy: nullable(inv.CreatedBy),
		CreatedAt: inv.CreatedAt.UTC(), ExpiresAt: inv.ExpiresAt.UTC(),
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert invitation: %w", err)
	}
	return nil
}

// ListInvitations returns up to limit invitations, newest first, before beforeID.
func ListInvitations(ctx context.Context, db bun.IDB, beforeID string, limit int) ([]domain.Invitation, error) {
	var rows []invitationRow
	q := db.NewSelect().Model(&rows).OrderExpr("id DESC").Limit(limit)
	if beforeID != "" {
		q = q.Where("id < ?", beforeID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list invitations: %w", err)
	}
	out := make([]domain.Invitation, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// GetInvitation returns an invitation by ID.
func GetInvitation(ctx context.Context, db bun.IDB, id string) (domain.Invitation, error) {
	var row invitationRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Invitation{}, domain.ErrInvitationNotFound
	}
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("store: read invitation: %w", err)
	}
	return row.toDomain(), nil
}

// FindRedeemableInvitation returns the pending invitation with verifier
// (domain.ErrCodeInvalid for unknown, used, revoked or expired codes).
func FindRedeemableInvitation(ctx context.Context, db bun.IDB, verifier string, now time.Time) (domain.Invitation, error) {
	var row invitationRow
	err := db.NewSelect().Model(&row).
		Where("verifier = ? AND redeemed_at IS NULL AND revoked_at IS NULL AND expires_at > ?", verifier, now.UTC()).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Invitation{}, domain.ErrCodeInvalid
	}
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("store: read invitation: %w", err)
	}
	return row.toDomain(), nil
}

// ConsumeInvitation marks a pending invitation redeemed. It is the atomic
// single-use step: a second redemption matches no row
// (domain.ErrCodeInvalid).
func ConsumeInvitation(ctx context.Context, db bun.IDB, id string, now time.Time) error {
	res, err := db.NewUpdate().Model((*invitationRow)(nil)).Set("redeemed_at = ?", now.UTC()).
		Where("id = ? AND redeemed_at IS NULL AND revoked_at IS NULL AND expires_at > ?", id, now.UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: consume invitation: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrCodeInvalid
	}
	return nil
}

// SetInvitationUser records the account created from an invitation.
func SetInvitationUser(ctx context.Context, db bun.IDB, id, userID string) error {
	_, err := db.NewUpdate().Model((*invitationRow)(nil)).Set("redeemed_user_id = ?", userID).Where("id = ?", id).Exec(ctx)
	return err
}

// RevokeInvitation revokes an invitation that was not redeemed yet.
func RevokeInvitation(ctx context.Context, db bun.IDB, id string, now time.Time) error {
	inv, err := GetInvitation(ctx, db, id)
	if err != nil {
		return err
	}
	if inv.RedeemedAt != nil {
		return domain.ErrInvitationRedeemed
	}
	if inv.RevokedAt != nil {
		return nil
	}
	_, err = db.NewUpdate().Model((*invitationRow)(nil)).Set("revoked_at = ?", now.UTC()).
		Where("id = ? AND redeemed_at IS NULL AND revoked_at IS NULL", id).Exec(ctx)
	return err
}

type accountResetRow struct {
	bun.BaseModel `bun:"table:account_resets"`

	ID        string     `bun:"id,pk"`
	UserID    string     `bun:"user_id,notnull"`
	Kind      string     `bun:"kind,notnull"`
	Verifier  string     `bun:"verifier,notnull"`
	CreatedBy *string    `bun:"created_by"`
	CreatedAt time.Time  `bun:"created_at,notnull"`
	ExpiresAt time.Time  `bun:"expires_at,notnull"`
	UsedAt    *time.Time `bun:"used_at"`
}

// InsertAccountReset stores a reset code verifier. Earlier unused resets
// of the same user are invalidated (only the newest code works).
func InsertAccountReset(ctx context.Context, db bun.IDB, r domain.AccountReset, verifier string) error {
	if _, err := db.NewUpdate().Model((*accountResetRow)(nil)).Set("used_at = ?", r.CreatedAt.UTC()).
		Where("user_id = ? AND used_at IS NULL", r.UserID).Exec(ctx); err != nil {
		return fmt.Errorf("store: supersede resets: %w", err)
	}
	row := accountResetRow{
		ID: r.ID, UserID: r.UserID, Kind: string(r.Kind), Verifier: verifier, CreatedBy: nullable(r.CreatedBy),
		CreatedAt: r.CreatedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC(),
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert reset: %w", err)
	}
	return nil
}

func (r accountResetRow) toDomain() domain.AccountReset {
	return domain.AccountReset{
		ID: r.ID, UserID: r.UserID, Kind: domain.AccountResetKind(r.Kind), CreatedBy: deref(r.CreatedBy),
		CreatedAt: r.CreatedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC(), UsedAt: utcPtr(r.UsedAt),
	}
}

// FindAccountReset returns the unexpired, unused reset with verifier
// (domain.ErrCodeInvalid otherwise).
func FindAccountReset(ctx context.Context, db bun.IDB, verifier string, now time.Time) (domain.AccountReset, error) {
	var row accountResetRow
	err := db.NewSelect().Model(&row).Where("verifier = ? AND used_at IS NULL AND expires_at > ?", verifier, now.UTC()).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccountReset{}, domain.ErrCodeInvalid
	}
	if err != nil {
		return domain.AccountReset{}, fmt.Errorf("store: read reset: %w", err)
	}
	return row.toDomain(), nil
}

// ConsumeAccountReset marks the reset used; it is the atomic single-use
// step (domain.ErrCodeInvalid when it was used or expired meanwhile).
func ConsumeAccountReset(ctx context.Context, db bun.IDB, id string, now time.Time) error {
	res, err := db.NewUpdate().Model((*accountResetRow)(nil)).Set("used_at = ?", now.UTC()).
		Where("id = ? AND used_at IS NULL AND expires_at > ?", id, now.UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: use reset: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrCodeInvalid
	}
	return nil
}

type groupRow struct {
	bun.BaseModel `bun:"table:groups"`

	ID        string    `bun:"id,pk"`
	Name      string    `bun:"name,notnull"`
	Revision  int64     `bun:"revision,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

type defaultGroupRow struct {
	bun.BaseModel `bun:"table:default_group"`

	Singleton int    `bun:"singleton,pk"`
	GroupID   string `bun:"group_id,notnull"`
}

// DefaultGroupID returns the current default group (always exactly one).
func DefaultGroupID(ctx context.Context, db bun.IDB) (string, error) {
	var row defaultGroupRow
	if err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx); err != nil {
		return "", fmt.Errorf("store: read default group: %w", err)
	}
	return row.GroupID, nil
}

// GetGroup returns a group.
func GetGroup(ctx context.Context, db bun.IDB, id string) (domain.Group, error) {
	var row groupRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Group{}, domain.ErrGroupNotFound
	}
	if err != nil {
		return domain.Group{}, fmt.Errorf("store: read group: %w", err)
	}
	def, err := DefaultGroupID(ctx, db)
	if err != nil {
		return domain.Group{}, err
	}
	return domain.Group{ID: row.ID, Name: row.Name, Default: row.ID == def, Revision: row.Revision,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}

type securitySettingsRow struct {
	bun.BaseModel `bun:"table:security_settings"`

	Singleton             int       `bun:"singleton,pk"`
	StrictPasswords       int       `bun:"strict_passwords,notnull"`
	MinPasswordLength     int       `bun:"min_password_length,notnull"`
	RequiredFactors       string    `bun:"required_factors,notnull"`
	EnrollmentGraceHours  int       `bun:"enrollment_grace_hours,notnull"`
	InvitationTTLHours    int       `bun:"invitation_ttl_hours,notnull"`
	PasswordResetTTLHours int       `bun:"password_reset_ttl_hours,notnull"`
	APITokensEnabled      int       `bun:"api_tokens_enabled,notnull"`
	APITokenMaxDays       int       `bun:"api_token_max_days,notnull"`
	APITokensNonExpiring  int       `bun:"api_tokens_non_expiring,notnull"`
	AllowStaySignedIn     int       `bun:"allow_stay_signed_in,notnull"`
	Revision              int64     `bun:"revision,notnull"`
	UpdatedAt             time.Time `bun:"updated_at,notnull"`
}

func (r securitySettingsRow) toDomain() domain.SecuritySettings {
	return domain.SecuritySettings{
		StrictPasswords: r.StrictPasswords == 1, MinPasswordLength: r.MinPasswordLength,
		RequiredFactors: domain.RequiredFactors(r.RequiredFactors), EnrollmentGraceHours: r.EnrollmentGraceHours,
		InvitationTTLHours: r.InvitationTTLHours, PasswordResetTTLHours: r.PasswordResetTTLHours,
		APITokensEnabled: r.APITokensEnabled == 1, APITokenMaxDays: r.APITokenMaxDays, APITokensNonExpiring: r.APITokensNonExpiring == 1,
		AllowStaySignedIn: r.AllowStaySignedIn == 1,
		Revision:          r.Revision, UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// GetSecuritySettings returns the instance sign-in policy.
func GetSecuritySettings(ctx context.Context, db bun.IDB) (domain.SecuritySettings, error) {
	var row securitySettingsRow
	if err := db.NewSelect().Model(&row).Where("singleton = 1").Scan(ctx); err != nil {
		return domain.SecuritySettings{}, fmt.Errorf("store: read security settings: %w", err)
	}
	return row.toDomain(), nil
}

// UpdateSecuritySettings replaces the policy if revision still matches
// (domain.ErrRevisionConflict otherwise) and returns the stored settings.
func UpdateSecuritySettings(ctx context.Context, db bun.IDB, revision int64, s domain.SecuritySettings, now time.Time) (domain.SecuritySettings, error) {
	res, err := db.NewUpdate().Model((*securitySettingsRow)(nil)).
		Set("strict_passwords = ?", boolInt(s.StrictPasswords)).Set("min_password_length = ?", s.MinPasswordLength).
		Set("required_factors = ?", string(s.RequiredFactors)).Set("enrollment_grace_hours = ?", s.EnrollmentGraceHours).
		Set("invitation_ttl_hours = ?", s.InvitationTTLHours).Set("password_reset_ttl_hours = ?", s.PasswordResetTTLHours).
		Set("api_tokens_enabled = ?", boolInt(s.APITokensEnabled)).Set("api_token_max_days = ?", s.APITokenMaxDays).
		Set("api_tokens_non_expiring = ?", boolInt(s.APITokensNonExpiring)).
		Set("allow_stay_signed_in = ?", boolInt(s.AllowStaySignedIn)).
		Set("revision = revision + 1").Set("updated_at = ?", now.UTC()).
		Where("singleton = 1 AND revision = ?", revision).Exec(ctx)
	if err != nil {
		return domain.SecuritySettings{}, fmt.Errorf("store: update security settings: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.SecuritySettings{}, domain.ErrRevisionConflict
	}
	return GetSecuritySettings(ctx, db)
}
