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

// Alert persistence (#159): the alerts table (at most one firing row per
// dedupe key, alerts_firing_key) and the outbox of their messages to
// notification channels (alert_deliveries).

type alertRow struct {
	bun.BaseModel `bun:"table:alerts"`

	ID              string     `bun:"id,pk"`
	DedupeKey       string     `bun:"dedupe_key,notnull"`
	Kind            string     `bun:"kind,notnull"`
	Severity        string     `bun:"severity,notnull"`
	State           string     `bun:"state,notnull"`
	EnvironmentID   string     `bun:"environment_id,notnull"`
	ResourceType    string     `bun:"resource_type,notnull"`
	ResourceID      string     `bun:"resource_id,notnull"`
	JobKind         string     `bun:"job_kind,notnull"`
	Targets         string     `bun:"targets,notnull"`
	Title           string     `bun:"title,notnull"`
	Facts           string     `bun:"facts,notnull"`
	Fingerprint     string     `bun:"fingerprint,notnull"`
	Escalation      int        `bun:"escalation,notnull"`
	StartedAt       time.Time  `bun:"started_at,notnull"`
	UpdatedAt       time.Time  `bun:"updated_at,notnull"`
	LastSeenAt      time.Time  `bun:"last_seen_at,notnull"`
	ResolvedAt      *time.Time `bun:"resolved_at"`
	Resolution      string     `bun:"resolution,notnull"`
	DismissedAt     *time.Time `bun:"dismissed_at"`
	DismissedBy     string     `bun:"dismissed_by,notnull"`
	DismissedByName string     `bun:"dismissed_by_name,notnull"`
	Revision        int64      `bun:"revision,notnull"`
}

type alertTargetRow struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	EnvironmentID string `json:"environmentId,omitempty"`
}

func fromAlert(a *domain.Alert) (alertRow, error) {
	targets := make([]alertTargetRow, 0, len(a.Targets))
	for _, t := range a.Targets {
		targets = append(targets, alertTargetRow{Type: string(t.Type), ID: t.ID, EnvironmentID: t.EnvironmentID})
	}
	tb, err := json.Marshal(targets)
	if err != nil {
		return alertRow{}, fmt.Errorf("store: encode alert targets: %w", err)
	}
	facts := a.Facts
	if facts == nil {
		facts = map[string]string{}
	}
	fb, err := json.Marshal(facts)
	if err != nil {
		return alertRow{}, fmt.Errorf("store: encode alert facts: %w", err)
	}
	return alertRow{
		ID: a.ID, DedupeKey: a.DedupeKey, Kind: string(a.Kind), Severity: string(a.Severity), State: string(a.State),
		EnvironmentID: a.EnvironmentID, ResourceType: a.ResourceType, ResourceID: a.ResourceID, JobKind: string(a.JobKind),
		Targets: string(tb), Title: a.Title, Facts: string(fb), Fingerprint: a.Fingerprint, Escalation: a.Escalation, StartedAt: a.StartedAt.UTC(),
		UpdatedAt: a.UpdatedAt.UTC(), LastSeenAt: a.LastSeenAt.UTC(), ResolvedAt: utcPtr(a.ResolvedAt), Resolution: a.Resolution,
		DismissedAt: utcPtr(a.DismissedAt), DismissedBy: a.DismissedBy, DismissedByName: a.DismissedByName, Revision: a.Revision,
	}, nil
}

