// Package metrics is the manager's metrics storage (#5): a separate
// SQLite file (<data>/metrics.db) with its own migrations
// (internal/db/metricsmigrations), holding 10 s samples, 1 min and 15 min
// rollups, the collector cursors and the last Engine inventories.
//
// Guarantees (docs/architecture/metrics.md):
//
//   - Ingestion is idempotent: the key (series, 10 s slot) ignores a sample
//     that is delivered twice (agent reconnect, manager restart).
//   - Missing data stays missing: a NULL value or an absent slot is a gap
//     in query results, never a zero.
//   - Retention per level (raw 24 h, 1 min 7 d, 15 min 90 d by default),
//     a series cap and a storage cap are enforced by Run (rollups every
//     minute, retention every 10 minutes, each bounded per pass).
//
// One writer connection serializes writes; queries use a separate small
// read-only pool (WAL readers never block the writer).
package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	"github.com/neurekadev/dockyard/internal/clock"
	"github.com/neurekadev/dockyard/internal/db/metricsmigrations"
	"github.com/neurekadev/dockyard/internal/domain"
	"github.com/neurekadev/dockyard/internal/manager/store"
)

// FileName is the metrics database file inside the data directory.
const FileName = "metrics.db"

// Defaults (overridable through Options; DOCKYARD_METRICS_* in
// docs/configuration.md).
const (
	DefaultRawRetention     = 24 * time.Hour
	DefaultMinuteRetention  = 7 * 24 * time.Hour
	DefaultQuarterRetention = 90 * 24 * time.Hour
	DefaultMaxBytes         = 2 << 30
	DefaultMaxSeries        = 5000
	// MaxPoints bounds the buckets of one query.
	MaxPoints = 1000
	// DefaultPoints is the target bucket count of an automatic step.
	DefaultPoints = 300
)

// Retention is how long each level is kept.
type Retention struct {
	Raw, Minute, Quarter time.Duration
}

// Options configures Open.
type Options struct {
	// Path is the database file.
	Path   string
	Clock  clock.Clock
	Logger *slog.Logger
	// Retention defaults to 24 h / 7 d / 90 d.
	Retention Retention
	// MaxBytes caps the used database pages (default 2 GiB).
	MaxBytes int64
	// MaxSeries caps the number of series (environments' hosts, disks and
	// containers; default 5000).
	MaxSeries int
	// RollupDelay is how long after a minute ends its rollup waits for
	// late samples (default 30 s).
	RollupDelay time.Duration
}

// Store is the metrics database.
type Store struct {
	opts Options
	db   *bun.DB
	read *sql.DB
	clk  clock.Clock
	log  *slog.Logger

	mu     sync.Mutex
	series map[seriesKey]int64
	nSer   int
	full   bool
	// horizon scales the retention of every level while over the storage
	// cap (1 = the configured retention).
	horizon float64
}

type seriesKey struct {
	env, kind, name string
}

// dsn builds the connection string: incremental auto-vacuum (set before
// the first table exists) so retention can return pages to the OS, WAL,
// and a bounded WAL file.
func dsn(path string, readOnly bool) string {
	var b strings.Builder
	b.WriteString(path)
	b.WriteString("?_txlock=immediate")
	pragmas := []string{"auto_vacuum(2)", "busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)", "journal_size_limit(67108864)"}
	if readOnly {
		pragmas = []string{"busy_timeout(5000)", "query_only(1)"}
	}
	for _, p := range pragmas {
		b.WriteString("&_pragma=")
		b.WriteString(p)
	}
	return b.String()
}

