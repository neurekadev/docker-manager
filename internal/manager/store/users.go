package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/dockyard/internal/domain"
)

// Identity persistence (#16). internal/manager/auth owns every state
// change; these functions map rows and enforce compare-and-set updates.

type userRow struct {
	bun.BaseModel `bun:"table:users"`

	ID                   string     `bun:"id,pk"`
	Username             string     `bun:"username,notnull"`
	DisplayName          string     `bun:"display_name,notnull"`
	Email                *string    `bun:"email"`
	IsOwner              int        `bun:"is_owner,notnull"`
	GroupID              string     `bun:"group_id,notnull"`
	Status               string     `bun:"status,notnull"`
	PasswordHash         *string    `bun:"password_hash"`
	PasswordChangedAt    *time.Time `bun:"password_changed_at"`
	TOTPSeed             *string    `bun:"totp_seed"`
	TOTPEnabledAt        *time.Time `bun:"totp_enabled_at"`
	TOTPLastStep         int64      `bun:"totp_last_step,notnull"`
	TOTPPendingSeed      *string    `bun:"totp_pending_seed"`
	TOTPPendingExpiresAt *time.Time `bun:"totp_pending_expires_at"`
	WebAuthnHandle       []byte     `bun:"webauthn_handle,notnull"`
	SessionEpoch         int64      `bun:"session_epoch,notnull"`
	EnrollmentDeadline   *time.Time `bun:"enrollment_deadline"`
	InvitationID         *string    `bun:"invitation_id"`
	Revision             int64      `bun:"revision,notnull"`
	CreatedAt            time.Time  `bun:"created_at,notnull"`
	UpdatedAt            time.Time  `bun:"updated_at,notnull"`
	DisabledAt           *time.Time `bun:"disabled_at"`
	LastSignInAt         *time.Time `bun:"last_sign_in_at"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r userRow) toDomain() domain.User {
	return domain.User{
		ID: r.ID, Username: r.Username, DisplayName: r.DisplayName, Email: deref(r.Email), Owner: r.IsOwner == 1,
		GroupID: r.GroupID, Status: domain.UserStatus(r.Status), WebAuthnHandle: r.WebAuthnHandle,
		SessionEpoch: r.SessionEpoch, HasPassword: r.PasswordHash != nil, PasswordChangedAt: utcPtr(r.PasswordChangedAt),
		TOTPEnabled: r.TOTPSeed != nil, EnrollmentDeadline: utcPtr(r.EnrollmentDeadline), InvitationID: deref(r.InvitationID),
		Revision: r.Revision, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		DisabledAt: utcPtr(r.DisabledAt), LastSignInAt: utcPtr(r.LastSignInAt),
	}
}

func (r userRow) credentials() domain.UserCredentials {
	return domain.UserCredentials{
		PasswordHash: deref(r.PasswordHash), TOTPSeedSealed: deref(r.TOTPSeed), TOTPLastStep: r.TOTPLastStep,
		TOTPPendingSealed: deref(r.TOTPPendingSeed), TOTPPendingExpiry: utcPtr(r.TOTPPendingExpiresAt),
	}
}

// uniqueViolation reports a UNIQUE constraint failure on column (e.g.
// "users.username").
func uniqueViolation(err error, column string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: "+column)
}

// CreateUser inserts a user. It returns domain.ErrUsernameTaken for a
// duplicate username and domain.ErrSetupComplete when an owner exists
// already (the partial unique index makes concurrent setups race-safe).
func CreateUser(ctx context.Context, db bun.IDB, u domain.NewUser) (domain.User, error) {
	now := u.CreatedAt.UTC()
	row := userRow{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: nullable(u.Email), IsOwner: boolInt(u.Owner),
		GroupID: u.GroupID, Status: string(domain.UserActive), PasswordHash: nullable(u.PasswordHash),
		WebAuthnHandle: u.WebAuthnHandle, SessionEpoch: 1, EnrollmentDeadline: utcPtr(u.EnrollmentDeadline),
		InvitationID: nullable(u.InvitationID), Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if u.PasswordHash != "" {
		row.PasswordChangedAt = &now
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		switch {
		case uniqueViolation(err, "users.username"):
			return domain.User{}, domain.ErrUsernameTaken
		case uniqueViolation(err, "users.is_owner"):
			return domain.User{}, domain.ErrSetupComplete
		}
		return domain.User{}, fmt.Errorf("store: create user: %w", err)
	}
	return row.toDomain(), nil
}

func getUserWhere(ctx context.Context, db bun.IDB, where string, arg any) (userRow, error) {
	var row userRow
	err := db.NewSelect().Model(&row).Where(where, arg).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return row, domain.ErrUserNotFound
	}
	if err != nil {
		return row, fmt.Errorf("store: read user: %w", err)
	}
	return row, nil
}

// GetUser returns a user by ID.
func GetUser(ctx context.Context, db bun.IDB, id string) (domain.User, error) {
	row, err := getUserWhere(ctx, db, "id = ?", id)
	return row.toDomain(), err
}

// GetUserWithCredentials returns a user and its stored secrets.
func GetUserWithCredentials(ctx context.Context, db bun.IDB, id string) (domain.User, domain.UserCredentials, error) {
	row, err := getUserWhere(ctx, db, "id = ?", id)
	return row.toDomain(), row.credentials(), err
}

// GetUserByUsername returns a user and its secrets by (case-insensitive) username.
func GetUserByUsername(ctx context.Context, db bun.IDB, username string) (domain.User, domain.UserCredentials, error) {
	row, err := getUserWhere(ctx, db, "username = ?", username)
	return row.toDomain(), row.credentials(), err
}

// GetUserByHandle returns a user by WebAuthn user handle.
func GetUserByHandle(ctx context.Context, db bun.IDB, handle []byte) (domain.User, error) {
	row, err := getUserWhere(ctx, db, "webauthn_handle = ?", handle)
	return row.toDomain(), err
}

// OwnerID returns the owner's user ID, or ok=false before first-run setup.
func OwnerID(ctx context.Context, db bun.IDB) (string, bool, error) {
	var id string
	err := db.NewSelect().Model((*userRow)(nil)).Column("id").Where("is_owner = 1").Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read owner: %w", err)
	}
	return id, true, nil
}

// ListUsers returns up to limit users ordered by ID after afterID.
func ListUsers(ctx context.Context, db bun.IDB, afterID string, limit int) ([]domain.User, error) {
	var rows []userRow
	q := db.NewSelect().Model(&rows).OrderExpr("id ASC").Limit(limit)
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	out := make([]domain.User, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// SessionEpochs returns the session epoch and active flag of each existing
// user in ids (missing users are absent from the result).
func SessionEpochs(ctx context.Context, db bun.IDB, ids []string) (map[string]UserSessionState, error) {
	out := map[string]UserSessionState{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []userRow
	if err := db.NewSelect().Model(&rows).Column("id", "session_epoch", "status").Where("id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: read session epochs: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = UserSessionState{Epoch: r.SessionEpoch, Active: r.Status == string(domain.UserActive)}
	}
	return out, nil
}

// UserSessionState is what live sessions are checked against.
type UserSessionState struct {
	Epoch  int64
	Active bool
}

// updateUser applies set clauses to one user, bumping revision and
// updated_at; ErrUserNotFound when nothing matched.
func updateUser(ctx context.Context, db bun.IDB, id string, now time.Time, where string, whereArgs []any, sets ...setClause) (bool, error) {
	q := db.NewUpdate().Model((*userRow)(nil)).
		Set("revision = revision + 1").Set("updated_at = ?", now.UTC()).
		Where("id = ?", id)
	for _, s := range sets {
		q = q.Set(s.expr, s.args...)
	}
	if where != "" {
		q = q.Where(where, whereArgs...)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: update user: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

type setClause struct {
	expr string
	args []any
}

func set(expr string, args ...any) setClause { return setClause{expr, args} }

func mustUpdate(ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrUserNotFound
	}
	return nil
}

// SetPasswordHash stores a new password hash (and change time when changed
// is true; upgrade-on-login rehashes pass false).
func SetPasswordHash(ctx context.Context, db bun.IDB, id, hash string, changed bool, now time.Time) error {
	sets := []setClause{set("password_hash = ?", hash)}
	if changed {
		sets = append(sets, set("password_changed_at = ?", now.UTC()))
	}
	return mustUpdate(updateUser(ctx, db, id, now, "", nil, sets...))
}

// BumpSessionEpoch ends every session of the user (sessions carry the epoch
// they were created in) and returns the new epoch.
func BumpSessionEpoch(ctx context.Context, db bun.IDB, id string, now time.Time) (int64, error) {
	if err := mustUpdate(updateUser(ctx, db, id, now, "", nil, set("session_epoch = session_epoch + 1"))); err != nil {
		return 0, err
	}
	var epoch int64
	if err := db.NewSelect().Model((*userRow)(nil)).Column("session_epoch").Where("id = ?", id).Scan(ctx, &epoch); err != nil {
		return 0, fmt.Errorf("store: read session epoch: %w", err)
	}
	return epoch, nil
}

// BumpAllSessionEpochs ends the sessions of every user and returns their IDs.
func BumpAllSessionEpochs(ctx context.Context, db bun.IDB, now time.Time) ([]string, error) {
	var ids []string
	if err := db.NewSelect().Model((*userRow)(nil)).Column("id").Scan(ctx, &ids); err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	if _, err := db.NewUpdate().Model((*userRow)(nil)).Set("session_epoch = session_epoch + 1").
		Set("updated_at = ?", now.UTC()).Where("1 = 1").Exec(ctx); err != nil {
		return nil, fmt.Errorf("store: bump session epochs: %w", err)
	}
	return ids, nil
}

// SetLastSignIn records a successful sign-in.
func SetLastSignIn(ctx context.Context, db bun.IDB, id string, now time.Time) error {
	_, err := db.NewUpdate().Model((*userRow)(nil)).Set("last_sign_in_at = ?", now.UTC()).Where("id = ?", id).Exec(ctx)
	return err
}

// PatchUser applies an owner edit if the revision still matches. It
// returns domain.ErrRevisionConflict on a stale revision.
func PatchUser(ctx context.Context, db bun.IDB, id string, revision int64, p domain.UserPatch, now time.Time) (domain.User, error) {
	var sets []setClause
	if p.DisplayName != nil {
		sets = append(sets, set("display_name = ?", *p.DisplayName))
	}
	if p.Email != nil {
		sets = append(sets, set("email = ?", nullable(*p.Email)))
	}
	if p.GroupID != nil {
		sets = append(sets, set("group_id = ?", *p.GroupID))
	}
	if p.Status != nil {
		sets = append(sets, set("status = ?", string(*p.Status)))
		if *p.Status == domain.UserDisabled {
			sets = append(sets, set("disabled_at = ?", now.UTC()), set("session_epoch = session_epoch + 1"))
		} else {
			sets = append(sets, set("disabled_at = NULL"))
		}
	}
	ok, err := updateUser(ctx, db, id, now, "revision = ?", []any{revision}, sets...)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return domain.User{}, domain.ErrGroupNotFound
		}
		if strings.Contains(err.Error(), "CHECK constraint failed") {
			return domain.User{}, domain.ErrOwnerProtected
		}
		return domain.User{}, err
	}
	if !ok {
		if _, err := GetUser(ctx, db, id); err != nil {
			return domain.User{}, err
		}
		return domain.User{}, domain.ErrRevisionConflict
	}
	return GetUser(ctx, db, id)
}

// DeleteUser deletes a non-owner user (the database refuses the owner).
func DeleteUser(ctx context.Context, db bun.IDB, id string) error {
	res, err := db.NewDelete().Model((*userRow)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "owner cannot be deleted") {
			return domain.ErrOwnerProtected
		}
		return fmt.Errorf("store: delete user: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrUserNotFound
	}
	return nil
}

// SetEnrollmentDeadline sets (or clears, with nil) the factor enrollment deadline.
func SetEnrollmentDeadline(ctx context.Context, db bun.IDB, id string, deadline *time.Time, now time.Time) error {
	return mustUpdate(updateUser(ctx, db, id, now, "", nil, set("enrollment_deadline = ?", utcPtr(deadline))))
}

// SetEnrollmentDeadlineWhereUnset gives every non-owner user without a
// deadline the given one (a stricter policy starts a grace period).
func SetEnrollmentDeadlineWhereUnset(ctx context.Context, db bun.IDB, deadline, now time.Time) error {
	_, err := db.NewUpdate().Model((*userRow)(nil)).Set("enrollment_deadline = ?", deadline.UTC()).
		Set("updated_at = ?", now.UTC()).Where("enrollment_deadline IS NULL AND is_owner = 0").Exec(ctx)
	return err
}

// SetTOTPPending stores a sealed, not yet verified TOTP seed.
func SetTOTPPending(ctx context.Context, db bun.IDB, id, sealed string, expires, now time.Time) error {
	return mustUpdate(updateUser(ctx, db, id, now, "", nil,
		set("totp_pending_seed = ?", sealed), set("totp_pending_expires_at = ?", expires.UTC())))
}

// ActivateTOTP turns the pending seed into the active one (re-sealed for
// its new context) and records the verified step. It fails when the
// pending seed changed meanwhile.
func ActivateTOTP(ctx context.Context, db bun.IDB, id, pendingSealed, activeSealed string, step int64, now time.Time) error {
	ok, err := updateUser(ctx, db, id, now, "totp_pending_seed = ?", []any{pendingSealed},
		set("totp_seed = ?", activeSealed), set("totp_enabled_at = ?", now.UTC()), set("totp_last_step = ?", step),
		set("totp_pending_seed = NULL"), set("totp_pending_expires_at = NULL"))
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrNoPendingFlow
	}
	return nil
}

// AdvanceTOTPStep records step as the last accepted TOTP step if it is
// later than the stored one (compare-and-set: concurrent uses of one code
// cannot both succeed). It reports whether the step was accepted.
func AdvanceTOTPStep(ctx context.Context, db bun.IDB, id string, step int64) (bool, error) {
	res, err := db.NewUpdate().Model((*userRow)(nil)).Set("totp_last_step = ?", step).
		Where("id = ? AND totp_seed IS NOT NULL AND totp_last_step < ?", id, step).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: advance totp step: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ClearTOTP removes the active and pending TOTP seeds.
func ClearTOTP(ctx context.Context, db bun.IDB, id string, now time.Time) error {
	return mustUpdate(updateUser(ctx, db, id, now, "", nil,
		set("totp_seed = NULL"), set("totp_enabled_at = NULL"), set("totp_last_step = 0"),
		set("totp_pending_seed = NULL"), set("totp_pending_expires_at = NULL")))
}
