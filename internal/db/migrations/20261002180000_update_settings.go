package migrations

// updateRuleScope matches the permission rules the single updates setup
// leaves without meaning: update_policy.manage is instance-only now.
// update_policy.read keeps its scopes: it shows the target records of
// stacks and containers.
const updateRuleScope = `capability = 'update_policy.manage' AND scope_kind <> 'instance'`

const updateSettingsColumns = `singleton, id, exclude_environments, exclude_stacks, exclude_containers, check_cron, check_time_zone,
	check_enabled, run_cron, run_time_zone, run_enabled, run_window, wait_timeout_seconds, revision, updated_at`

func init() {
	// One updates setup for every environment (#240) replaces the
	// environment update policies:
	//
	//   - update_settings holds the one setup. Its id is the policy ID of
	//     its schedules and environment-wide jobs, and the parent of every
	//     target record (update_policies.parent_id), so the candidates,
	//     quarantine and history of every stack and container stay.
	//   - An all-environments policy becomes the setup as it is. Otherwise
	//     the only policy of one environment does, leaving every other
	//     environment out (its container exclusions gain their
	//     environment: environmentID/name), so what runs stays the same.
	//     Otherwise (none, or several) the setup starts with both schedules
	//     disabled and the schedule defaults.
	//   - Permission rules of update_policy.manage on one environment or
	//     one record go (instance-only now; widening them would grant more).
	//
	// Down turns the setup back into an all-environments policy; deleted
	// permission rules stay deleted.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE update_settings (
				singleton            INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
				id                   TEXT    NOT NULL CHECK (length(id) BETWEEN 1 AND 64),
				exclude_environments TEXT    NOT NULL DEFAULT '[]',
				exclude_stacks       TEXT    NOT NULL DEFAULT '[]',
				exclude_containers   TEXT    NOT NULL DEFAULT '[]',
				check_cron           TEXT    NOT NULL,
				check_time_zone      TEXT    NOT NULL,
				check_enabled        INTEGER NOT NULL CHECK (check_enabled IN (0, 1)),
				run_cron             TEXT    NOT NULL,
				run_time_zone        TEXT    NOT NULL,
				run_enabled          INTEGER NOT NULL CHECK (run_enabled IN (0, 1)),
				run_window           TEXT    NOT NULL DEFAULT '',
				wait_timeout_seconds INTEGER NOT NULL DEFAULT 0 CHECK (wait_timeout_seconds BETWEEN 0 AND 3600),
				revision             INTEGER NOT NULL CHECK (revision >= 1),
				updated_at           TEXT    NOT NULL
			) STRICT`,
			`INSERT INTO update_settings (`+updateSettingsColumns+`)
			 SELECT 1, id, '[]', exclude_stacks, exclude_containers, check_cron, check_time_zone, check_enabled, run_cron,
				run_time_zone, run_enabled, run_window, wait_timeout_seconds, revision, updated_at
			 FROM environment_update_policies WHERE environment_id = '' LIMIT 1`,
			`INSERT INTO update_settings (`+updateSettingsColumns+`)
			 SELECT 1, p.id, (SELECT json_group_array(e.id) FROM environments e WHERE e.id <> p.environment_id), p.exclude_stacks,
				(SELECT json_group_array(p.environment_id || '/' || c.value) FROM json_each(p.exclude_containers) c), p.check_cron,
				p.check_time_zone, p.check_enabled, p.run_cron, p.run_time_zone, p.run_enabled, p.run_window, p.wait_timeout_seconds,
				p.revision, p.updated_at
			 FROM environment_update_policies p
			 WHERE NOT EXISTS (SELECT 1 FROM update_settings) AND (SELECT count(*) FROM environment_update_policies) = 1`,
			// SQLite's clock only because a migration has no injected clock.
			`INSERT INTO update_settings (`+updateSettingsColumns+`)
			 SELECT 1, lower(hex(randomblob(16))), '[]', '[]', '[]',
				COALESCE((SELECT cron FROM schedule_default_crons WHERE kind = 'update_check'), '0 3 * * *'),
				COALESCE((SELECT time_zone FROM schedule_settings WHERE singleton = 1), 'UTC'), 0,
				COALESCE((SELECT cron FROM schedule_default_crons WHERE kind = 'update_run'), '0 4 * * *'),
				COALESCE((SELECT time_zone FROM schedule_settings WHERE singleton = 1), 'UTC'), 0, '', 0, 1,
				strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')
			 WHERE NOT EXISTS (SELECT 1 FROM update_settings)`,
			`UPDATE update_policies SET parent_id = (SELECT id FROM update_settings)`,
			`DROP TABLE environment_update_policies`,
			`UPDATE groups SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT group_id FROM group_permission_rules WHERE `+updateRuleScope+`)`,
			`UPDATE users SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT user_id FROM user_permission_rules WHERE `+updateRuleScope+`)`,
			`DELETE FROM group_permission_rules WHERE `+updateRuleScope,
			`DELETE FROM user_permission_rules WHERE `+updateRuleScope,
			`DELETE FROM api_token_scopes WHERE `+updateRuleScope,
		)),
		Tx(Exec(
			`CREATE TABLE environment_update_policies (
				id TEXT NOT NULL PRIMARY KEY,
				environment_id TEXT NOT NULL,
				name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
				exclude_stacks TEXT NOT NULL DEFAULT '[]',
				exclude_containers TEXT NOT NULL DEFAULT '[]',
				check_cron TEXT NOT NULL,
				check_time_zone TEXT NOT NULL,
				check_enabled INTEGER NOT NULL CHECK (check_enabled IN (0, 1)),
				run_cron TEXT NOT NULL,
				run_time_zone TEXT NOT NULL,
				run_enabled INTEGER NOT NULL CHECK (run_enabled IN (0, 1)),
				run_window TEXT NOT NULL DEFAULT '',
				wait_timeout_seconds INTEGER NOT NULL DEFAULT 0,
				revision INTEGER NOT NULL CHECK (revision >= 1),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				UNIQUE (environment_id)
			) STRICT`,
			`INSERT INTO environment_update_policies (id, environment_id, name, exclude_stacks, exclude_containers, check_cron,
				check_time_zone, check_enabled, run_cron, run_time_zone, run_enabled, run_window, wait_timeout_seconds, revision,
				created_at, updated_at)
			 SELECT id, '', 'Automatic updates', exclude_stacks, exclude_containers, check_cron, check_time_zone, check_enabled, run_cron,
				run_time_zone, run_enabled, run_window, wait_timeout_seconds, revision, updated_at, updated_at
			 FROM update_settings`,
			`DROP TABLE update_settings`,
		)),
	)
}
