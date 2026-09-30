package metrics

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/uptrace/bun"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// RawResolution is the sample slot width.
const RawResolution = 10 * time.Second

// IngestResult counts what Ingest did.
type IngestResult struct {
	// Inserted samples (host, disk and container rows).
	Inserted int
	// Duplicates were already stored (same series and slot).
	Duplicates int
	// Expired samples were older than the raw retention.
	Expired int
	// Refused container/disk series exceeded the series or storage cap.
	Refused int
	// Containers lists the container names with an inserted sample.
	Containers []string
	// Host reports whether a host sample was inserted.
	Host bool
}

func scaled(v *float64, factor float64) any {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	return int64(math.Round(*v * factor))
}

func intv(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// Ingest stores samples of an environment and, when cursor is set,
// advances its collector cursor in the same transaction. Samples are keyed
// by (series, 10 s slot): a repeated delivery is ignored, never counted
// twice. Samples older than the raw retention are dropped; samples older
// than a rollup watermark move the watermark back so the rollup is
// recomputed.
func (s *Store) Ingest(ctx context.Context, envID string, samples []domain.MetricSample, cursor *domain.MetricCursor) (IngestResult, error) {
	var res IngestResult
	now := s.clk.Now()
	oldest := align(now.Add(-s.retention(LevelRaw)), RawResolution)
	minTS := int64(math.MaxInt64)
	seenCtr := map[string]bool{}
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, smp := range samples {
			ts := align(smp.At, RawResolution)
			if ts < oldest {
				res.Expired++
				continue
			}
			if h := smp.Host; h != nil {
				id, err := s.seriesID(ctx, tx, envID, domain.MetricHost, "", ts, true)
				if err != nil {
					return err
				}
				n, err := exec(ctx, tx, `INSERT OR IGNORE INTO host_raw (series_id, ts, flags, cpu, mem_used, mem_total, load1, load5, load15, net_rx, net_tx)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, ts, smp.Flags, scaled(h.CPUPercent, 100), intv(h.MemoryUsedBytes),
					intv(h.MemoryTotalBytes), scaled(h.Load1, 100), scaled(h.Load5, 100), scaled(h.Load15, 100), scaled(h.NetworkRxBPS, 1),
					scaled(h.NetworkTxBPS, 1))
				if err != nil {
					return err
				}
				res.count(n)
				if n > 0 {
					res.Host = true
					minTS = min(minTS, ts)
				}
			}
			for _, d := range smp.Disks {
				id, err := s.seriesID(ctx, tx, envID, domain.MetricDisk, d.Mount, ts, false)
				if err != nil {
					return err
				}
				if id == 0 {
					res.Refused++
					continue
				}
				n, err := exec(ctx, tx, `INSERT OR IGNORE INTO disk_raw (series_id, ts, flags, used, total) VALUES (?, ?, ?, ?, ?)`,
					id, ts, smp.Flags, d.UsedBytes, d.TotalBytes)
				if err != nil {
					return err
				}
				res.count(n)
				if n > 0 {
					minTS = min(minTS, ts)
				}
			}
			for _, c := range smp.Containers {
				id, err := s.seriesID(ctx, tx, envID, domain.MetricContainer, c.Name, ts, false)
				if err != nil {
					return err
				}
				if id == 0 {
					res.Refused++
					continue
				}
				n, err := exec(ctx, tx, `INSERT OR IGNORE INTO container_raw (series_id, ts, flags, cpu, mem, mem_limit, net_rx, net_tx, blk_r, blk_w, pids)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, ts, smp.Flags, scaled(c.CPUPercent, 100), intv(c.MemoryBytes),
					intv(c.MemoryLimitBytes), scaled(c.NetworkRxBPS, 1), scaled(c.NetworkTxBPS, 1), scaled(c.BlockReadBPS, 1),
					scaled(c.BlockWriteBPS, 1), intv(c.PIDs))
				if err != nil {
					return err
				}
				res.count(n)
				if n > 0 {
					minTS = min(minTS, ts)
					if !seenCtr[c.Name] {
						seenCtr[c.Name] = true
						res.Containers = append(res.Containers, c.Name)
					}
				}
			}
		}
		if minTS != math.MaxInt64 {
			// Late samples: recompute the rollups that already covered them.
			for _, l := range levels[1:] {
				if _, err := tx.ExecContext(ctx, `UPDATE rollup_state SET done_until = ? WHERE level = ? AND done_until > ?`,
					align(time.Unix(minTS, 0), l.res), l.name, minTS); err != nil {
					return fmt.Errorf("metrics: move rollup watermark: %w", err)
				}
			}
		}
		if cursor != nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO collector_state (environment_id, epoch, last_seq, skew_ms, updated_at) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (environment_id) DO UPDATE SET epoch = excluded.epoch, last_seq = excluded.last_seq, skew_ms = excluded.skew_ms,
				updated_at = excluded.updated_at`,
				envID, cursor.Epoch, int64(min(cursor.LastSeq, math.MaxInt64)), cursor.Skew.Milliseconds(), now.UTC().Format(time.RFC3339Nano)); err != nil { //nolint:gosec // G115: clamped
				return fmt.Errorf("metrics: save cursor: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		// The in-memory series cache may hold IDs of rolled-back inserts.
		s.mu.Lock()
		s.series = map[seriesKey]int64{}
		s.mu.Unlock()
		if lerr := s.loadSeries(context.WithoutCancel(ctx)); lerr != nil {
			s.log.Error("metrics: reloading series after a failed ingest", "error", lerr)
		}
		return IngestResult{}, err
	}
	return res, nil
}

func (r *IngestResult) count(n int64) {
	if n > 0 {
		r.Inserted++
	} else {
		r.Duplicates++
	}
}

func exec(ctx context.Context, tx bun.Tx, q string, args ...any) (int64, error) {
	r, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("metrics: insert sample: %w", err)
	}
	return r.RowsAffected()
}

// seriesID returns (creating) the ID of a series. Beyond the series cap,
// or while the storage cap is reached, new non-host series are refused
// (ID 0); host series are always accepted (one per environment).
func (s *Store) seriesID(ctx context.Context, tx bun.Tx, env, kind, name string, ts int64, always bool) (int64, error) {
	k := seriesKey{env, kind, name}
	s.mu.Lock()
	id, ok := s.series[k]
	refuse := !always && (s.full || s.nSer >= s.opts.MaxSeries)
	s.mu.Unlock()
	if ok {
		return id, nil
	}
	if refuse {
		return 0, nil
	}
	err := tx.QueryRowContext(ctx, `INSERT INTO series (environment_id, kind, name, first_ts, last_ts) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (environment_id, kind, name) DO UPDATE SET last_ts = max(last_ts, excluded.last_ts) RETURNING id`,
		env, kind, name, ts, ts).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("metrics: create series: %w", err)
	}
	s.mu.Lock()
	s.series[k] = id
	s.nSer = len(s.series)
	s.mu.Unlock()
	return id, nil
}

// retention is the effective retention of a level (scaled down while over
// the storage cap).
func (s *Store) retention(lvl string) time.Duration {
	s.mu.Lock()
	h := s.horizon
	s.mu.Unlock()
	var d time.Duration
	switch lvl {
	case LevelRaw:
		d = s.opts.Retention.Raw
	case LevelMinute:
		d = s.opts.Retention.Minute
	default:
		d = s.opts.Retention.Quarter
	}
	return time.Duration(float64(d) * h)
}
