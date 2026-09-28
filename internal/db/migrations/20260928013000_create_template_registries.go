package migrations

func init() {
	// Template registries of other Docker Manager instances (template
	// registry). instance_id (the remote instance's ID) identifies a
	// registry, so adding the same instance again (even under another URL)
	// finds the stacks created from its templates; entries and icons are
	// the cached index, replaced by each sync and deleted with the registry.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE template_registries (
				instance_id      TEXT    NOT NULL PRIMARY KEY,
				url              TEXT    NOT NULL UNIQUE,
				name             TEXT    NOT NULL,
				status           TEXT    NOT NULL CHECK (status IN ('ok', 'error')),
				error_class      TEXT    NOT NULL DEFAULT '',
				error_message    TEXT    NOT NULL DEFAULT '',
				etag             TEXT    NOT NULL DEFAULT '',
				failures         INTEGER NOT NULL DEFAULT 0,
				synced_at        TEXT,
				attempted_at     TEXT,
				added_by_user_id TEXT    NOT NULL DEFAULT '',
				created_at       TEXT    NOT NULL,
				updated_at       TEXT    NOT NULL
			) STRICT`,
			`CREATE TABLE template_registry_entries (
				registry_id   TEXT NOT NULL REFERENCES template_registries (instance_id) ON DELETE CASCADE,
				template_id   TEXT NOT NULL,
				name          TEXT NOT NULL,
				description   TEXT NOT NULL DEFAULT '',
				tags          TEXT NOT NULL DEFAULT '[]',
				versions      TEXT NOT NULL DEFAULT '[]',
				icon_sha256   TEXT NOT NULL DEFAULT '',
				icon_url      TEXT NOT NULL DEFAULT '',
				updated_at    TEXT NOT NULL,
				PRIMARY KEY (registry_id, template_id)
			) STRICT`,
			`CREATE TABLE template_registry_icons (
				registry_id TEXT    NOT NULL,
				template_id TEXT    NOT NULL,
				media_type  TEXT    NOT NULL,
				sha256      TEXT    NOT NULL,
				size        INTEGER NOT NULL CHECK (size BETWEEN 1 AND 262144),
				data        BLOB    NOT NULL,
				PRIMARY KEY (registry_id, template_id),
				FOREIGN KEY (registry_id, template_id) REFERENCES template_registry_entries (registry_id, template_id) ON DELETE CASCADE
			) STRICT`,
		)),
		Tx(Exec(
			`DROP TABLE template_registry_icons`,
			`DROP TABLE template_registry_entries`,
			`DROP TABLE template_registries`,
		)),
	)
}
