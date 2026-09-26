package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"code.neureka.dev/docker-manager/docker-manager/internal/domain"
)

func queryErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrMetricQuery, fmt.Sprintf(format, args...))
}

// plan is a resolved query: level, step and buckets.
type plan struct {
	lvl     level
	step    int64
	b0, end int64
	n       int
}

// Plan resolves the storage level and step of a range: the coarsest level
// whose resolution fits the step among those still holding data for
// from; the step is rounded up to a multiple of that resolution.
func (s *Store) plan(from, to time.Time, step time.Duration) (plan, error) {
	if !from.Before(to) {
		return plan{}, queryErr("from must be before to")
	}
	if step < 0 {
		return plan{}, queryErr("step must not be negative")
	}
	age := s.clk.Now().Sub(from)
	var covering []level
	for _, l := range levels {
		if age <= s.retention(l.name) {
			covering = append(covering, l)
		}
	}
	if len(covering) == 0 {
		covering = levels[len(levels)-1:]
	}
	finest := covering[0]
	span := to.Sub(from)
	if step == 0 {
		step = (span + DefaultPoints - 1) / DefaultPoints
	}
	step = max(step, finest.res)
	step = (step + finest.res - 1) / finest.res * finest.res
	lvl := finest
	for _, l := range covering {
		if l.res <= step {
			lvl = l
		}
	}
	res := int64(lvl.res / time.Second)
	st := (int64((step+time.Second-1)/time.Second) + res - 1) / res * res
	p := plan{lvl: lvl, step: st}
	p.b0 = floorDiv(from.Unix(), st) * st
	p.end = to.Unix()
	p.n = int((p.end - p.b0 + st - 1) / st)
	if p.n > MaxPoints {
		return plan{}, queryErr("the range needs %d points at a %d s step; at most %d are allowed (increase step)", p.n, st, MaxPoints)
	}
	return p, nil
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// bucketAgg accumulates one metric of one bucket across sources.
type bucketAgg struct {
	sum, cnt float64
	max      *float64
}

type source struct {
	lvl      level
	from, to int64
}

func (s *Store) sources(ctx context.Context, p plan) ([]source, error) {
	if p.lvl.name == LevelRaw {
		return []source{{levels[0], p.b0, p.end}}, nil
	}
	w1, err := s.Watermark(ctx, LevelMinute)
	if err != nil {
		return nil, err
	}
	m := w1.Unix()
	if p.lvl.name == LevelMinute {
		return []source{{levels[1], p.b0, min(p.end, m)}, {levels[0], max(p.b0, m), p.end}}, nil
	}
	w15, err := s.Watermark(ctx, LevelQuarter)
	if err != nil {
		return nil, err
	}
	q := min(w15.Unix(), m)
	return []source{{levels[2], p.b0, min(p.end, q)}, {levels[1], max(p.b0, q), min(p.end, m)}, {levels[0], max(p.b0, m), p.end}}, nil
}

// Query returns the requested series of one environment, one value per
// step bucket; a bucket without data is nil.
func (s *Store) Query(ctx context.Context, q domain.MetricQuery) (domain.MetricResult, error) {
	if q.Kind != domain.MetricHost && q.Kind != domain.MetricContainer {
		return domain.MetricResult{}, queryErr("unknown kind %q", q.Kind)
	}
	if q.Kind == domain.MetricContainer && q.Name == "" {
		return domain.MetricResult{}, queryErr("container name required")
	}
	now := s.clk.Now()
	if q.To.IsZero() {
		q.To = now
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-time.Hour)
	}
	keys := q.Keys
	if len(keys) == 0 {
		keys = MetricKeys(q.Kind)
	}
	known := MetricKeys(q.Kind)
	for _, k := range keys {
		if !slices.Contains(known, k) {
			return domain.MetricResult{}, queryErr("unknown metric %q", k)
		}
	}
	p, err := s.plan(q.From, q.To, q.Step)
	if err != nil {
		return domain.MetricResult{}, err
	}
	srcs, err := s.sources(ctx, p)
	if err != nil {
		return domain.MetricResult{}, err
	}
	out := domain.MetricResult{From: q.From.UTC(), To: q.To.UTC(), Step: time.Duration(p.step) * time.Second, Resolution: p.lvl.name,
		Timestamps: make([]time.Time, p.n), Series: []domain.MetricSeries{}}
	for i := range out.Timestamps {
		out.Timestamps[i] = time.Unix(p.b0+int64(i)*p.step, 0).UTC()
	}
	type target struct {
		kind, name, mount string
		defs              []metricDef
	}
	var targets []target
	pick := func(kind string) []metricDef {
		var defs []metricDef
		for _, k := range keys {
			for _, m := range kinds[kind].metrics {
				if m.key == k {
					defs = append(defs, m)
				}
			}
		}
		return defs
	}
	if q.Kind == domain.MetricContainer {
		targets = append(targets, target{kind: domain.MetricContainer, name: q.Name, defs: pick(domain.MetricContainer)})
	} else {
		targets = append(targets, target{kind: domain.MetricHost, defs: pick(domain.MetricHost)})
		if defs := pick(domain.MetricDisk); len(defs) > 0 {
			for _, m := range s.names(q.EnvironmentID, domain.MetricDisk) {
				targets = append(targets, target{kind: domain.MetricDisk, name: m, mount: m, defs: defs})
			}
		}
	}
	for _, t := range targets {
		if len(t.defs) == 0 {
			continue
		}
		aggs := make([][]bucketAgg, len(t.defs))
		for i := range aggs {
			aggs[i] = make([]bucketAgg, p.n)
		}
		s.mu.Lock()
		id, ok := s.series[seriesKey{q.EnvironmentID, t.kind, t.name}]
		s.mu.Unlock()
		if ok {
			for _, src := range srcs {
				if src.from >= src.to {
					continue
				}
				flags, err := s.aggregate(ctx, t.kind, id, src, p, t.defs, aggs)
				if err != nil {
					return domain.MetricResult{}, err
				}
				out.Flags |= flags
			}
		}
		for i, d := range t.defs {
			ser := domain.MetricSeries{Key: d.key, Unit: d.unit, Mount: t.mount, Values: make([]*float64, p.n)}
			for b, a := range aggs[i] {
				switch {
				case d.max && a.max != nil:
					v := *a.max * d.scale
					ser.Values[b] = &v
				case !d.max && a.cnt > 0:
					v := a.sum / a.cnt * d.scale
					ser.Values[b] = &v
				}
			}
			out.Series = append(out.Series, ser)
		}
	}
	return out, nil
}

