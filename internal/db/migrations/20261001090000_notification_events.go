package migrations

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/uptrace/bun"
)

// alertsTable is the alerts table of this migration (name, allowed kinds).
func alertsTable(name, kinds string) string {
	return `CREATE TABLE ` + name + ` (
		id                TEXT    NOT NULL PRIMARY KEY,
		dedupe_key        TEXT    NOT NULL CHECK (length(dedupe_key) BETWEEN 1 AND 512),
		kind              TEXT    NOT NULL CHECK (kind IN (` + kinds + `)),
		severity          TEXT    NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
		state             TEXT    NOT NULL CHECK (state IN ('firing', 'resolved')),
		environment_id    TEXT    NOT NULL DEFAULT '',
		resource_type     TEXT    NOT NULL DEFAULT '',
		resource_id       TEXT    NOT NULL DEFAULT '',
		job_kind          TEXT    NOT NULL DEFAULT '',
		targets           TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(targets) AND json_type(targets) = 'array'),
		title             TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
		facts             TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(facts) AND json_type(facts) = 'object'),
		fingerprint       TEXT    NOT NULL DEFAULT '',
		escalation        INTEGER NOT NULL DEFAULT 0 CHECK (escalation >= 0),
		started_at        TEXT    NOT NULL,
		updated_at        TEXT    NOT NULL,
		last_seen_at      TEXT    NOT NULL,
		resolved_at       TEXT,
		resolution        TEXT    NOT NULL DEFAULT '' CHECK (resolution IN ('', 'resolved', 'removed', 'expired', 'archived')),
		dismissed_at      TEXT,
		dismissed_by      TEXT    NOT NULL DEFAULT '',
		dismissed_by_name TEXT    NOT NULL DEFAULT '',
		revision          INTEGER NOT NULL CHECK (revision >= 1),
		CHECK ((state = 'firing') = (resolved_at IS NULL))
	) STRICT`
}

const alertColumns = `id, dedupe_key, kind, severity, state, environment_id, resource_type, resource_id, job_kind, targets,
	title, facts, fingerprint, escalation, started_at, updated_at, last_seen_at, resolved_at, resolution, dismissed_at,
	dismissed_by, dismissed_by_name, revision`

var alertIndexes = []string{
	`CREATE UNIQUE INDEX alerts_firing_key ON alerts (dedupe_key) WHERE state = 'firing'`,
	`CREATE INDEX alerts_state ON alerts (state, environment_id)`,
	`CREATE INDEX alerts_resolved_at ON alerts (resolved_at) WHERE resolved_at IS NOT NULL`,
}

var deliveryIndexes = []string{
	`CREATE INDEX alert_deliveries_pending ON alert_deliveries (channel_id, created_at) WHERE state = 'pending'`,
	`CREATE INDEX alert_deliveries_due ON alert_deliveries (next_attempt_at) WHERE state = 'pending'`,
	`CREATE INDEX alert_deliveries_channel_due ON alert_deliveries (channel_id, next_attempt_at) WHERE state = 'pending'`,
	`CREATE INDEX alert_deliveries_alert ON alert_deliveries (alert_id)`,
	`CREATE INDEX alert_deliveries_updated ON alert_deliveries (updated_at) WHERE state <> 'pending'`,
}

