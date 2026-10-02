package migrations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/uptrace/bun"
)

// notificationsTable is the notifications table with its allowed kinds.
func notificationsTable(name, kinds string) string {
	return `CREATE TABLE ` + name + ` (
		id             TEXT NOT NULL PRIMARY KEY,
		kind           TEXT NOT NULL CHECK (kind IN (` + kinds + `)),
		outcome        TEXT NOT NULL CHECK (outcome IN ('success', 'warning', 'failure')),
		environment_id TEXT NOT NULL DEFAULT '',
		job_id         TEXT NOT NULL DEFAULT '',
		job_kind       TEXT NOT NULL DEFAULT '',
		targets        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(targets) AND json_type(targets) = 'array'),
		origin         TEXT NOT NULL DEFAULT '',
		title          TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
		facts          TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(facts) AND json_type(facts) = 'object'),
		created_at     TEXT NOT NULL
	) STRICT`
}

// deliveriesTable is alert_deliveries referencing alerts and the
// notifications table named notifications.
func deliveriesTable(name, notifications string) string {
	return `CREATE TABLE ` + name + ` (
		id              TEXT    NOT NULL PRIMARY KEY,
		alert_id        TEXT    REFERENCES alerts (id) ON DELETE CASCADE,
		notification_id TEXT    REFERENCES ` + notifications + ` (id) ON DELETE CASCADE,
		channel_id      TEXT    NOT NULL,
		event           TEXT    NOT NULL CHECK (event IN ('firing', 'worse', 'resolved', 'notification')),
		kind            TEXT    NOT NULL,
		environment_id  TEXT    NOT NULL DEFAULT '',
		severity        TEXT    NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
		outcome         TEXT    NOT NULL DEFAULT '',
		title           TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
		body            TEXT    NOT NULL DEFAULT '',
		fields          TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(fields) AND json_type(fields) = 'array'),
		link            TEXT    NOT NULL DEFAULT '',
		state           TEXT    NOT NULL CHECK (state IN ('pending', 'sent', 'failed', 'dropped')),
		attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
		next_attempt_at TEXT    NOT NULL,
		last_error      TEXT    NOT NULL DEFAULT '' CHECK (length(last_error) <= 32),
		created_at      TEXT    NOT NULL,
		updated_at      TEXT    NOT NULL,
		sent_at         TEXT,
		CHECK ((alert_id IS NULL) <> (notification_id IS NULL))
	) STRICT`
}

const (
	notificationColumns = `id, kind, outcome, environment_id, job_id, job_kind, targets, origin, title, facts, created_at`
	deliveryColumns     = `id, alert_id, notification_id, channel_id, event, kind, environment_id, severity, outcome, title, body,
		fields, link, state, attempts, next_attempt_at, last_error, created_at, updated_at, sent_at`
)

// rebuildNotifications replaces notifications (allowing kinds) and
// alert_deliveries, which references it: SQLite cannot change a CHECK in
// place, and dropping notifications alone would delete its deliveries
// (ON DELETE CASCADE). kindOf and deliveryKindOf are the SQL expressions
// of the new kind of a notification row and of a delivery row (d).
func rebuildNotifications(kinds, kindOf, deliveryKindOf string) func(ctx context.Context, tx bun.Tx) error {
	return Exec(
		notificationsTable("notifications_new", kinds),
		`INSERT INTO notifications_new (`+notificationColumns+`)
		 SELECT id, `+kindOf+`, outcome, environment_id, job_id, job_kind, targets, origin, title, facts, created_at
		 FROM notifications`,
		deliveriesTable("alert_deliveries_new", "notifications_new"),
		`INSERT INTO alert_deliveries_new (`+deliveryColumns+`)
		 SELECT d.id, d.alert_id, d.notification_id, d.channel_id, d.event, `+deliveryKindOf+`, d.environment_id, d.severity,
			d.outcome, d.title, d.body, d.fields, d.link, d.state, d.attempts, d.next_attempt_at, d.last_error, d.created_at,
			d.updated_at, d.sent_at
		 FROM alert_deliveries d LEFT JOIN notifications n ON n.id = d.notification_id`,
		`DROP TABLE alert_deliveries`,
		`DROP TABLE notifications`,
		// Renaming notifications_new also renames the reference of
		// alert_deliveries_new.
		`ALTER TABLE notifications_new RENAME TO notifications`,
		`ALTER TABLE alert_deliveries_new RENAME TO alert_deliveries`,
		`CREATE INDEX notifications_environment ON notifications (environment_id, id)`,
		`CREATE INDEX notifications_created_at ON notifications (created_at)`,
		deliveryIndexes[0], deliveryIndexes[1], deliveryIndexes[2], deliveryIndexes[3], deliveryIndexes[4],
		`CREATE INDEX alert_deliveries_notification ON alert_deliveries (notification_id) WHERE notification_id IS NOT NULL`,
	)
}

// kindOutcomes are the outcomes of the kinds this migration adds to, in
// their display order.
var kindOutcomes = map[string][]string{
	"backup":  {"failure", "warning", "success"},
	"restore": {"failure", "success"},
	"updates": {"available", "failure", "success"},
}