// aggregate adds one source's buckets of one series into aggs.
func (s *Store) aggregate(ctx context.Context, kind string, id int64, src source, p plan, defs []metricDef, aggs [][]bucketAgg) (int, error) {
	raw := src.lvl.name == LevelRaw
	cols := []string{"ts / " + itoa(p.step), "(MAX(flags & 1) | MAX(flags & 2) | MAX(flags & 4) | MAX(flags & 8))"}
	for _, d := range defs {
		switch {
		case raw && d.max:
			cols = append(cols, "MAX("+d.raw+")", "0")
		case raw:
			cols = append(cols, "SUM("+d.raw+")", "COUNT("+d.raw+")")
		case d.max:
			cols = append(cols, "MAX("+d.roll+")", "0")
		default:
			cols = append(cols, "SUM("+d.roll+" * n)", "SUM(CASE WHEN "+d.roll+" IS NULL THEN 0 ELSE n END)")
		}
	}
	// Only fixed column and table names of the schema are concatenated;
	// values are bound parameters.
	q := "SELECT " + strings.Join(cols, ", ") + " FROM " + table(kind, src.lvl.name) + //nolint:gosec // G202: identifiers from the schema tables above
		" WHERE series_id = ? AND ts >= ? AND ts < ? GROUP BY ts / " + itoa(p.step)
	rows, err := s.read.QueryContext(ctx, q, id, src.from, src.to)
	if err != nil {
		return 0, fmt.Errorf("metrics: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	flags := 0
	for rows.Next() {
		var b int64
		var f sql.NullInt64
		vals := make([]sql.NullFloat64, 2*len(defs))
		dst := []any{&b, &f}
		for i := range vals {
			dst = append(dst, &vals[i])
		}
		if err := rows.Scan(dst...); err != nil {
			return 0, err
		}
		flags |= int(f.Int64)
		i := int(b - p.b0/p.step)
		if i < 0 || i >= p.n {
			continue
		}
		for j, d := range defs {
			v1, v2 := vals[2*j], vals[2*j+1]
			a := &aggs[j][i]
			if d.max {
				if v1.Valid && (a.max == nil || v1.Float64 > *a.max) {
					x := v1.Float64
					a.max = &x
				}
				continue
			}
			if v1.Valid && v2.Valid && v2.Float64 > 0 {
				a.sum += v1.Float64
				a.cnt += v2.Float64
			}
		}
	}
	return flags, rows.Err()
}

// names lists the series names of a kind in an environment (sorted: the
// Docker filesystem first for disks).
func (s *Store) names(env, kind string) []string {
	s.mu.Lock()
	var out []string
	for k := range s.series {
		if k.env == env && k.kind == kind {
			out = append(out, k.name)
		}
	}
	s.mu.Unlock()
	rank := func(n string) int {
		switch n {
		case "docker":
			return 0
		case "stacks":
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// Latest returns an environment's most recent host sample and the latest
// usage of each of its filesystems (ok false: no sample yet).
func (s *Store) Latest(ctx context.Context, env string) (domain.LatestMetrics, bool, error) {
	var out domain.LatestMetrics
	s.mu.Lock()
	id, ok := s.series[seriesKey{env, domain.MetricHost, ""}]
	s.mu.Unlock()
	if !ok {
		return out, false, nil
	}
	var ts, flags int64
	var cpu, used, total, l1, l5, l15, rx, tx sql.NullInt64
	err := s.read.QueryRowContext(ctx, `SELECT ts, flags, cpu, mem_used, mem_total, load1, load5, load15, net_rx, net_tx FROM host_raw
		WHERE series_id = ? ORDER BY ts DESC LIMIT 1`, id).Scan(&ts, &flags, &cpu, &used, &total, &l1, &l5, &l15, &rx, &tx)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, fmt.Errorf("metrics: latest: %w", err)
	}
	out.At, out.Flags = time.Unix(ts, 0).UTC(), int(flags)
	out.Host = domain.HostValues{CPUPercent: fscale(cpu, 0.01), MemoryUsedBytes: iptr(used), MemoryTotalBytes: iptr(total),
		Load1: fscale(l1, 0.01), Load5: fscale(l5, 0.01), Load15: fscale(l15, 0.01), NetworkRxBPS: fscale(rx, 1), NetworkTxBPS: fscale(tx, 1)}
	for _, m := range s.names(env, domain.MetricDisk) {
		s.mu.Lock()
		did := s.series[seriesKey{env, domain.MetricDisk, m}]
		s.mu.Unlock()
		var u, t sql.NullInt64
		var dts int64
		err := s.read.QueryRowContext(ctx, `SELECT ts, used, total FROM disk_raw WHERE series_id = ? ORDER BY ts DESC LIMIT 1`, did).Scan(&dts, &u, &t)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, false, fmt.Errorf("metrics: latest disk: %w", err)
		}
		// Only filesystems reported with the latest host sample are current.
		if dts < ts-int64(5*time.Minute/time.Second) {
			continue
		}
		out.Disks = append(out.Disks, domain.DiskValues{Mount: m, UsedBytes: u.Int64, TotalBytes: t.Int64})
	}
	return out, true, nil
}

func fscale(v sql.NullInt64, f float64) *float64 {
	if !v.Valid {
		return nil
	}
	x := float64(v.Int64) * f
	return &x
}

func iptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}
