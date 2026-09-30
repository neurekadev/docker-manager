package migrations

func init() {
	// Notification channels (#142): outgoing destinations (Shoutrrr URLs).
	//
	//   - secret_sealed is the Shoutrrr URL sealed with the
	//     secret-protection key (context "notification_channels/<id>/url");
	//     secret_fingerprint is a keyed fingerprint (never the address);
	//   - service is the URL's Shoutrrr service, target a non-secret hint
	//     (a mail or push server's host) or '';
	//   - event_kinds is a JSON array of subscribed event kinds (never
	//     empty); send_resolved also sends resolved problems;
	//   - last_result is '' (never sent), 'ok' or a send error class;
	//   - notification_channel_environments limits a channel to some
	//     environments; no rows means every environment, future ones too.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE notification_channels (
				id                  TEXT    NOT NULL PRIMARY KEY,
				name                TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key            TEXT    NOT NULL UNIQUE,
				service             TEXT    NOT NULL CHECK (length(service) BETWEEN 1 AND 32),
				target              TEXT    NOT NULL DEFAULT '' CHECK (length(target) <= 255),
				enabled             INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
				event_kinds         TEXT    NOT NULL CHECK (json_valid(event_kinds) AND json_type(event_kinds) = 'array'
				                                            AND json_array_length(event_kinds) >= 1),
				send_resolved       INTEGER NOT NULL DEFAULT 1 CHECK (send_resolved IN (0, 1)),
				secret_sealed       TEXT    NOT NULL CHECK (secret_sealed <> ''),
				secret_fingerprint  TEXT    NOT NULL,
				secret_version      INTEGER NOT NULL CHECK (secret_version >= 1),
				secret_updated_at   TEXT    NOT NULL,
				last_result         TEXT    NOT NULL DEFAULT '' CHECK (length(last_result) <= 32),
				last_attempt_at     TEXT,
				last_success_at     TEXT,
				revision            INTEGER NOT NULL CHECK (revision >= 1),
				created_at          TEXT    NOT NULL,
				updated_at          TEXT    NOT NULL
			) STRICT`,
			`CREATE TABLE notification_channel_environments (
				channel_id     TEXT NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
				environment_id TEXT NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
				PRIMARY KEY (channel_id, environment_id)
			) STRICT`,
			`CREATE INDEX notification_channel_environments_environment ON notification_channel_environments (environment_id)`,
		)),
		Tx(Exec(
			`DROP TABLE notification_channel_environments`,
			`DROP TABLE notification_channels`,
		)),
	)
}
