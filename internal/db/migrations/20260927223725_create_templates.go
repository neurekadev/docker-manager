package migrations

func init() {
	// Stack templates (template registry).
	//
	//   - templates: metadata; tags is a JSON array of lowercase tags;
	//     version_seq numbers publications (never reused after a version is
	//     deleted). The draft files live in <data dir>/templates/<id>/draft.
	//   - template_icons: at most one icon per template (sniffed image
	//     bytes, sha256 of data).
	//   - template_versions: immutable publications; archive is the sealed
	//     canonical tar.gz of the draft (context
	//     "template_versions/<id>/archive"), archive_sha256/size describe the
	//     unsealed tar.gz, definition a JSON array of {path, size}.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE templates (
				id                 TEXT    NOT NULL PRIMARY KEY,
				name               TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key           TEXT    NOT NULL UNIQUE,
				description        TEXT    NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
				tags               TEXT    NOT NULL DEFAULT '[]',
				visibility         TEXT    NOT NULL CHECK (visibility IN ('private', 'public')),
				version_seq        INTEGER NOT NULL DEFAULT 0 CHECK (version_seq >= 0),
				created_by_user_id TEXT    NOT NULL DEFAULT '',
				revision           INTEGER NOT NULL CHECK (revision >= 1),
				created_at         TEXT    NOT NULL,
				updated_at         TEXT    NOT NULL
			) STRICT`,
			`CREATE TABLE template_icons (
				template_id TEXT    NOT NULL PRIMARY KEY REFERENCES templates (id) ON DELETE CASCADE,
				media_type  TEXT    NOT NULL,
				sha256      TEXT    NOT NULL,
				size        INTEGER NOT NULL CHECK (size BETWEEN 1 AND 262144),
				data        BLOB    NOT NULL,
				updated_at  TEXT    NOT NULL
			) STRICT`,
			`CREATE TABLE template_versions (
				id                   TEXT    NOT NULL PRIMARY KEY,
				template_id          TEXT    NOT NULL REFERENCES templates (id) ON DELETE CASCADE,
				number               INTEGER NOT NULL CHECK (number >= 1),
				label                TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 32),
				notes                TEXT    NOT NULL DEFAULT '' CHECK (length(notes) <= 4096),
				archive              TEXT    NOT NULL,
				archive_sha256       TEXT    NOT NULL,
				archive_size         INTEGER NOT NULL CHECK (archive_size >= 0),
				content_size         INTEGER NOT NULL CHECK (content_size >= 0),
				entries              INTEGER NOT NULL CHECK (entries >= 0),
				definition           TEXT    NOT NULL DEFAULT '[]',
				published_by_user_id TEXT    NOT NULL DEFAULT '',
				created_at           TEXT    NOT NULL,
				UNIQUE (template_id, number),
				UNIQUE (template_id, label)
			) STRICT`,
			`CREATE TRIGGER template_versions_immutable BEFORE UPDATE ON template_versions
			BEGIN
				SELECT RAISE(ABORT, 'template versions are immutable');
			END`,
		)),
		Tx(Exec(
			`DROP TABLE template_versions`,
			`DROP TABLE template_icons`,
			`DROP TABLE templates`,
		)),
	)
}