func (r alertRow) toDomain() (domain.Alert, error) {
	var targets []alertTargetRow
	if err := json.Unmarshal([]byte(r.Targets), &targets); err != nil {
		return domain.Alert{}, fmt.Errorf("store: decode targets of alert %s: %w", r.ID, err)
	}
	facts := map[string]string{}
	if err := json.Unmarshal([]byte(r.Facts), &facts); err != nil {
		return domain.Alert{}, fmt.Errorf("store: decode facts of alert %s: %w", r.ID, err)
	}
	a := domain.Alert{
		ID: r.ID, DedupeKey: r.DedupeKey, Kind: domain.NotificationEventKind(r.Kind), Severity: domain.AlertSeverity(r.Severity),
		State: domain.AlertState(r.State), EnvironmentID: r.EnvironmentID, ResourceType: r.ResourceType, ResourceID: r.ResourceID,
		JobKind: domain.JobKind(r.JobKind), Title: r.Title, Facts: facts, Fingerprint: r.Fingerprint, Escalation: r.Escalation,
		StartedAt: r.StartedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(), LastSeenAt: r.LastSeenAt.UTC(), ResolvedAt: utcPtr(r.ResolvedAt), Resolution: r.Resolution,
		DismissedAt: utcPtr(r.DismissedAt), DismissedBy: r.DismissedBy, DismissedByName: r.DismissedByName, Revision: r.Revision,
	}
	for _, t := range targets {
		a.Targets = append(a.Targets, domain.JobTarget{Type: domain.TargetType(t.Type), ID: t.ID, EnvironmentID: t.EnvironmentID})
	}
	return a, nil
}

