package metrics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/uptrace/bun"
)

// Work bounds of one maintenance pass (bounded CPU on a small manager).
const (
	// rollupSpan bounds the time range one rollup transaction covers.
	minuteRollupSpan  = time.Hour
	quarterRollupSpan = 6 * time.Hour
	// maxRollupSteps bounds the transactions of one Rollup call.
	maxRollupSteps = 24
	// retainChunk is the number of series per retention transaction.
	retainChunk = 250
	// minHorizon is the smallest retention factor the storage cap may
	// impose (then new series are refused instead).
	minHorizon = 0.05
	// RollupInterval and RetentionInterval pace Run.
	RollupInterval    = time.Minute
	RetentionInterval = 10 * time.Minute
)

// Watermark returns how far a rollup level is complete (exclusive).
func (s *Store) Watermark(ctx context.Context, lvl string) (time.Time, error) {
	var done int64
	if err := s.read.QueryRowContext(ctx, `SELECT done_until FROM rollup_state WHERE level = ?`, lvl).Scan(&done); err != nil {
		return time.Time{}, fmt.Errorf("metrics: read watermark: %w", err)
	}
	return time.Unix(done, 0).UTC(), nil
}

// Rollup aggregates complete buckets into the 1 min and 15 min levels,
// including buckets reopened by late samples. Each step is one short
// transaction; at most maxRollupSteps run per call.
func (s *Store) Rollup(ctx context.Context) error {
	now := s.clk.Now().Add(-s.opts.RollupDelay)
	steps := 0
	for i := 1; i < len(levels) && steps < maxRollupSteps; i++ {
		l, finer := levels[i], levels[i-1]
		span := minuteRollupSpan
		if l.name == LevelQuarter {
			span = quarterRollupSpan
		}
		for steps < maxRollupSteps {
			more := false
			err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
				var done int64
				if err := tx.QueryRowContext(ctx, `SELECT done_until FROM rollup_state WHERE level = ?`, l.name).Scan(&done); err != nil {
					return err
				}
				target := align(now, l.res)
				// Nothing older than the finer level's retention can be
				// aggregated any more: skip ahead.
				done = max(done, align(now.Add(-s.retention(finer.name)), l.res))
				if l.name == LevelQuarter {
					var fine int64
					if err := tx.QueryRowContext(ctx, `SELECT done_until FROM rollup_state WHERE level = ?`, finer.name).Scan(&fine); err != nil {
						return err
					}
					target = min(target, fine/int64(l.res/time.Second)*int64(l.res/time.Second))
				}
				if target <= done {
					return nil
				}
				end := min(target, done+int64(span/time.Second))
				for _, k := range kindOrder {
					if _, err := tx.ExecContext(ctx, rollupSQL(kinds[k], l, finer), done, end, k); err != nil {
						return fmt.Errorf("metrics: rollup %s %s: %w", k, l.name, err)
					}
				}
				if _, err := tx.ExecContext(ctx, `UPDATE rollup_state SET done_until = ? WHERE level = ?`, end, l.name); err != nil {
					return err
				}
				more = end < target
				return nil
			})
			if err != nil {
				return err
			}
			steps++
			if !more {
				break
			}
		}
	}
	return nil
}

var kindOrder = []string{"host", "disk", "sensor", "container"}

// RetainResult reports a retention pass.
type RetainResult struct {
	Deleted   int64
	Series    int
	UsedBytes int64
	Horizon   float64
	Full      bool
}

