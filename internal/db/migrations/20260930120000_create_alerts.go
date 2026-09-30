package migrations

func init() {
	// Alerts (#159): problems the manager raises by itself and the outbox
	// of their messages to notification channels.
	//
	//   - alerts: one row per problem; dedupe_key identifies it and is
	//     unique among firing rows (alerts_firing_key); resolved rows stay
	//     90 days. targets is a JSON array of job targets (who may see a
	//     job or update alert), facts a JSON object of small non-secret
	//     values, fingerprint the sorted problem tokens, escalation how often
	//     it got worse (a browser-local dismissal is keyed by it).
	//     dismissed_* record who dismissed it for everyone (cleared when it
	//     gets worse).
	//   - alert_deliveries: one message of an alert to one channel,
	//     written in the transaction that raised or resolved the alert,
	//     with a snapshot of what the message says (kind, environment,
	//     severity, title, body, link) at that moment; pending until sent,
	//     given up (failed) or dropped (the channel is gone, off or no
	//     longer subscribed); finished rows stay 7 days. A channel's pending
	//     rows are never due before its oldest one (next_attempt_at does
	//     not decrease in creation order), so alert_deliveries_due finds
	//     the due channels.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE alerts (
				id                TEXT    NOT NULL PRIMARY KEY,
				dedupe_key        TEXT    NOT NULL CHECK (length(dedupe_key) BETWEEN 1 AND 512),
				kind              TEXT    NOT NULL CHECK (kind IN ('disk_health', 'raid', 'environment_offline', 'job_failed', 'updates_available')),
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
			) STRICT`,
			`CREATE UNIQUE INDEX alerts_firing_key ON alerts (dedupe_key) WHERE state = 'firing'`,
			`CREATE INDEX alerts_state ON alerts (state, environment_id)`,
			`CREATE INDEX alerts_resolved_at ON alerts (resolved_at) WHERE resolved_at IS NOT NULL`,
			`CREATE TABLE alert_deliveries (
				id              TEXT    NOT NULL PRIMARY KEY,
				alert_id        TEXT    NOT NULL REFERENCES alerts (id) ON DELETE CASCADE,
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
			`CREATE INDEX alert_deliveries_pending ON alert_deliveries (channel_id, created_at) WHERE state = 'pending'`,
			`CREATE INDEX alert_deliveries_due ON alert_deliveries (next_attempt_at) WHERE state = 'pending'`,
			// A channel's latest pending due time (new messages keep its order).
			`CREATE INDEX alert_deliveries_channel_due ON alert_deliveries (channel_id, next_attempt_at) WHERE state = 'pending'`,
			`CREATE INDEX alert_deliveries_alert ON alert_deliveries (alert_id)`,
			`CREATE INDEX alert_deliveries_updated ON alert_deliveries (updated_at) WHERE state <> 'pending'`,
		)),
		Tx(Exec(
			`DROP TABLE alert_deliveries`,
			`DROP TABLE alerts`,
		)),
	)
}
