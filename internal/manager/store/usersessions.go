package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Signed-in devices (#16): one row per browser session. The SCS session
// carries the row ID; internal/manager/auth owns every state change.

type userSessionRow struct {
	bun.BaseModel `bun:"table:user_sessions"`

	ID           string    `bun:"id,pk"`
	UserID       string    `bun:"user_id,notnull"`
	Epoch        int64     `bun:"epoch,notnull"`
	StaySignedIn int       `bun:"stay_signed_in,notnull"`
	CreatedAt    time.Time `bun:"created_at,notnull"`
	LastSeenAt   time.Time `bun:"last_seen_at,notnull"`
	IP           string    `bun:"ip,notnull"`
	UserAgent    string    `bun:"user_agent,notnull"`
}

func (r userSessionRow) toDomain() domain.UserSession {
	return domain.UserSession{
		ID: r.ID, UserID: r.UserID, Epoch: r.Epoch, StaySignedIn: r.StaySignedIn == 1,
		CreatedAt: r.CreatedAt.UTC(), LastSeenAt: r.LastSeenAt.UTC(), IP: r.IP, UserAgent: r.UserAgent,
	}
}

// SessionLimits are the idle timeouts and absolute lifetimes of normal and
// "Stay signed in" sessions.
type SessionLimits struct {
	Idle, Lifetime         time.Duration
	StayIdle, StayLifetime time.Duration
}

// InsertUserSession stores a new session.
func InsertUserSession(ctx context.Context, db bun.IDB, s domain.UserSession) error {
	row := userSessionRow{ID: s.ID, UserID: s.UserID, Epoch: s.Epoch, StaySignedIn: boolInt(s.StaySignedIn),
		CreatedAt: s.CreatedAt.UTC(), LastSeenAt: s.LastSeenAt.UTC(), IP: s.IP, UserAgent: s.UserAgent}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert session: %w", err)
	}
	return nil
}

// GetUserSession returns a session (domain.ErrUserSessionNotFound when it
// does not exist).
func GetUserSession(ctx context.Context, db bun.IDB, id string) (domain.UserSession, error) {
	var row userSessionRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UserSession{}, domain.ErrUserSessionNotFound
	}
	if err != nil {
		return domain.UserSession{}, fmt.Errorf("store: read session: %w", err)
	}
	return row.toDomain(), nil
}

// ListUserSessions lists the live sessions of a user (those of the user's
// current session epoch), most recently active first.
func ListUserSessions(ctx context.Context, db bun.IDB, userID string) ([]domain.UserSession, error) {
	var rows []userSessionRow
	err := db.NewRaw(`SELECT s.id, s.user_id, s.epoch, s.stay_signed_in, s.created_at, s.last_seen_at, s.ip, s.user_agent
		FROM user_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.user_id = ? AND s.epoch = u.session_epoch
		ORDER BY s.last_seen_at DESC, s.id DESC`, userID).Scan(ctx, &rows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	out := make([]domain.UserSession, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// TouchUserSession records activity of a session.
func TouchUserSession(ctx context.Context, db bun.IDB, id string, now time.Time, ip string) error {
	_, err := db.NewUpdate().Model((*userSessionRow)(nil)).Set("last_seen_at = ?", now.UTC()).Set("ip = ?", ip).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

// SetUserSessionEpoch moves a session to a new session epoch (the caller's
// session continues after a password change).
func SetUserSessionEpoch(ctx context.Context, db bun.IDB, id string, epoch int64) error {
	_, err := db.NewUpdate().Model((*userSessionRow)(nil)).Set("epoch = ?", epoch).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: set session epoch: %w", err)
	}
	return nil
}

// EndStaySignedIn moves every "Stay signed in" session to the normal
// limits (the owner no longer allows it).
func EndStaySignedIn(ctx context.Context, db bun.IDB) error {
	_, err := db.NewUpdate().Model((*userSessionRow)(nil)).Set("stay_signed_in = 0").Where("stay_signed_in = 1").Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: end stay signed in: %w", err)
	}
	return nil
}

// DeleteUserSession removes a session of userID (domain.ErrUserSessionNotFound
// when there is none, or it belongs to someone else).
func DeleteUserSession(ctx context.Context, db bun.IDB, id, userID string) error {
	res, err := db.NewDelete().Model((*userSessionRow)(nil)).Where("id = ? AND user_id = ?", id, userID).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrUserSessionNotFound
	}
	return nil
}

// DeleteOtherUserSessions removes every session of userID except keepID
// and returns the removed IDs.
func DeleteOtherUserSessions(ctx context.Context, db bun.IDB, userID, keepID string) ([]string, error) {
	var ids []string
	err := db.NewRaw(`DELETE FROM user_sessions WHERE user_id = ? AND id <> ? RETURNING id`, userID, keepID).Scan(ctx, &ids)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: delete sessions: %w", err)
	}
	return ids, nil
}

// LiveUserSessionIDs returns which of ids are sessions of an active user in
// the user's current session epoch.
func LiveUserSessionIDs(ctx context.Context, db bun.IDB, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	var live []string
	err := db.NewRaw(`SELECT s.id FROM user_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id IN (?) AND s.epoch = u.session_epoch AND u.status = ?`, bun.List(ids), string(domain.UserActive)).Scan(ctx, &live)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: read sessions: %w", err)
	}
	for _, id := range live {
		out[id] = true
	}
	return out, nil
}

// UserSessionIDs returns the IDs of every stored session.
func UserSessionIDs(ctx context.Context, db bun.IDB) (map[string]bool, error) {
	var ids []string
	if err := db.NewSelect().Model((*userSessionRow)(nil)).Column("id").Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// DeleteStaleUserSessions removes the sessions that ended: made in an
// older session epoch, idle for longer than their idle timeout, or older
// than their lifetime. It returns how many it removed.
func DeleteStaleUserSessions(ctx context.Context, db bun.IDB, now time.Time, l SessionLimits) (int, error) {
	now = now.UTC()
	res, err := db.NewRaw(`DELETE FROM user_sessions WHERE
		epoch <> (SELECT u.session_epoch FROM users u WHERE u.id = user_sessions.user_id)
		OR (stay_signed_in = 0 AND (created_at <= ? OR last_seen_at <= ?))
		OR (stay_signed_in = 1 AND (created_at <= ? OR last_seen_at <= ?))`,
		now.Add(-l.Lifetime), now.Add(-l.Idle), now.Add(-l.StayLifetime), now.Add(-l.StayIdle)).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: delete ended sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
