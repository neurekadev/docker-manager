package migrations

const (
	// inAppChannelID is domain.InAppChannelID: it sorts before every
	// UUIDv7, so the In App channel is listed first.
	inAppChannelID = `'00000000-0000-0000-0000-000000000000'`
	// inAppSubscriptions is every outcome of every event kind (a new
	// channel's default, domain.AllNotificationSubscriptions).
	inAppSubscriptions = `'{"disk_health":["warning","critical","resolved"],"raid":["warning","critical","resolved"],` +
		`"temperature":["warning","critical","resolved"],"disk_space":["warning","critical","resolved"],` +
		`"memory":["warning","critical","resolved"],"environment_offline":["critical","resolved"],` +
		`"backup":["failure","warning","success"],"restore":["failure","success"],"prune":["failure","success"],` +
		`"updates":["available","failure","success"],"job_failed":["failure","warning","resolved"]}'`

	channelColumns = `id, name, name_key, service, target, enabled, subscriptions, all_environments, secret_sealed,
		secret_fingerprint, secret_version, secret_updated_at, last_result, last_attempt_at, last_success_at, revision,
		created_at, updated_at`

	// Every channel has an address; with the In App channel, every
	// channel but it.
	addressRequired      = `CHECK (secret_sealed <> '' AND secret_version >= 1)`
	addressUnlessBuiltIn = `CHECK ((secret_sealed <> '' AND secret_version >= 1) OR id = ` + inAppChannelID + `)`
)

// rebuildChannels replaces notification_channels with one whose address
// rule is check (SQLite cannot change a CHECK in place), and
// notification_channel_environments with it: it references the table, and
// dropping the table alone would delete its rows (ON DELETE CASCADE).
func rebuildChannels(check string) []string {
	return []string{
		`CREATE TABLE notification_channels_new (
			id                  TEXT    NOT NULL PRIMARY KEY,
			name                TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
			name_key            TEXT    NOT NULL UNIQUE,
			service             TEXT    NOT NULL CHECK (length(service) BETWEEN 1 AND 32),
			target              TEXT    NOT NULL DEFAULT '' CHECK (length(target) <= 255),
			enabled             INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
			subscriptions       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(subscriptions) AND json_type(subscriptions) = 'object'),
			all_environments    INTEGER NOT NULL DEFAULT 1 CHECK (all_environments IN (0, 1)),
			secret_sealed       TEXT    NOT NULL,
			secret_fingerprint  TEXT    NOT NULL,
			secret_version      INTEGER NOT NULL CHECK (secret_version >= 0),
			secret_updated_at   TEXT    NOT NULL,
			last_result         TEXT    NOT NULL DEFAULT '' CHECK (length(last_result) <= 32),
			last_attempt_at     TEXT,
			last_success_at     TEXT,
			revision            INTEGER NOT NULL CHECK (revision >= 1),
			created_at          TEXT    NOT NULL,
			updated_at          TEXT    NOT NULL,
			` + check + `
		) STRICT`,
		`INSERT INTO notification_channels_new (` + channelColumns + `) SELECT ` + channelColumns + ` FROM notification_channels`,
		`CREATE TABLE notification_channel_environments_new (
			channel_id     TEXT NOT NULL REFERENCES notification_channels_new (id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
			PRIMARY KEY (channel_id, environment_id)
		) STRICT`,
		`INSERT INTO notification_channel_environments_new (channel_id, environment_id)
		 SELECT channel_id, environment_id FROM notification_channel_environments`,
		`DROP TABLE notification_channel_environments`,
		`DROP TABLE notification_channels`,
		// Renaming notification_channels_new also renames the reference
		// of notification_channel_environments_new.
		`ALTER TABLE notification_channels_new RENAME TO notification_channels`,
		`ALTER TABLE notification_channel_environments_new RENAME TO notification_channel_environments`,
		`CREATE INDEX notification_channel_environments_environment ON notification_channel_environments (environment_id)`,
	}
}

func init() {
	// The built-in In App channel: the bell in the web UI. It has no
	// address (secret_sealed '', version 0, which only it may have), sends
	// nothing out and starts with every outcome of every kind, for every
	// environment. A channel already named "In App" is renamed "In App
	// (Renamed)". Down removes the In App channel; a renamed channel keeps
	// its new name.
	up := append([]string{
		`UPDATE notification_channels SET name = 'In App (Renamed)', name_key = 'in app (renamed)'
		 WHERE name_key = 'in app'`,
	}, rebuildChannels(addressUnlessBuiltIn)...)
	up = append(up,
		// SQLite's clock only because a migration has no injected clock.
		`INSERT INTO notification_channels (`+channelColumns+`)
		 VALUES (`+inAppChannelID+`, 'In App', 'in app', 'app', '', 1, `+inAppSubscriptions+`, 1, '', '', 0,
			strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'), '', NULL, NULL, 1,
			strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'), strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))`,
	)
	down := append([]string{
		`DELETE FROM notification_channels WHERE id = ` + inAppChannelID,
	}, rebuildChannels(addressRequired)...)
	Migrations.MustRegister(Tx(Exec(up...)), Tx(Exec(down...)))
}
