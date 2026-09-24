// Package store owns the manager's SQLite database: opening it with the
// required pragmas, pre-migration snapshots and running migrations.
//
// Connection guidance (single writer): SQLite allows one writer at a time.
// The manager uses ONE *bun.DB with a single open connection, so every write
// is serialized in-process instead of contending on SQLITE_BUSY, and
// transactions start with BEGIN IMMEDIATE (_txlock=immediate) so a read that
// later upgrades to a write cannot deadlock. Keep transactions short and
// never hold one across network or Docker calls. If read concurrency becomes
// a bottleneck, add a separate read-only pool (query_only) next to this
// writer; do not raise MaxOpenConns on the writer.
//
// Metrics samples will live in a separate database file (#5) so high-volume
// writes and retention cleanup do not contend with the main store.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"
)

// Pragmas applied to every connection.
var pragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"foreign_keys(1)",
	"synchronous(NORMAL)",
}

// DSN builds the driver data source name for a database file path.
func DSN(path string) string {
	var b strings.Builder
	b.WriteString(path)
	b.WriteString("?_txlock=immediate")
	for _, p := range pragmas {
		b.WriteString("&_pragma=")
		b.WriteString(p)
	}
	return b.String()
}

// Open opens (creating if needed) the database at path and verifies the
// connection and pragmas.
func Open(ctx context.Context, path string) (*bun.DB, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: database path must not contain '?' or '#'")
	}
	sqldb, err := sql.Open(sqliteshim.ShimName, DSN(path))
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	sqldb.SetMaxOpenConns(1)
	sqldb.SetMaxIdleConns(1)
	sqldb.SetConnMaxLifetime(0)
	sqldb.SetConnMaxIdleTime(0)

	db := bun.NewDB(sqldb, sqlitedialect.New())
	if err := verify(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func verify(ctx context.Context, db *bun.DB) error {
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("store: read journal_mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("store: journal_mode is %q, want wal", mode)
	}
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("store: read foreign_keys: %w", err)
	}
	if fk != 1 {
		return errors.New("store: foreign_keys pragma is off")
	}
	return nil
}

// UserTables lists the non-internal tables in the database, sorted.
func UserTables(ctx context.Context, db bun.IDB) ([]string, error) {
	var names []string
	err := db.NewRaw(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`).
		Scan(ctx, &names)
	return names, err
}
