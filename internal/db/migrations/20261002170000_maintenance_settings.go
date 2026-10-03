package migrations

// maintenanceRuleScope matches the permission rules the single maintenance
// setup leaves without meaning: maintenance_policy.read and .manage are
// instance-only now, and no rule targets one maintenance policy.
const maintenanceRuleScope = `(capability IN ('maintenance_policy.read', 'maintenance_policy.manage') AND scope_kind <> 'instance')
	OR resource_type = 'maintenance_policy'`

const maintenanceSettingsColumns = `singleton, id, enabled, cron, time_zone, rules, exclude_environments, last_run, revision, updated_at`

func init() {
	// One maintenance setup for every environment (#238) replaces the
	// prune policies and the default rules:
	//
	//   - maintenance_settings holds the one setup. Its id is the policy ID
	//     prune jobs and schedules carry.
	//   - An all-environments policy becomes the setup as it is. Otherwise
	//     the only policy of one environment does, leaving every other
	//     environment out, so what runs stays the same. Otherwise (none,
	//     or several) the setup starts disabled with the default rules.
	//   - Permission rules on one environment or one policy of
	//     maintenance_policy.read and .manage go (they are instance-only
	//     now; widening them would grant more), as do rules and token
	//     scopes on one maintenance policy. maintenance.preview and .run on
	//     an environment stay: one-off prunes use them.
	//
	// Down turns the setup back into an all-environments policy and its
	// rules into the default rules; deleted permission rules stay deleted.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE maintenance_settings (
				singleton            INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
				id                   TEXT    NOT NULL CHECK (length(id) BETWEEN 1 AND 64),
				enabled              INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
				cron                 TEXT    NOT NULL,
				time_zone            TEXT    NOT NULL,
				rules                TEXT    NOT NULL DEFAULT '',
				exclude_environments TEXT    NOT NULL DEFAULT '[]',
				last_run             TEXT    NOT NULL DEFAULT '',
				revision             INTEGER NOT NULL CHECK (revision >= 1),
				updated_at           TEXT    NOT NULL
			) STRICT`,
			`INSERT INTO maintenance_settings (`+maintenanceSettingsColumns+`)
			 SELECT 1, id, schedule_enabled, cron, time_zone, rules, '[]', last_run, revision, updated_at
			 FROM maintenance_policies WHERE environment_id = '' LIMIT 1`,
			`INSERT INTO maintenance_settings (`+maintenanceSettingsColumns+`)
			 SELECT 1, p.id, p.schedule_enabled, p.cron, p.time_zone, p.rules,
				(SELECT json_group_array(e.id) FROM environments e WHERE e.id <> p.environment_id), p.last_run, p.revision, p.updated_at
			 FROM maintenance_policies p
			 WHERE NOT EXISTS (SELECT 1 FROM maintenance_settings) AND (SELECT count(*) FROM maintenance_policies) = 1`,
			// SQLite's clock only because a migration has no injected clock.
			`INSERT INTO maintenance_settings (`+maintenanceSettingsColumns+`)
			 SELECT 1, lower(hex(randomblob(16))), 0,
				COALESCE((SELECT cron FROM schedule_default_crons WHERE kind = 'prune'), '0 3 * * 0'),
				COALESCE((SELECT time_zone FROM schedule_settings WHERE singleton = 1), 'UTC'),
				COALESCE((SELECT rules FROM maintenance_defaults WHERE singleton = 1), ''), '[]', '', 1,
				strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')
			 WHERE NOT EXISTS (SELECT 1 FROM maintenance_settings)`,
			`DROP TABLE maintenance_policies`,
			`DROP TABLE maintenance_defaults`,
			`UPDATE groups SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT group_id FROM group_permission_rules WHERE `+maintenanceRuleScope+`)`,
			`UPDATE users SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT user_id FROM user_permission_rules WHERE `+maintenanceRuleScope+`)`,
			`DELETE FROM group_permission_rules WHERE `+maintenanceRuleScope,
			`DELETE FROM user_permission_rules WHERE `+maintenanceRuleScope,
			`DELETE FROM api_token_scopes WHERE `+maintenanceRuleScope,
		)),
		Tx(Exec(
			`CREATE TABLE maintenance_policies (
				id TEXT NOT NULL PRIMARY KEY,
				environment_id TEXT NOT NULL DEFAULT '',
				name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				name_key TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				cron TEXT NOT NULL,
				time_zone TEXT NOT NULL,
				schedule_enabled INTEGER NOT NULL DEFAULT 0 CHECK (schedule_enabled IN (0, 1)),
				rules TEXT NOT NULL,
				last_run TEXT NOT NULL DEFAULT '',
				revision INTEGER NOT NULL CHECK (revision >= 1),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			) STRICT`,
			`CREATE UNIQUE INDEX maintenance_policy_scope ON maintenance_policies (environment_id)`,
			`CREATE TABLE maintenance_defaults (
				singleton  INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
				rules      TEXT    NOT NULL DEFAULT '',
				revision   INTEGER NOT NULL CHECK (revision >= 1),
				updated_at TEXT    NOT NULL
			) STRICT`,
			`INSERT INTO maintenance_policies (id, environment_id, name, name_key, description, cron, time_zone, schedule_enabled,
				rules, last_run, revision, created_at, updated_at)
			 SELECT id, '', 'Maintenance', 'maintenance', '', cron, time_zone, enabled, rules, last_run, revision, updated_at, updated_at
			 FROM maintenance_settings`,
			`INSERT INTO maintenance_defaults (singleton, rules, revision, updated_at)
			 SELECT 1, rules, 1, updated_at FROM maintenance_settings`,
			`DROP TABLE maintenance_settings`,
		)),
	)
}