// Retain deletes data older than each level's retention and series without
// data, then enforces the storage cap: while the used size exceeds it, the
// retention of every level is shortened (horizon) and old data deleted;
// at the minimum horizon new series are refused until space is free.
func (s *Store) Retain(ctx context.Context) (RetainResult, error) {
	var res RetainResult
	for range 20 {
		n, err := s.deleteExpired(ctx)
		if err != nil {
			return res, err
		}
		res.Deleted += n
		used, err := s.Size(ctx)
		if err != nil {
			return res, err
		}
		res.UsedBytes = used
		s.mu.Lock()
		over := used > s.opts.MaxBytes
		switch {
		case over && s.horizon > minHorizon:
			s.horizon = max(minHorizon, s.horizon*0.8)
			s.mu.Unlock()
			continue // delete again with the shorter horizon
		case over:
			s.full = true
		case used < s.opts.MaxBytes*8/10:
			s.full = false
			s.horizon = min(1, s.horizon*1.25)
		default:
			s.full = false
		}
		res.Horizon, res.Full = s.horizon, s.full
		s.mu.Unlock()
		break
	}
	if res.Horizon < 1 || res.Full {
		s.log.Warn("metrics storage cap reached: retention shortened", "used_bytes", res.UsedBytes, "max_bytes", s.opts.MaxBytes,
			"horizon", res.Horizon, "refusing_new_series", res.Full)
	}
	n, err := s.deleteEmptySeries(ctx)
	if err != nil {
		return res, err
	}
	res.Series = n
	if err := s.vacuum(ctx); err != nil {
		return res, err
	}
	if res.UsedBytes, err = s.Size(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// deleteExpired deletes, per series and level, the rows older than the
// effective retention (index range deletes on (series_id, ts)).
func (s *Store) deleteExpired(ctx context.Context) (int64, error) {
	now := s.clk.Now()
	type ser struct {
		id   int64
		kind string
	}
	s.mu.Lock()
	all := make([]ser, 0, len(s.series))
	for k, id := range s.series {
		all = append(all, ser{id, k.kind})
	}
	s.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })
	var cut [3]int64
	for i, l := range levels {
		cut[i] = align(now.Add(-s.retention(l.name)), l.res)
	}
	var deleted int64
	for start := 0; start < len(all); start += retainChunk {
		chunk := all[start:min(start+retainChunk, len(all))]
		err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			for _, sr := range chunk {
				for i, l := range levels {
					r, err := tx.ExecContext(ctx, "DELETE FROM "+table(sr.kind, l.name)+" WHERE series_id = ? AND ts < ?", sr.id, cut[i])
					if err != nil {
						return fmt.Errorf("metrics: retention: %w", err)
					}
					n, _ := r.RowsAffected()
					deleted += n
				}
			}
			return nil
		})
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// deleteEmptySeries removes series with no data left at any level.
func (s *Store) deleteEmptySeries(ctx context.Context) (int, error) {
	total := 0
	for _, k := range kindOrder {
		q := "DELETE FROM series WHERE kind = ?"
		for _, l := range levels {
			q += " AND NOT EXISTS (SELECT 1 FROM " + table(k, l.name) + " r WHERE r.series_id = series.id)"
		}
		r, err := s.db.ExecContext(ctx, q, k)
		if err != nil {
			return total, fmt.Errorf("metrics: delete empty series: %w", err)
		}
		n, _ := r.RowsAffected()
		total += int(n)
	}
	if total > 0 {
		s.mu.Lock()
		s.series = map[seriesKey]int64{}
		s.mu.Unlock()
		if err := s.loadSeries(ctx); err != nil {
			return total, err
		}
	}
	return total, nil
}

// vacuum returns free pages to the file system once they exceed a fifth of
// the file (incremental auto-vacuum).
func (s *Store) vacuum(ctx context.Context) error {
	var pages, free int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
		return err
	}
	if free == 0 || free*5 < pages {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA incremental_vacuum")
	if err != nil {
		return fmt.Errorf("metrics: incremental vacuum: %w", err)
	}
	for rows.Next() { //nolint:revive // drain: the pragma frees pages while stepping
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return rows.Err()
}

// Run performs rollups every minute and retention every ten minutes until
// ctx ends.
func (s *Store) Run(ctx context.Context) {
	s.maintain(ctx, true)
	for i := 1; ; i++ {
		t := s.clk.NewTimer(RollupInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		s.maintain(ctx, i%int(RetentionInterval/RollupInterval) == 0)
	}
}

func (s *Store) maintain(ctx context.Context, retain bool) {
	if err := s.Rollup(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("metrics rollup failed", "error", err)
	}
	if !retain {
		return
	}
	res, err := s.Retain(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Error("metrics retention failed", "error", err)
		return
	}
	if res.Deleted > 0 {
		s.log.Debug("metrics retention", "deleted_rows", res.Deleted, "deleted_series", res.Series, "used_bytes", res.UsedBytes)
	}
}
