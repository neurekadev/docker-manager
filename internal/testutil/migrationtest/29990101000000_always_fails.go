package migrationtest

import (
	"context"
	"errors"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
)

// registerFailing lives in a timestamp-named file because Bun derives the
// migration name from the caller's file name.
func registerFailing(set *migrate.Migrations) {
	set.MustRegister(
		migrations.Tx(func(ctx context.Context, tx bun.Tx) error {
			if _, err := tx.ExecContext(ctx, "CREATE TABLE "+FailingTable+" (id INTEGER PRIMARY KEY)"); err != nil {
				return err
			}
			return errors.New("injected migration failure")
		}),
		func(context.Context, *bun.DB) error { return nil },
	)
}