// addOutcomes adds outcomes to kind k of subs (kept in k's order, without
// duplicates) and reports whether anything was added.
func addOutcomes(subs map[string][]string, k string, add ...string) bool {
	changed := false
	for _, o := range add {
		if !slices.Contains(subs[k], o) {
			subs[k] = append(subs[k], o)
			changed = true
		}
	}
	if changed {
		var ordered []string
		for _, o := range kindOutcomes[k] {
			if slices.Contains(subs[k], o) {
				ordered = append(ordered, o)
			}
		}
		subs[k] = ordered
	}
	return changed
}

// splitSubscriptions keeps what a channel was sent before restores and
// failed jobs got kinds by area:
//
//   - a channel subscribed to backups gets the same outcomes of restores
//     (those restores have: failure, success), from its backup outcomes
//     before the next step; restores were backups until now;
//   - a failed scheduled or API token job of an area (a backup
//     verification, retention or import; an update check) is now sent as
//     backups or image updates, no longer as job_failed: a channel's
//     job_failed failure and warning carry over to backups (failure,
//     warning) and to image updates (either one: failure, as updates have
//     no warning). Its other outcomes stay, job_failed stays, and
//     resolved maps to no success (that would send every successful run).
func splitSubscriptions(subsJSON string) (string, bool, error) {
	var subs map[string][]string
	if err := json.Unmarshal([]byte(subsJSON), &subs); err != nil {
		return "", false, err
	}
	changed := false
	var restores []string
	for _, o := range kindOutcomes["restore"] {
		if slices.Contains(subs["backup"], o) {
			restores = append(restores, o)
		}
	}
	if len(restores) > 0 && addOutcomes(subs, "restore", restores...) {
		changed = true
	}
	jobs := subs["job_failed"]
	var backups []string
	for _, o := range []string{"failure", "warning"} {
		if slices.Contains(jobs, o) {
			backups = append(backups, o)
		}
	}
	if len(backups) > 0 && addOutcomes(subs, "backup", backups...) {
		changed = true
	}
	if len(backups) > 0 && addOutcomes(subs, "updates", "failure") {
		changed = true
	}
	if !changed {
		return subsJSON, false, nil
	}
	b, err := json.Marshal(subs)
	return string(b), true, err
}

// withoutRestores drops a channel's restores (down: they are backups
// again, which the channel's backup outcomes cover). The outcomes carried
// over from job_failed stay: they can't be told from ones the owner
// chose, and they only send what failed jobs of those areas sent before.
func withoutRestores(subsJSON string) (string, bool, error) {
	var subs map[string][]string
	if err := json.Unmarshal([]byte(subsJSON), &subs); err != nil {
		return "", false, err
	}
	if _, ok := subs["restore"]; !ok {
		return subsJSON, false, nil
	}
	delete(subs, "restore")
	b, err := json.Marshal(subs)
	return string(b), true, err
}

// rewriteSubscriptions applies fn to every channel's subscriptions.
func rewriteSubscriptions(ctx context.Context, tx bun.Tx, fn func(string) (string, bool, error)) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, subscriptions FROM notification_channels`)
	if err != nil {
		return err
	}
	type channel struct{ id, subs string }
	var cs []channel
	for rows.Next() {
		var c channel
		if err := rows.Scan(&c.id, &c.subs); err != nil {
			_ = rows.Close()
			return err
		}
		cs = append(cs, c)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cs {
		subs, changed, err := fn(c.subs)
		if err != nil {
			return fmt.Errorf("convert the subscriptions of notification channel %s: %w", c.id, err)
		}
		if !changed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notification_channels SET subscriptions = ? WHERE id = ?`, subs, c.id); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	// Restores are a notification kind of their own (#218), split from
	// backups: notifications allow kind 'restore' and recorded restores
	// (job kind restore.run) move to it, with their messages' kind; every
	// channel subscribed to backups also subscribes to restores, with its
	// backup outcomes restores have (failure, success). Failed jobs of an
	// area are sent as that area's kind, so a channel's job_failed failure
	// and warning carry over to backups and image updates
	// (splitSubscriptions).
	Migrations.MustRegister(
		Tx(func(ctx context.Context, tx bun.Tx) error {
			if err := rebuildNotifications(`'backup', 'restore', 'prune', 'updates'`,
				`CASE WHEN job_kind = 'restore.run' THEN 'restore' ELSE kind END`,
				`CASE WHEN n.job_kind = 'restore.run' THEN 'restore' ELSE d.kind END`)(ctx, tx); err != nil {
				return err
			}
			return rewriteSubscriptions(ctx, tx, splitSubscriptions)
		}),
		Tx(func(ctx context.Context, tx bun.Tx) error {
			if err := rebuildNotifications(`'backup', 'prune', 'updates'`,
				`CASE kind WHEN 'restore' THEN 'backup' ELSE kind END`,
				`CASE d.kind WHEN 'restore' THEN 'backup' ELSE d.kind END`)(ctx, tx); err != nil {
				return err
			}
			return rewriteSubscriptions(ctx, tx, withoutRestores)
		}),
	)
}
