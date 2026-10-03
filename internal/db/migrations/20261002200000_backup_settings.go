package migrations

// backupRuleScope matches the permission rules the single backup setup
// leaves without meaning: backup_policy.read and .manage are instance-only
// now, and no rule targets one backup policy.
const backupRuleScope = `(capability IN ('backup_policy.read', 'backup_policy.manage') AND scope_kind <> 'instance')
	OR resource_type = 'backup_policy'`

const backupSettingsColumns = `singleton, id, enabled, primary_repository_id, secondary_repository_id, exclude_environments,
	exclude_stacks, exclude_volumes, anonymous_volumes, buildx_volumes, external_binds, include_metrics, shutdown, cron, time_zone,
	retention, revision, updated_at`

func init() {
	// One backup setup for every environment (#246) replaces the backup
	// policies:
	//
	//   - backup_settings holds the one setup. Its id is the policy ID
	//     sets, snapshots, jobs and the restic tag policy:<id> carry.
	//   - An all-environments policy becomes the setup as it is (its
	//     per-environment repositories go: everything goes to the Primary).
	//     Otherwise the only policy of one environment does, leaving every
	//     other current environment out, its volume exclusions keyed
	//     environmentID/name. Otherwise (none, or several) the setup starts
	//     disabled with the defaults.
	//   - The Primary is the adopted policy's repository (for one
	//     environment, the repository it used there), otherwise the oldest
	//     repository; there is no Secondary yet. The manager state is
	//     always backed up now.
	//   - Policies that are not adopted go; their sets and backups stay
	//     browsable and restorable.
	//   - Permission rules on one environment or one policy of
	//     backup_policy.read and .manage go (they are instance-only now;
	//     widening them would grant more), as do rules and token scopes on
	//     one backup policy.
	//
	// Down turns the setup back into an all-environments policy (when it
	// has a Primary); deleted permission rules stay deleted.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE backup_settings (
				singleton               INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
				id                      TEXT    NOT NULL CHECK (length(id) BETWEEN 1 AND 64),
				enabled                 INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
				primary_repository_id   TEXT    NOT NULL DEFAULT '',
				secondary_repository_id TEXT    NOT NULL DEFAULT '',
				exclude_environments    TEXT    NOT NULL DEFAULT '[]',
				exclude_stacks          TEXT    NOT NULL DEFAULT '[]',
				exclude_volumes         TEXT    NOT NULL DEFAULT '[]',
				anonymous_volumes       INTEGER NOT NULL DEFAULT 0 CHECK (anonymous_volumes IN (0, 1)),
				buildx_volumes          INTEGER NOT NULL DEFAULT 0 CHECK (buildx_volumes IN (0, 1)),
				external_binds          INTEGER NOT NULL DEFAULT 0 CHECK (external_binds IN (0, 1)),
				include_metrics         INTEGER NOT NULL DEFAULT 0 CHECK (include_metrics IN (0, 1)),
				shutdown                INTEGER NOT NULL DEFAULT 0 CHECK (shutdown IN (0, 1)),
				cron                    TEXT    NOT NULL,
				time_zone               TEXT    NOT NULL,
				retention               TEXT    NOT NULL DEFAULT '{}',
				revision                INTEGER NOT NULL CHECK (revision >= 1),
				updated_at              TEXT    NOT NULL,
				CHECK (secondary_repository_id = '' OR secondary_repository_id <> primary_repository_id)
			) STRICT`,
			`INSERT INTO backup_settings (`+backupSettingsColumns+`)
			 SELECT 1, id, enabled, repository_id, '', '[]', exclude_stacks, exclude_volumes, anonymous_volumes, buildx_volumes,
				external_binds, include_metrics, shutdown, cron, time_zone, retention, revision, updated_at
			 FROM backup_policies WHERE environment_id = '' LIMIT 1`,
			`INSERT INTO backup_settings (`+backupSettingsColumns+`)
			 SELECT 1, p.id, p.enabled,
				COALESCE(NULLIF(json_extract(p.environment_repos, '$."' || p.environment_id || '"'), ''), p.repository_id), '',
				(SELECT json_group_array(e.id) FROM environments e WHERE e.id <> p.environment_id), p.exclude_stacks,
				(SELECT json_group_array(p.environment_id || '/' || v.value) FROM json_each(p.exclude_volumes) v),
				p.anonymous_volumes, p.buildx_volumes, p.external_binds, p.include_metrics, p.shutdown, p.cron, p.time_zone,
				p.retention, p.revision, p.updated_at
			 FROM backup_policies p
			 WHERE NOT EXISTS (SELECT 1 FROM backup_settings) AND (SELECT count(*) FROM backup_policies) = 1`,
			// SQLite's clock only because a migration has no injected clock.
			`INSERT INTO backup_settings (`+backupSettingsColumns+`)
			 SELECT 1, lower(hex(randomblob(16))), 0,
				COALESCE((SELECT id FROM backup_repositories ORDER BY created_at, id LIMIT 1), ''), '', '[]', '[]', '[]', 0, 0, 0, 0, 0,
				COALESCE((SELECT cron FROM schedule_default_crons WHERE kind = 'backup'), '0 * * * *'),
				COALESCE((SELECT time_zone FROM schedule_settings WHERE singleton = 1), 'UTC'), '{}', 1,
				strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')
			 WHERE NOT EXISTS (SELECT 1 FROM backup_settings)`,
			`DROP TABLE backup_policies`,
			`UPDATE groups SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT group_id FROM group_permission_rules WHERE `+backupRuleScope+`)`,
			`UPDATE users SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT user_id FROM user_permission_rules WHERE `+backupRuleScope+`)`,
			`DELETE FROM group_permission_rules WHERE `+backupRuleScope,
			`DELETE FROM user_permission_rules WHERE `+backupRuleScope,
			`DELETE FROM api_token_scopes WHERE `+backupRuleScope,
		)),
		Tx(Exec(
			`CREATE TABLE backup_policies (
				id                TEXT    NOT NULL PRIMARY KEY,
				name              TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key          TEXT    NOT NULL UNIQUE,
				repository_id     TEXT    NOT NULL REFERENCES backup_repositories (id),
				environment_repos TEXT    NOT NULL DEFAULT '{}',
				include_manager   INTEGER NOT NULL DEFAULT 0 CHECK (include_manager IN (0, 1)),
				include_metrics   INTEGER NOT NULL DEFAULT 0 CHECK (include_metrics IN (0, 1)),
				stacks            TEXT    NOT NULL DEFAULT '[]',
				volumes           TEXT    NOT NULL DEFAULT '[]',
				shutdown          INTEGER NOT NULL DEFAULT 0 CHECK (shutdown IN (0, 1)),
				cron              TEXT    NOT NULL,
				time_zone         TEXT    NOT NULL,
				enabled           INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
				retention         TEXT    NOT NULL DEFAULT '{}',
				revision          INTEGER NOT NULL CHECK (revision >= 1),
				created_at        TEXT    NOT NULL,
				updated_at        TEXT    NOT NULL,
				environment_id    TEXT    NOT NULL DEFAULT '',
				exclude_stacks    TEXT    NOT NULL DEFAULT '[]',
				exclude_volumes   TEXT    NOT NULL DEFAULT '[]',
				anonymous_volumes INTEGER NOT NULL DEFAULT 0 CHECK (anonymous_volumes IN (0, 1)),
				buildx_volumes    INTEGER NOT NULL DEFAULT 0 CHECK (buildx_volumes IN (0, 1)),
				external_binds    INTEGER NOT NULL DEFAULT 0 CHECK (external_binds IN (0, 1))
			) STRICT`,
			`CREATE UNIQUE INDEX backup_policy_scope ON backup_policies (environment_id)`,
			`INSERT INTO backup_policies (id, name, name_key, repository_id, include_manager, include_metrics, shutdown, cron, time_zone,
				enabled, retention, revision, created_at, updated_at, exclude_stacks, exclude_volumes, anonymous_volumes, buildx_volumes,
				external_binds)
			 SELECT id, 'Backups', 'backups', primary_repository_id, 1, include_metrics, shutdown, cron, time_zone, enabled, retention,
				revision, updated_at, updated_at, exclude_stacks, exclude_volumes, anonymous_volumes, buildx_volumes, external_binds
			 FROM backup_settings WHERE primary_repository_id <> ''`,
			`DROP TABLE backup_settings`,
		)),
	)
}