// Open opens (creating if needed) and migrates the metrics database.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Retention.Raw <= 0 {
		opts.Retention.Raw = DefaultRawRetention
	}
	if opts.Retention.Minute <= 0 {
		opts.Retention.Minute = DefaultMinuteRetention
	}
	if opts.Retention.Quarter <= 0 {
		opts.Retention.Quarter = DefaultQuarterRetention
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.MaxSeries <= 0 {
		opts.MaxSeries = DefaultMaxSeries
	}
	if opts.RollupDelay <= 0 {
		opts.RollupDelay = 30 * time.Second
	}
	if strings.ContainsAny(opts.Path, "?#") {
		return nil, errors.New("metrics: database path must not contain '?' or '#'")
	}
	w, err := sql.Open(sqliteshim.ShimName, dsn(opts.Path, false))
	if err != nil {
		return nil, fmt.Errorf("metrics: open: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	db := bun.NewDB(w, sqlitedialect.New())
	s := &Store{opts: opts, db: db, clk: opts.Clock, log: opts.Logger, series: map[seriesKey]int64{}, horizon: 1}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("metrics: open %s: %w", opts.Path, err)
	}
	if _, err := store.Migrate(ctx, db, store.MigrateOptions{Migrations: metricsmigrations.Migrations, NoSnapshot: true,
		Clock: opts.Clock, Logger: opts.Logger}); err != nil {
		return nil, fmt.Errorf("metrics: %w (the metrics database holds only expendable samples: "+
			"moving %s away starts an empty one)", err, opts.Path)
	}
	if s.read, err = sql.Open(sqliteshim.ShimName, dsn(opts.Path, true)); err != nil {
		return nil, fmt.Errorf("metrics: open reader: %w", err)
	}
	s.read.SetMaxOpenConns(4)
	s.read.SetMaxIdleConns(4)
	if err := s.loadSeries(ctx); err != nil {
		return nil, err
	}
	if err := s.initRollupState(ctx); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error {
	var errs []error
	if s.read != nil {
		errs = append(errs, s.read.Close())
	}
	errs = append(errs, s.db.Close())
	return errors.Join(errs...)
}

// DB returns the writer (tests, diagnostics).
func (s *Store) DB() *bun.DB { return s.db }

func (s *Store) loadSeries(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, environment_id, kind, name FROM series`)
	if err != nil {
		return fmt.Errorf("metrics: load series: %w", err)
	}
	defer func() { _ = rows.Close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var id int64
		var k seriesKey
		if err := rows.Scan(&id, &k.env, &k.kind, &k.name); err != nil {
			return err
		}
		s.series[k] = id
	}
	s.nSer = len(s.series)
	return rows.Err()
}

func (s *Store) initRollupState(ctx context.Context) error {
	now := s.clk.Now().Unix()
	for _, l := range levels[1:] {
		r := int64(l.res / time.Second)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO rollup_state (level, done_until) VALUES (?, ?) ON CONFLICT (level) DO NOTHING`,
			l.name, now/r*r); err != nil {
			return fmt.Errorf("metrics: init rollup state: %w", err)
		}
	}
	return nil
}

// Size is the used size of the database in bytes (pages in use; free
// pages are reused before the file grows).
func (s *Store) Size(ctx context.Context) (int64, error) {
	var pages, free, size int64
	for q, dst := range map[string]*int64{"PRAGMA page_count": &pages, "PRAGMA freelist_count": &free, "PRAGMA page_size": &size} {
		if err := s.db.QueryRowContext(ctx, q).Scan(dst); err != nil {
			return 0, fmt.Errorf("metrics: %s: %w", q, err)
		}
	}
	return (pages - free) * size, nil
}

// Stats describes the store for diagnostics and the storage documentation.
type Stats struct {
	UsedBytes int64
	MaxBytes  int64
	Series    int
	MaxSeries int
	Full      bool
	// Horizon is the retention factor applied while over the cap (1: none).
	Horizon float64
	Rows    map[string]int64
}

// Stats counts rows per table (a full scan: diagnostics and tests only).
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	used, err := s.Size(ctx)
	if err != nil {
		return Stats{}, err
	}
	s.mu.Lock()
	st := Stats{UsedBytes: used, MaxBytes: s.opts.MaxBytes, Series: s.nSer, MaxSeries: s.opts.MaxSeries, Full: s.full, Horizon: s.horizon,
		Rows: map[string]int64{}}
	s.mu.Unlock()
	for k := range kinds {
		for _, l := range levels {
			var n int64
			t := table(k, l.name)
			if err := s.read.QueryRowContext(ctx, "SELECT count(*) FROM "+t).Scan(&n); err != nil {
				return Stats{}, err
			}
			st.Rows[t] = n
		}
	}
	return st, nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// unix returns t's unix seconds aligned down to res.
func align(t time.Time, res time.Duration) int64 {
	r := int64(res / time.Second)
	u := t.Unix()
	if u < 0 {
		return (u - r + 1) / r * r
	}
	return u / r * r
}

// Cursor returns the collector cursor of an environment.
func (s *Store) Cursor(ctx context.Context, envID string) (domain.MetricCursor, bool, error) {
	var c domain.MetricCursor
	var seq, skew int64
	err := s.db.QueryRowContext(ctx, `SELECT epoch, last_seq, skew_ms FROM collector_state WHERE environment_id = ?`, envID).Scan(&c.Epoch, &seq, &skew)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("metrics: read cursor: %w", err)
	}
	c.LastSeq, c.Skew = uint64(seq), time.Duration(skew)*time.Millisecond //nolint:gosec // G115: stored from a uint64 below 2^63
	return c, true, nil
}
