package migrations

import (
	"context"

	"github.com/uptrace/bun"
)

func init() {
	// Instance settings (#4, GET/PATCH /api/v1/settings): the singleton,
	// revisioned row of editable instance-wide settings. Only the display
	// name so far; the sign-in policy (security_settings), schedule
	// defaults (schedule_settings) and maintenance defaults keep their own
	// tables and revisions.
	Migrations.MustRegister(
		Tx(func(ctx context.Context, tx bun.Tx) error {
			if err := Exec(
				`CREATE TABLE instance_settings (
					singleton  INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
					name       TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
					revision   INTEGER NOT NULL CHECK (revision >= 1),
					updated_at TEXT    NOT NULL
				) STRICT`,
			)(ctx, tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO instance_settings (singleton, name, revision, updated_at)
				VALUES (1, 'DockYard', 1, strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))`)
			return err
		}),
		Tx(Exec(
			`DROP TABLE instance_settings`,
		)),
	)
}