func alertsOf(rows []alertRow) ([]domain.Alert, error) {
	out := make([]domain.Alert, 0, len(rows))
	for _, r := range rows {
		a, err := r.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// ErrAlertFiring is returned by InsertAlert when a firing alert with the
// same dedupe key exists (alerts_firing_key).
var ErrAlertFiring = errors.New("store: a firing alert with this dedupe key exists")

// InsertAlert stores a new alert.
func InsertAlert(ctx context.Context, db bun.IDB, a *domain.Alert) error {
	row, err := fromAlert(a)
	if err != nil {
		return err
	}
	if _, err := db.NewInsert().Model(&row).Exec(ctx); err != nil {
		if uniqueViolation(err, "alerts.dedupe_key") {
			return ErrAlertFiring
		}
		return fmt.Errorf("store: insert alert: %w", err)
	}
	return nil
}

// UpdateAlert writes every field of a when the stored revision still
// equals expectRevision (domain.ErrRevisionMismatch otherwise).
func UpdateAlert(ctx context.Context, db bun.IDB, a *domain.Alert, expectRevision int64) error {
	row, err := fromAlert(a)
	if err != nil {
		return err
	}
	res, err := db.NewUpdate().Model(&row).ExcludeColumn("id", "dedupe_key", "kind", "started_at").WherePK().
		Where("revision = ?", expectRevision).Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: update alert: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if _, gerr := GetAlert(ctx, db, a.ID); gerr != nil {
			return gerr
		}
		return domain.ErrRevisionMismatch
	}
	return nil
}

// RekeyAlert gives a firing alert another dedupe key (a disk's alert
// follows its disk to another path). No revision change: the key is not
// shown; the caller updates what users see. ErrAlertFiring when another
// firing alert holds key.
func RekeyAlert(ctx context.Context, db bun.IDB, id, key string) error {
	res, err := db.NewUpdate().Model((*alertRow)(nil)).Set("dedupe_key = ?", key).Where("id = ?", id).
		Where("state = ?", string(domain.AlertFiring)).Exec(ctx)
	if err != nil {
		if uniqueViolation(err, "alerts.dedupe_key") {
			return ErrAlertFiring
		}
		return fmt.Errorf("store: rekey alert: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrAlertNotFound
	}
	return nil
}

// TouchAlerts sets last_seen_at of firing alerts (no revision change: the
// problem was observed again, nothing users see changed).
func TouchAlerts(ctx context.Context, db bun.IDB, ids []string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := db.NewUpdate().Model((*alertRow)(nil)).Set("last_seen_at = ?", at.UTC()).
		Where("id IN (?)", bun.List(ids)).Where("state = ?", string(domain.AlertFiring)).Exec(ctx); err != nil {
		return fmt.Errorf("store: touch alerts: %w", err)
	}
	return nil
}

// GetAlert returns one alert.
func GetAlert(ctx context.Context, db bun.IDB, id string) (domain.Alert, error) {
	var row alertRow
	err := db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Alert{}, domain.ErrAlertNotFound
	}
	if err != nil {
		return domain.Alert{}, fmt.Errorf("store: get alert: %w", err)
	}
	return row.toDomain()
}

// FiringAlert returns the firing alert of a dedupe key (found false when
// none fires).
func FiringAlert(ctx context.Context, db bun.IDB, key string) (domain.Alert, bool, error) {
	var row alertRow
	err := db.NewSelect().Model(&row).Where("dedupe_key = ?", key).Where("state = ?", string(domain.AlertFiring)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Alert{}, false, nil
	}
	if err != nil {
		return domain.Alert{}, false, fmt.Errorf("store: get firing alert: %w", err)
	}
	a, err := row.toDomain()
	return a, err == nil, err
}

// FiringAlerts returns the firing alerts, optionally of one kind and
// environment ("" for any), oldest first.
func FiringAlerts(ctx context.Context, db bun.IDB, kind domain.NotificationEventKind, environmentID string) ([]domain.Alert, error) {
	var rows []alertRow
	q := db.NewSelect().Model(&rows).Where("state = ?", string(domain.AlertFiring)).Order("id ASC")
	if kind != "" {
		q = q.Where("kind = ?", string(kind))
	}
	if environmentID != "" {
		q = q.Where("environment_id = ?", environmentID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list firing alerts: %w", err)
	}
	return alertsOf(rows)
}

// ListAlerts returns alerts matching f, newest first (ID order, UUIDv7),
// strictly before beforeID ("" from the newest), at most limit (0 = all).
func ListAlerts(ctx context.Context, db bun.IDB, f domain.AlertFilter, beforeID string, limit int) ([]domain.Alert, error) {
	var rows []alertRow
	q := db.NewSelect().Model(&rows).Order("id DESC")
	switch f.State {
	case domain.AlertListActive:
		q = q.Where("state = ?", string(domain.AlertFiring)).Where("dismissed_at IS NULL")
	case domain.AlertListDismissed:
		q = q.Where("state = ?", string(domain.AlertFiring)).Where("dismissed_at IS NOT NULL")
	case domain.AlertListFiring:
		q = q.Where("state = ?", string(domain.AlertFiring))
	case domain.AlertListResolved:
		q = q.Where("state = ?", string(domain.AlertResolved))
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", string(f.Kind))
	}
	if f.EnvironmentID != "" {
		q = q.Where("environment_id = ?", f.EnvironmentID)
	}
	if beforeID != "" {
		q = q.Where("id < ?", beforeID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list alerts: %w", err)
	}
	return alertsOf(rows)
}

// PurgeResolvedAlerts deletes alerts resolved before cutoff (their
// deliveries cascade) and returns how many.
func PurgeResolvedAlerts(ctx context.Context, db bun.IDB, cutoff time.Time) (int64, error) {
	res, err := db.NewDelete().Model((*alertRow)(nil)).Where("state = ?", string(domain.AlertResolved)).
		Where("resolved_at < ?", cutoff.UTC()).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: purge resolved alerts: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// --- deliveries ---

type alertDeliveryRow struct {
	bun.BaseModel `bun:"table:alert_deliveries"`

	ID             string     `bun:"id,pk"`
	AlertID        string     `bun:"alert_id,nullzero"`
	NotificationID string     `bun:"notification_id,nullzero"`
	ChannelID      string     `bun:"channel_id,notnull"`
	Event          string     `bun:"event,notnull"`
	Kind           string     `bun:"kind,notnull"`
	EnvironmentID  string     `bun:"environment_id,notnull"`
	Severity       string     `bun:"severity,notnull"`
	Outcome        string     `bun:"outcome,notnull"`
	Title          string     `bun:"title,notnull"`
	Body           string     `bun:"body,notnull"`
	Fields         string     `bun:"fields,notnull"`
	Link           string     `bun:"link,notnull"`
	State          string     `bun:"state,notnull"`
	Attempts       int        `bun:"attempts,notnull"`
	NextAttemptAt  time.Time  `bun:"next_attempt_at,notnull"`
	LastError      string     `bun:"last_error,notnull"`
	CreatedAt      time.Time  `bun:"created_at,notnull"`
	UpdatedAt      time.Time  `bun:"updated_at,notnull"`
	SentAt         *time.Time `bun:"sent_at"`
}

func fromAlertDelivery(d *domain.AlertDelivery) alertDeliveryRow {
	fields := d.Fields
	if fields == nil {
		fields = []domain.NotificationField{}
	}
	// Fields are plain strings: encoding them cannot fail.
	fb, _ := json.Marshal(fields)
	return alertDeliveryRow{
		ID: d.ID, AlertID: d.AlertID, NotificationID: d.NotificationID, ChannelID: d.ChannelID, Event: d.Event, Kind: string(d.Kind),
		EnvironmentID: d.EnvironmentID, Severity: string(d.Severity), Outcome: string(d.Outcome), Title: d.Title, Body: d.Body,
		Fields: string(fb), Link: d.Link, State: d.State, Attempts: d.Attempts,
		NextAttemptAt: d.NextAttemptAt.UTC(), LastError: d.LastError, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
		SentAt: utcPtr(d.SentAt),
	}
}

func (r alertDeliveryRow) toDomain() domain.AlertDelivery {
	var fields []domain.NotificationField
	// A row the database accepted holds a JSON array (CHECK); a field
	// that cannot be read is left out rather than failing the send.
	_ = json.Unmarshal([]byte(r.Fields), &fields)
	return domain.AlertDelivery{
		ID: r.ID, AlertID: r.AlertID, NotificationID: r.NotificationID, ChannelID: r.ChannelID, Event: r.Event,
		Kind: domain.NotificationEventKind(r.Kind), EnvironmentID: r.EnvironmentID, Severity: domain.AlertSeverity(r.Severity),
		Outcome: domain.NotificationOutcome(r.Outcome), Title: r.Title, Body: r.Body, Fields: fields, Link: r.Link,
		State: r.State, Attempts: r.Attempts,
		NextAttemptAt: r.NextAttemptAt.UTC(), LastError: r.LastError, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
		SentAt: utcPtr(r.SentAt),
	}
}

// InsertAlertDeliveries adds outbox rows (in the transaction that raised
// or resolved their alert).
func InsertAlertDeliveries(ctx context.Context, db bun.IDB, ds []domain.AlertDelivery) error {
	if len(ds) == 0 {
		return nil
	}
	rows := make([]alertDeliveryRow, 0, len(ds))
	for i := range ds {
		rows = append(rows, fromAlertDelivery(&ds[i]))
	}
	if _, err := db.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return fmt.Errorf("store: insert alert deliveries: %w", err)
	}
	return nil
}

// PendingAlertDeliveries returns every pending delivery in creation order
// (channel by channel: the dispatcher keeps each channel's order).
func PendingAlertDeliveries(ctx context.Context, db bun.IDB) ([]domain.AlertDelivery, error) {
	var rows []alertDeliveryRow
	if err := db.NewSelect().Model(&rows).Where("state = ?", domain.DeliveryPending).Order("channel_id ASC", "created_at ASC", "id ASC").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list pending alert deliveries: %w", err)
	}
	out := make([]domain.AlertDelivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

func deliveriesOf(rows []alertDeliveryRow) []domain.AlertDelivery {
	out := make([]domain.AlertDelivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out
}

// DueAlertChannels returns the channels with a pending delivery due at
// now (the index alert_deliveries_due). A channel's pending deliveries are
// never due before its oldest one, so its oldest is due too.
func DueAlertChannels(ctx context.Context, db bun.IDB, now time.Time) ([]string, error) {
	var ids []string
	if err := db.NewSelect().Model((*alertDeliveryRow)(nil)).Distinct().Column("channel_id").
		Where("state = ?", domain.DeliveryPending).Where("next_attempt_at <= ?", now.UTC()).Scan(ctx, &ids); err != nil {
		return nil, fmt.Errorf("store: list due alert channels: %w", err)
	}
	return ids, nil
}

// NextAlertDelivery returns when the next pending delivery is due after
// now (found false when none is waiting).
func NextAlertDelivery(ctx context.Context, db bun.IDB, now time.Time) (time.Time, bool, error) {
	var row alertDeliveryRow
	err := db.NewSelect().Model(&row).Column("next_attempt_at").Where("state = ?", domain.DeliveryPending).
		Where("next_attempt_at > ?", now.UTC()).Order("next_attempt_at ASC").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: next alert delivery: %w", err)
	}
	return row.NextAttemptAt.UTC(), true, nil
}

// ChannelAlertDeliveries returns a channel's oldest pending deliveries,
// at most limit, in creation order.
func ChannelAlertDeliveries(ctx context.Context, db bun.IDB, channelID string, limit int) ([]domain.AlertDelivery, error) {
	var rows []alertDeliveryRow
	if err := db.NewSelect().Model(&rows).Where("channel_id = ?", channelID).Where("state = ?", domain.DeliveryPending).
		Order("created_at ASC", "id ASC").Limit(limit).Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list a channel's pending alert deliveries: %w", err)
	}
	return deliveriesOf(rows), nil
}

// LatestAlertAttempts returns the latest next attempt of the pending
// deliveries of each of these channels (channels without any are absent)
// in one query: a new delivery is not due before it, which keeps each
// channel's order.
func LatestAlertAttempts(ctx context.Context, db bun.IDB, channelIDs []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(channelIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ChannelID string    `bun:"channel_id"`
		Latest    time.Time `bun:"latest"`
	}
	if err := db.NewSelect().Model((*alertDeliveryRow)(nil)).ColumnExpr("channel_id, MAX(next_attempt_at) AS latest").
		Where("state = ?", domain.DeliveryPending).Where("channel_id IN (?)", bun.List(channelIDs)).
		Group("channel_id").Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("store: latest alert attempts: %w", err)
	}
	for _, r := range rows {
		out[r.ChannelID] = r.Latest.UTC()
	}
	return out, nil
}

// DeferChannelAlertDeliveries moves every pending delivery of a channel
// due before until to until (a failed send: the whole channel waits, in
// order).
func DeferChannelAlertDeliveries(ctx context.Context, db bun.IDB, channelID string, until, now time.Time) error {
	if _, err := db.NewUpdate().Model((*alertDeliveryRow)(nil)).Set("next_attempt_at = ?", until.UTC()).Set("updated_at = ?", now.UTC()).
		Where("channel_id = ?", channelID).Where("state = ?", domain.DeliveryPending).Where("next_attempt_at < ?", until.UTC()).
		Exec(ctx); err != nil {
		return fmt.Errorf("store: defer alert deliveries: %w", err)
	}
	return nil
}

// AlertDeliveries returns the deliveries of an alert in creation order.
func AlertDeliveries(ctx context.Context, db bun.IDB, alertID string) ([]domain.AlertDelivery, error) {
	var rows []alertDeliveryRow
	if err := db.NewSelect().Model(&rows).Where("alert_id = ?", alertID).Order("created_at ASC", "id ASC").Scan(ctx); err != nil {
		return nil, fmt.Errorf("store: list alert deliveries: %w", err)
	}
	out := make([]domain.AlertDelivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toDomain())
	}
	return out, nil
}

// SetAlertDeliveries writes the outcome of pending deliveries: state,
// attempts, next attempt, last error class and send time. Rows no longer
// pending are left alone.
func SetAlertDeliveries(ctx context.Context, db bun.IDB, ds []domain.AlertDelivery) error {
	for i := range ds {
		row := fromAlertDelivery(&ds[i])
		if _, err := db.NewUpdate().Model(&row).Column("state", "attempts", "next_attempt_at", "last_error", "updated_at", "sent_at").
			WherePK().Where("state = ?", domain.DeliveryPending).Exec(ctx); err != nil {
			return fmt.Errorf("store: update alert delivery: %w", err)
		}
	}
	return nil
}

// PurgeAlertDeliveries deletes finished deliveries (sent, failed,
// dropped) last changed before cutoff and returns how many. Those of a
// firing alert stay: they record which channels were told about it (they
// get its resolution).
func PurgeAlertDeliveries(ctx context.Context, db bun.IDB, cutoff time.Time) (int64, error) {
	res, err := db.NewDelete().Model((*alertDeliveryRow)(nil)).Where("state <> ?", domain.DeliveryPending).
		Where("updated_at < ?", cutoff.UTC()).
		Where("alert_id IS NULL OR alert_id NOT IN (SELECT id FROM alerts WHERE state = ?)", string(domain.AlertFiring)).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: purge alert deliveries: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
