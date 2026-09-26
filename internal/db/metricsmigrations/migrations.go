// Package metricsmigrations holds the schema migrations of the metrics
// database (<data>/metrics.db, #5). It is a separate SQLite file from the
// manager database so high-volume sample writes, rollups and retention
// never contend with jobs and authentication, and so manager-state backups
// (#10) can leave it out.
//
// The rules of internal/db/migrations apply: one file per migration named
// <UTC timestamp YYYYMMDDHHMMSS>_<snake_name>.go, registered in init() with
// Migrations.MustRegister(Tx(up), Tx(down)); never edit a merged migration.
package metricsmigrations

import (
	"github.com/uptrace/bun/migrate"

	"code.neureka.dev/docker-manager/docker-manager/internal/db/migrations"
)

// Migrations is the registry of all metrics database migrations.
var Migrations = migrate.NewMigrations()

// Tx and Exec are the manager database's helpers.
var (
	Tx   = migrations.Tx
	Exec = migrations.Exec
)
