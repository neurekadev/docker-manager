// Package migrations holds the manager's versioned Bun schema migrations.
//
// Adding a migration (see docs/internal/conventions/database-migrations.md):
//
//   - Create ONE file per migration named <UTC timestamp YYYYMMDDHHMMSS>_<snake_name>.go,
//     e.g. 20261001093000_create_users.go. Bun derives the migration name from
//     the file name, so the timestamp prefix orders migrations and keeps
//     parallel branches from colliding.
//   - In its init(), call Migrations.MustRegister(Tx(up), Tx(down)) directly
//     (not through a helper: Bun reads the caller's file name).
//   - Migrations are forward-only in practice: never edit a migration that has
//     been merged to main; add a new one. Use raw SQL (db.ExecContext) rather
//     than Bun models so a migration never changes when a model struct does.
//
// The manager applies pending migrations on every start before any listener
// or worker starts, after taking a VACUUM INTO snapshot (see
// internal/manager/store).
package migrations

import (
	"context"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// Migrations is the registry of all manager database migrations.
var Migrations = migrate.NewMigrations()

// Tx wraps a migration body in a transaction so a failing migration leaves
// the schema untouched (SQLite has transactional DDL).
func Tx(fn func(ctx context.Context, tx bun.Tx) error) migrate.MigrationFunc {
	return func(ctx context.Context, db *bun.DB) error {
		return db.RunInTx(ctx, nil, fn)
	}
}

// Exec returns a transactional migration body that runs the statements in order.
func Exec(statements ...string) func(ctx context.Context, tx bun.Tx) error {
	return func(ctx context.Context, tx bun.Tx) error {
		for _, s := range statements {
			if _, err := tx.ExecContext(ctx, s); err != nil {
				return err
			}
		}
		return nil
	}
}
