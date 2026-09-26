package migrations

import (
	"context"

	"github.com/uptrace/bun"
)

func init() {
	Migrations.MustRegister(
		Tx(func(ctx context.Context, tx bun.Tx) error {
			return Exec(
				`DELETE FROM update_history`,
				`DELETE FROM update_quarantine`,
				`DELETE FROM update_candidates`,
				`DELETE FROM update_policies`,
				`ALTER TABLE update_policies ADD COLUMN parent_id TEXT NOT NULL DEFAULT ''`,
				`ALTER TABLE update_policies ADD COLUMN active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1))`,
				`CREATE INDEX update_policies_parent ON update_policies (parent_id, active)`,
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
			)(ctx, tx)
		}),
		Tx(func(ctx context.Context, tx bun.Tx) error {
			return Exec(
				`DROP TABLE environment_update_policies`,
				`DROP INDEX update_policies_parent`,
				`ALTER TABLE update_policies DROP COLUMN active`,
				`ALTER TABLE update_policies DROP COLUMN parent_id`,
			)(ctx, tx)
		}),
	)
}