// oldSubscriptions turns a channel's event kinds and resolved flag into
// outcomes per kind. A channel that had every kind (the old default) also
// gets the new kinds.
func oldSubscriptions(kindsJSON string, sendResolved bool) (string, error) {
	var kinds []string
	if err := json.Unmarshal([]byte(kindsJSON), &kinds); err != nil {
		return "", err
	}
	resolved := func(os ...string) []string {
		if sendResolved {
			return append(os, "resolved")
		}
		return os
	}
	out := map[string][]string{}
	all := true
	for _, k := range []string{"disk_health", "raid", "environment_offline", "job_failed", "updates_available"} {
		if !slices.Contains(kinds, k) {
			all = false
			continue
		}
		switch k {
		case "disk_health", "raid":
			out[k] = resolved("warning", "critical")
		case "environment_offline":
			out[k] = resolved("critical")
		case "job_failed":
			out[k] = resolved("failure", "warning")
		case "updates_available":
			out["updates"] = []string{"available"}
		}
	}
	if all {
		for _, k := range []string{"temperature", "disk_space", "memory"} {
			out[k] = resolved("warning", "critical")
		}
		out["backup"] = []string{"failure", "warning", "success"}
		out["prune"] = []string{"failure", "success"}
		out["updates"] = []string{"available", "failure", "success"}
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// convertSubscriptions fills notification_channels.subscriptions from
// event_kinds and send_resolved.
func convertSubscriptions(ctx context.Context, tx bun.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, event_kinds, send_resolved FROM notification_channels`)
	if err != nil {
		return err
	}
	type channel struct {
		id, kinds string
		resolved  int
	}
	var cs []channel
	for rows.Next() {
		var c channel
		if err := rows.Scan(&c.id, &c.kinds, &c.resolved); err != nil {
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
		subs, err := oldSubscriptions(c.kinds, c.resolved == 1)
		if err != nil {
			return fmt.Errorf("convert the event kinds of notification channel %s: %w", c.id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notification_channels SET subscriptions = ? WHERE id = ?`, subs, c.id); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	// Notifications, host threshold alerts and outcome subscriptions:
	//
	//   - alerts: new kinds temperature, disk_space and memory;
	//     updates_available is now updates (its alerts say updates are
	//     available, its notifications that a run applied them). SQLite
	//     cannot change a CHECK in place, so alerts and alert_deliveries
	//     are rebuilt (deliveries reference alerts).
	//   - notifications: finished runs (backups and restores, prunes,
	//     update runs) with their outcome (success, warning, failure), a
	//     title and facts (JSON object of small non-secret values), the job
	//     (who may see it) and the time; kept 90 days.
	//   - alert_deliveries: a message is of an alert or of a notification
	//     (exactly one of alert_id, notification_id); event
	//     'notification'; outcome (what a channel subscribes to) and
	//     fields (JSON array of {name, value, inline}) are part of the
	//     snapshot.
	//   - notification_channels: subscriptions (JSON object: event kind to
	//     outcomes) replaces event_kinds and send_resolved.
	//   - alert_settings (one row) and alert_threshold_overrides (per
	//     environment, NULL keeps the default): warning and critical
	//     levels of temperature (°C), disk space and memory (percent used);
	//     0 is off.
	const newKinds = `'disk_health', 'raid', 'temperature', 'disk_space', 'memory', 'environment_offline', 'updates', 'job_failed'`
	const oldKinds = `'disk_health', 'raid', 'environment_offline', 'job_failed', 'updates_available'`
	Migrations.MustRegister(
		Tx(func(ctx context.Context, tx bun.Tx) error {
			if err := Exec(
				alertsTable("alerts_new", newKinds),
				`INSERT INTO alerts_new (`+alertColumns+`)
				 SELECT id, dedupe_key, CASE kind WHEN 'updates_available' THEN 'updates' ELSE kind END, severity, state,
					environment_id, resource_type, resource_id, job_kind, targets, title, facts, fingerprint, escalation,
					started_at, updated_at, last_seen_at, resolved_at, resolution, dismissed_at, dismissed_by,
					dismissed_by_name, revision FROM alerts`,
				`CREATE TABLE notifications (
					id             TEXT NOT NULL PRIMARY KEY,
					kind           TEXT NOT NULL CHECK (kind IN ('backup', 'prune', 'updates')),
					outcome        TEXT NOT NULL CHECK (outcome IN ('success', 'warning', 'failure')),
					environment_id TEXT NOT NULL DEFAULT '',
					job_id         TEXT NOT NULL DEFAULT '',
					job_kind       TEXT NOT NULL DEFAULT '',
					targets        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(targets) AND json_type(targets) = 'array'),
					origin         TEXT NOT NULL DEFAULT '',
					title          TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
					facts          TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(facts) AND json_type(facts) = 'object'),
					created_at     TEXT NOT NULL
				) STRICT`,
				`CREATE INDEX notifications_environment ON notifications (environment_id, id)`,
				`CREATE INDEX notifications_created_at ON notifications (created_at)`,
				`CREATE TABLE alert_deliveries_new (
					id              TEXT    NOT NULL PRIMARY KEY,
					alert_id        TEXT    REFERENCES alerts_new (id) ON DELETE CASCADE,
					notification_id TEXT    REFERENCES notifications (id) ON DELETE CASCADE,
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
				) STRICT`,
				`INSERT INTO alert_deliveries_new (id, alert_id, channel_id, event, kind, environment_id, severity, outcome,
					title, body, link, state, attempts, next_attempt_at, last_error, created_at, updated_at, sent_at)
				 SELECT id, alert_id, channel_id, event, CASE kind WHEN 'updates_available' THEN 'updates' ELSE kind END,
					environment_id, severity,
					CASE
						WHEN event = 'resolved' THEN 'resolved'
						WHEN kind = 'updates_available' THEN 'available'
						WHEN kind = 'job_failed' AND severity = 'critical' THEN 'failure'
						WHEN severity = 'critical' THEN 'critical'
						ELSE 'warning'
					END,
					title, body, link, state, attempts, next_attempt_at, last_error, created_at, updated_at, sent_at
				 FROM alert_deliveries`,
				`DROP TABLE alert_deliveries`,
				`DROP TABLE alerts`,
				// Renaming alerts_new also renames the reference of
				// alert_deliveries_new.
				`ALTER TABLE alerts_new RENAME TO alerts`,
				`ALTER TABLE alert_deliveries_new RENAME TO alert_deliveries`,
				alertIndexes[0], alertIndexes[1], alertIndexes[2],
				deliveryIndexes[0], deliveryIndexes[1], deliveryIndexes[2], deliveryIndexes[3], deliveryIndexes[4],
				`CREATE INDEX alert_deliveries_notification ON alert_deliveries (notification_id) WHERE notification_id IS NOT NULL`,
				`ALTER TABLE notification_channels ADD COLUMN subscriptions TEXT NOT NULL DEFAULT '{}'
					CHECK (json_valid(subscriptions) AND json_type(subscriptions) = 'object')`,
			)(ctx, tx); err != nil {
				return err
			}
			if err := convertSubscriptions(ctx, tx); err != nil {
				return err
			}
			return Exec(
				`ALTER TABLE notification_channels DROP COLUMN event_kinds`,
				`ALTER TABLE notification_channels DROP COLUMN send_resolved`,
				`CREATE TABLE alert_settings (
					singleton            INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
					temperature_warning  INTEGER NOT NULL CHECK (temperature_warning BETWEEN 0 AND 150),
					temperature_critical INTEGER NOT NULL CHECK (temperature_critical BETWEEN 0 AND 150),
					disk_space_warning   INTEGER NOT NULL CHECK (disk_space_warning BETWEEN 0 AND 100),
					disk_space_critical  INTEGER NOT NULL CHECK (disk_space_critical BETWEEN 0 AND 100),
					memory_warning       INTEGER NOT NULL CHECK (memory_warning BETWEEN 0 AND 100),
					memory_critical      INTEGER NOT NULL CHECK (memory_critical BETWEEN 0 AND 100),
					revision             INTEGER NOT NULL CHECK (revision >= 1),
					updated_at           TEXT    NOT NULL
				) STRICT`,
				`INSERT INTO alert_settings (singleton, temperature_warning, temperature_critical, disk_space_warning,
					disk_space_critical, memory_warning, memory_critical, revision, updated_at)
				 VALUES (1, 80, 90, 85, 95, 90, 95, 1, strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))`,
				`CREATE TABLE alert_threshold_overrides (
					environment_id       TEXT    NOT NULL PRIMARY KEY REFERENCES environments (id) ON DELETE CASCADE,
					temperature_warning  INTEGER CHECK (temperature_warning BETWEEN 0 AND 150),
					temperature_critical INTEGER CHECK (temperature_critical BETWEEN 0 AND 150),
					disk_space_warning   INTEGER CHECK (disk_space_warning BETWEEN 0 AND 100),
					disk_space_critical  INTEGER CHECK (disk_space_critical BETWEEN 0 AND 100),
					memory_warning       INTEGER CHECK (memory_warning BETWEEN 0 AND 100),
					memory_critical      INTEGER CHECK (memory_critical BETWEEN 0 AND 100)
				) STRICT`,
			)(ctx, tx)
		}),
		Tx(Exec(
			`DROP TABLE alert_threshold_overrides`,
			`DROP TABLE alert_settings`,
			// Subscriptions go back to every kind with resolved messages
			// (the old default): the outcomes per kind cannot be kept.
			`ALTER TABLE notification_channels ADD COLUMN event_kinds TEXT NOT NULL
				DEFAULT '["disk_health","raid","environment_offline","job_failed","updates_available"]'
				CHECK (json_valid(event_kinds) AND json_type(event_kinds) = 'array' AND json_array_length(event_kinds) >= 1)`,
			`ALTER TABLE notification_channels ADD COLUMN send_resolved INTEGER NOT NULL DEFAULT 1 CHECK (send_resolved IN (0, 1))`,
			`ALTER TABLE notification_channels DROP COLUMN subscriptions`,
			alertsTable("alerts_old", oldKinds),
			`INSERT INTO alerts_old (`+alertColumns+`)
			 SELECT id, dedupe_key, CASE kind WHEN 'updates' THEN 'updates_available' ELSE kind END, severity, state,
				environment_id, resource_type, resource_id, job_kind, targets, title, facts, fingerprint, escalation,
				started_at, updated_at, last_seen_at, resolved_at, resolution, dismissed_at, dismissed_by,
				dismissed_by_name, revision FROM alerts WHERE kind NOT IN ('temperature', 'disk_space', 'memory')`,
			`CREATE TABLE alert_deliveries_old (
				id              TEXT    NOT NULL PRIMARY KEY,
				alert_id        TEXT    NOT NULL REFERENCES alerts_old (id) ON DELETE CASCADE,
				channel_id      TEXT    NOT NULL,
				event           TEXT    NOT NULL CHECK (event IN ('firing', 'worse', 'resolved')),
				kind            TEXT    NOT NULL,
				environment_id  TEXT    NOT NULL DEFAULT '',
				severity        TEXT    NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
				title           TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
				body            TEXT    NOT NULL DEFAULT '',
				link            TEXT    NOT NULL DEFAULT '',
				state           TEXT    NOT NULL CHECK (state IN ('pending', 'sent', 'failed', 'dropped')),
				attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
				next_attempt_at TEXT    NOT NULL,
				last_error      TEXT    NOT NULL DEFAULT '' CHECK (length(last_error) <= 32),
				created_at      TEXT    NOT NULL,
				updated_at      TEXT    NOT NULL,
				sent_at         TEXT
			) STRICT`,
			`INSERT INTO alert_deliveries_old (id, alert_id, channel_id, event, kind, environment_id, severity, title, body,
				link, state, attempts, next_attempt_at, last_error, created_at, updated_at, sent_at)
			 SELECT d.id, d.alert_id, d.channel_id, d.event, CASE d.kind WHEN 'updates' THEN 'updates_available' ELSE d.kind END,
				d.environment_id, d.severity, d.title, d.body, d.link, d.state, d.attempts, d.next_attempt_at, d.last_error,
				d.created_at, d.updated_at, d.sent_at
			 FROM alert_deliveries d JOIN alerts_old a ON a.id = d.alert_id`,
			`DROP TABLE alert_deliveries`,
			`DROP TABLE notifications`,
			`DROP TABLE alerts`,
			`ALTER TABLE alerts_old RENAME TO alerts`,
			`ALTER TABLE alert_deliveries_old RENAME TO alert_deliveries`,
			alertIndexes[0], alertIndexes[1], alertIndexes[2],
			deliveryIndexes[0], deliveryIndexes[1], deliveryIndexes[2], deliveryIndexes[3], deliveryIndexes[4],
		)),
	)
}
