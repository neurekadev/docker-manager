// Package migrationtest builds migration sets for startup/migration tests.
// Only import it from _test.go files.
package migrationtest

import "github.com/uptrace/bun/migrate"

// Clone copies every migration of base into a new set.
func Clone(base *migrate.Migrations) *migrate.Migrations {
	set := migrate.NewMigrations()
	for _, m := range base.Sorted() {
		set.Add(m)
	}
	return set
}

// WithFailing returns base plus a final migration that creates a table and
// then fails, which must leave the database unchanged.
func WithFailing(base *migrate.Migrations) *migrate.Migrations {
	set := Clone(base)
	registerFailing(set)
	return set
}

// FailingTable is the table the failing migration creates before failing.
const FailingTable = "must_not_exist"

// FailingName is the name of the failing migration.
const FailingName = "29990101000000"

// Previous returns base without its newest n migrations: the schema of a
// previous release, for upgrade tests (#34).
func Previous(base *migrate.Migrations, n int) *migrate.Migrations {
	all := base.Sorted()
	set := migrate.NewMigrations()
	for _, m := range all[:max(len(all)-n, 0)] {
		set.Add(m)
	}
	return set
}
