package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Notification channel persistence (#142). The sealed address never leaves
// this package inside a domain value: it is written with the channel and
// read only through NotificationChannelWithSecret, which returns it beside
// the channel it belongs to (one row read, so a send never pairs an address
// with another address's service, name or version).

type notificationChannelRow struct {
	bun.BaseModel `bun:"table:notification_channels"`

	ID                string     `bun:"id,pk"`
	Name              string     `bun:"name,notnull"`
	NameKey           string     `bun:"name_key,notnull"`
	Service           string     `bun:"service,notnull"`
	Target            string     `bun:"target,notnull"`
	Enabled           int        `bun:"enabled,notnull"`
	Subscriptions     string     `bun:"subscriptions,notnull"`
	AllEnvironments   int        `bun:"all_environments,notnull"`
	SecretSealed      string     `bun:"secret_sealed,notnull"`
	SecretFingerprint string     `bun:"secret_fingerprint,notnull"`
	SecretVersion     int        `bun:"secret_version,notnull"`
	SecretUpdatedAt   time.Time  `bun:"secret_updated_at,notnull"`
	LastResult        string     `bun:"last_result,notnull"`
	LastAttemptAt     *time.Time `bun:"last_attempt_at"`
	LastSuccessAt     *time.Time `bun:"last_success_at"`
	Revision          int64      `bun:"revision,notnull"`
	CreatedAt         time.Time  `bun:"created_at,notnull"`
	UpdatedAt         time.Time  `bun:"updated_at,notnull"`
}

type notificationChannelEnvironmentRow struct {
	bun.BaseModel `bun:"table:notification_channel_environments"`

	ChannelID     string `bun:"channel_id,pk"`
	EnvironmentID string `bun:"environment_id,pk"`
}

func fromNotificationChannel(c *domain.NotificationChannel, sealed string) (notificationChannelRow, error) {
	subs := c.Subscriptions.Normalize()
	kinds, err := json.Marshal(subs)
	if err != nil {
		return notificationChannelRow{}, fmt.Errorf("store: encode subscriptions: %w", err)
	}
	return notificationChannelRow{
		ID: c.ID, Name: c.Name, NameKey: NameKey(c.Name), Service: c.Service, Target: c.Target, Enabled: b2i(c.Enabled),
		Subscriptions: string(kinds), AllEnvironments: b2i(c.AllEnvironments), SecretSealed: sealed, SecretFingerprint: c.AddressFingerprint,
		SecretVersion: c.AddressVersion, SecretUpdatedAt: c.AddressUpdatedAt.UTC(), LastResult: c.LastResult,
		LastAttemptAt: utcPtr(c.LastAttemptAt), LastSuccessAt: utcPtr(c.LastSuccessAt), Revision: c.Revision,
		CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
	}, nil
}

func (r notificationChannelRow) toDomain(envs []string) (domain.NotificationChannel, error) {
	var subs domain.NotificationSubscriptions
	if err := json.Unmarshal([]byte(r.Subscriptions), &subs); err != nil {
		return domain.NotificationChannel{}, fmt.Errorf("store: decode subscriptions of notification channel %s: %w", r.ID, err)
	}
	return domain.NotificationChannel{
		ID: r.ID, Name: r.Name, Service: r.Service, Target: r.Target, Enabled: r.Enabled == 1, Subscriptions: subs.Normalize(),
		AllEnvironments: r.AllEnvironments == 1, EnvironmentIDs: envs,
		AddressFingerprint: r.SecretFingerprint,
		AddressVersion:     r.SecretVersion, AddressUpdatedAt: r.SecretUpdatedAt.UTC(), LastResult: r.LastResult,
		LastAttemptAt: utcPtr(r.LastAttemptAt), LastSuccessAt: utcPtr(r.LastSuccessAt), Revision: r.Revision,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}, nil
}

// InsertNotificationChannel stores a new channel with its sealed address
// and environment filter. Run it in a transaction.
func InsertNotificationChannel(ctx context.Context, db bun.IDB, c *domain.NotificationChannel, sealed string) error {
	row, err := fromNotificationChannel(c, sealed)
	if err != nil {
		return err
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "notification_channels.name_key") {
			return domain.ErrNotificationChannelNameTaken
		}
		return fmt.Errorf("store: insert notification channel: %w", err)
	}
	return replaceNotificationChannelEnvironments(ctx, db, c.ID, c.EnvironmentIDs)
}

// UpdateNotificationChannel writes the settings of c (and, when sealed is
// not empty, its new address with the reset send status) and replaces its
// environment filter when the stored revision still equals
// expectRevision. Run it in a transaction.
func UpdateNotificationChannel(ctx context.Context, db bun.IDB, c *domain.NotificationChannel, sealed string, expectRevision int64) error {
	row, err := fromNotificationChannel(c, sealed)
	if err != nil {
		return err
	}
	cols := []string{"name", "name_key", "service", "target", "enabled", "subscriptions", "all_environments", "revision",
		"updated_at"}
	if sealed != "" {
		cols = append(cols, "secret_sealed", "secret_fingerprint", "secret_version", "secret_updated_at", "last_result",
			"last_attempt_at", "last_success_at")
	}
	res, err := db.NewUpdate().Model(&row).Column(cols...).WherePK().Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "notification_channels.name_key") {
			return domain.ErrNotificationChannelNameTaken
		}
		return fmt.Errorf("store: update notification channel: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetNotificationChannel(ctx, db, c.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return replaceNotificationChannelEnvironments(ctx, db, c.ID, c.EnvironmentIDs)
}

