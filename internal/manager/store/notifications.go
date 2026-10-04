package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Notification persistence: the notifications table (finished runs; their
// messages are alert_deliveries rows with notification_id).

type notificationRow struct {
	bun.BaseModel `bun:"table:notifications"`

	ID            string    `bun:"id,pk"`
	Kind          string    `bun:"kind,notnull"`
	Outcome       string    `bun:"outcome,notnull"`
	EnvironmentID string    `bun:"environment_id,notnull"`
	JobID         string    `bun:"job_id,notnull"`
	JobKind       string    `bun:"job_kind,notnull"`
	Targets       string    `bun:"targets,notnull"`
	Origin        string    `bun:"origin,notnull"`
	Title         string    `bun:"title,notnull"`
	Facts         string    `bun:"facts,notnull"`
	CreatedAt     time.Time `bun:"created_at,notnull"`
}

func fromNotification(n *domain.Notification) (notificationRow, error) {
	targets := make([]alertTargetRow, 0, len(n.Targets))
	for _, t := range n.Targets {
		targets = append(targets, alertTargetRow{Type: string(t.Type), ID: t.ID, EnvironmentID: t.EnvironmentID})
	}
	tb, err := json.Marshal(targets)
	if err != nil {
		return notificationRow{}, fmt.Errorf("store: encode notification targets: %w", err)
	}
	facts := n.Facts
	if facts == nil {
		facts = map[string]string{}
	}
	fb, err := json.Marshal(facts)
	if err != nil {
		return notificationRow{}, fmt.Errorf("store: encode notification facts: %w", err)
	}
	return notificationRow{
		ID: n.ID, Kind: string(n.Kind), Outcome: string(n.Outcome), EnvironmentID: n.EnvironmentID, JobID: n.JobID,
		JobKind: string(n.JobKind), Targets: string(tb), Origin: string(n.Origin), Title: n.Title, Facts: string(fb),
		CreatedAt: n.CreatedAt.UTC(),
	}, nil
}

func (r notificationRow) toDomain() (domain.Notification, error) {
	var targets []alertTargetRow
	if err := json.Unmarshal([]byte(r.Targets), &targets); err != nil {
		return domain.Notification{}, fmt.Errorf("store: decode targets of notification %s: %w", r.ID, err)
	}
	facts := map[string]string{}
	if err := json.Unmarshal([]byte(r.Facts), &facts); err != nil {
		return domain.Notification{}, fmt.Errorf("store: decode facts of notification %s: %w", r.ID, err)
	}
	n := domain.Notification{
		ID: r.ID, Kind: domain.NotificationEventKind(r.Kind), Outcome: domain.NotificationOutcome(r.Outcome),
		EnvironmentID: r.EnvironmentID, JobID: r.JobID, JobKind: domain.JobKind(r.JobKind), Origin: domain.JobOrigin(r.Origin),
		Title: r.Title, Facts: facts, CreatedAt: r.CreatedAt.UTC(),
	}
	for _, t := range targets {
		n.Targets = append(n.Targets, domain.JobTarget{Type: domain.TargetType(t.Type), ID: t.ID, EnvironmentID: t.EnvironmentID})
	}
	return n, nil
}

// InsertNotification stores a notification (in the transaction that
// writes its messages).
func InsertNotification(ctx context.Context, db bun.IDB, n *domain.Notification) error {
	row, err := fromNotification(n)
	if err != nil {
		return err
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert notification: %w", err)
	}
	return nil
}

// GetNotification returns one notification.
func GetNotification(ctx context.Context, db bun.IDB, id string) (domain.Notification, error) {
	var row notificationRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Notification{}, domain.ErrNotificationNotFound
	}
	if err != nil {
		return domain.Notification{}, fmt.Errorf("store: get notification: %w", err)
	}
	return row.toDomain()
}

// ListNotifications returns notifications matching f, newest first (ID
// order, UUIDv7), strictly before beforeID ("" from the newest), at most
// limit (0 = all).
func ListNotifications(ctx context.Context, db bun.IDB, f domain.NotificationFilter, beforeID string, limit int) ([]domain.Notification, error) {
	var rows []notificationRow
	q := db.NewSelect().Model(&rows).Order("id DESC")
	if f.Kind != "" {
		q = q.Where("kind = ?", string(f.Kind))
	}
	if f.Outcome != "" {
		q = q.Where("outcome = ?", string(f.Outcome))
	}
	if f.EnvironmentID != "" {
		q = q.Where("environment_id = ?", f.EnvironmentID)
	}
	if !f.Since.IsZero() {
		q = q.Where("created_at >= ?", f.Since.UTC())
	}
	if beforeID != "" {
		q = q.Where("id < ?", beforeID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list notifications: %w", err)
	}
	out := make([]domain.Notification, 0, len(rows))
	for _, r := range rows {
		n, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// PurgeNotifications deletes notifications created before cutoff (their
// deliveries cascade) and returns how many.
func PurgeNotifications(ctx context.Context, db bun.IDB, cutoff time.Time) (int64, error) {
	res, err := db.NewDelete().Model((*notificationRow)(nil)).Where("created_at < ?", cutoff.UTC()).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: purge notifications: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