func replaceNotificationChannelEnvironments(ctx context.Context, db bun.IDB, id string, envs []string) error {
	if _, err := db.NewDelete().Model((*notificationChannelEnvironmentRow)(nil)).Where("channel_id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("store: clear notification channel environments: %w", err)
	}
	if len(envs) == 0 {
		return nil
	}
	rows := make([]notificationChannelEnvironmentRow, 0, len(envs))
	for _, e := range envs {
		rows = append(rows, notificationChannelEnvironmentRow{ChannelID: id, EnvironmentID: e})
	}
	if _, err := db.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert notification channel environments: %w", err)
	}
	return nil
}

// RecordNotificationResult stores the outcome of a send through the
// address of version addressVersion (no revision change: it is status, not
// configuration). ok also stamps LastSuccessAt. When the address was
// replaced meanwhile nothing is recorded (the new address has not been
// tried) and recorded is false.
func RecordNotificationResult(ctx context.Context, db bun.IDB, id string, addressVersion int, at time.Time, result string,
	ok bool) (recorded bool, err error) {
	q := db.NewUpdate().Model((*notificationChannelRow)(nil)).Where("id = ?", id).Where("secret_version = ?", addressVersion).
		Set("last_attempt_at = ?", at.UTC()).Set("last_result = ?", result)
	if ok {
		q = q.Set("last_success_at = ?", at.UTC())
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("store: record notification result: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteNotificationChannel removes a channel (and its environment
// filter) when the revision matches.
func DeleteNotificationChannel(ctx context.Context, db bun.IDB, id string, expectRevision int64) error {
	res, err := db.NewDelete().Model((*notificationChannelRow)(nil)).Where("id = ?", id).Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: delete notification channel: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetNotificationChannel(ctx, db, id); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// GetNotificationChannel returns one channel (without its address).
func GetNotificationChannel(ctx context.Context, db bun.IDB, id string) (domain.NotificationChannel, error) {
	var row notificationChannelRow
	err := db.NewSelect().Model(&row).ExcludeColumn("secret_sealed").Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotificationChannel{}, domain.ErrNotificationChannelNotFound
	}
	if err != nil {
		return domain.NotificationChannel{}, fmt.Errorf("store: get notification channel: %w", err)
	}
	envs, err := notificationChannelEnvironments(ctx, db, []string{id})
	if err != nil {
		return domain.NotificationChannel{}, err
	}
	return row.toDomain(envs[id])
}

// ListNotificationChannels returns channels (without addresses) in ID
// (creation) order after afterID, at most limit (0 = all).
func ListNotificationChannels(ctx context.Context, db bun.IDB, afterID string, limit int) ([]domain.NotificationChannel, error) {
	var rows []notificationChannelRow
	q := db.NewSelect().Model(&rows).ExcludeColumn("secret_sealed").Order("id ASC")
	if afterID != "" {
		q = q.Where("id > ?", afterID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list notification channels: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	envs, err := notificationChannelEnvironments(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.NotificationChannel, 0, len(rows))
	for _, r := range rows {
		c, err := r.toDomain(envs[r.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// notificationChannelEnvironments returns the environment filter of each
// channel (sorted IDs; absent: every environment).
func notificationChannelEnvironments(ctx context.Context, db bun.IDB, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []notificationChannelEnvironmentRow
	if err := db.NewSelect().Model(&rows).Where("channel_id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list notification channel environments: %w", err)
	}
	for _, r := range rows {
		out[r.ChannelID] = append(out[r.ChannelID], r.EnvironmentID)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out, nil
}

// NotificationChannelWithSecret returns a channel together with its sealed
// address, both from the same row read: the address, its version
// (AddressVersion) and the channel's name and service always belong
// together, whatever changes run concurrently.
func NotificationChannelWithSecret(ctx context.Context, db bun.IDB, id string) (domain.NotificationChannel, string, error) {
	var row notificationChannelRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotificationChannel{}, "", domain.ErrNotificationChannelNotFound
	}
	if err != nil {
		return domain.NotificationChannel{}, "", fmt.Errorf("store: read notification channel address: %w", err)
	}
	envs, err := notificationChannelEnvironments(ctx, db, []string{id})
	if err != nil {
		return domain.NotificationChannel{}, "", err
	}
	c, err := row.toDomain(envs[id])
	if err != nil {
		return domain.NotificationChannel{}, "", err
	}
	return c, row.SecretSealed, nil
}
