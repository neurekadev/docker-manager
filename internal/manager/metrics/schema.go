package metrics

import (
	"strings"
	"time"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Storage levels.
const (
	LevelRaw     = "raw"
	LevelMinute  = "1m"
	LevelQuarter = "15m"
)

type level struct {
	name string
	res  time.Duration
}

var levels = []level{{LevelRaw, 10 * time.Second}, {LevelMinute, time.Minute}, {LevelQuarter, 15 * time.Minute}}

// rollupCol is one rollup column: its expression over the raw table and
// over the finer rollup table.
type rollupCol struct {
	name, fromRaw, fromRollup string
}

func avgCol(name, raw string) rollupCol {
	return rollupCol{name, "CAST(ROUND(AVG(" + raw + ")) AS INTEGER)", "CAST(ROUND(SUM(" + name + " * n) * 1.0 / NULLIF(SUM(CASE WHEN " +
		name + " IS NULL THEN 0 ELSE n END), 0)) AS INTEGER)"}
}

func maxCol(name, raw string) rollupCol {
	return rollupCol{name, "MAX(" + raw + ")", "MAX(" + name + ")"}
}

// metricDef maps an API metric key to storage columns.
type metricDef struct {
	key, unit string
	// raw is the raw column; roll the rollup column; max selects MAX
	// aggregation instead of the sample-weighted average.
	raw, roll string
	max       bool
	// scale converts the stored integer to the unit.
	scale float64
}

// kindSpec describes the tables of one series kind.
type kindSpec struct {
	kind    string
	rawCols []string
	rollup  []rollupCol
	metrics []metricDef
}

var flagsCol = rollupCol{"flags", "(MAX(flags & 1) | MAX(flags & 2) | MAX(flags & 4) | MAX(flags & 8))",
	"(MAX(flags & 1) | MAX(flags & 2) | MAX(flags & 4) | MAX(flags & 8))"}

var kinds = map[string]kindSpec{
	domain.MetricHost: {
		kind: domain.MetricHost,
		rawCols: []string{"cpu", "mem_used", "mem_total", "load1", "load5", "load15", "net_rx", "net_tx", "mem_cache", "mem_arc",
			"swap_used", "swap_total", "disk_r", "disk_w"},
		rollup: []rollupCol{flagsCol, avgCol("cpu_avg", "cpu"), maxCol("cpu_max", "cpu"), avgCol("mem_used_avg", "mem_used"),
			maxCol("mem_used_max", "mem_used"), maxCol("mem_total", "mem_total"), avgCol("load1", "load1"), avgCol("load5", "load5"),
			avgCol("load15", "load15"), avgCol("net_rx_avg", "net_rx"), maxCol("net_rx_max", "net_rx"), avgCol("net_tx_avg", "net_tx"),
			maxCol("net_tx_max", "net_tx"), avgCol("mem_cache", "mem_cache"), avgCol("mem_arc", "mem_arc"), avgCol("swap_used", "swap_used"),
			maxCol("swap_total", "swap_total"), avgCol("disk_r_avg", "disk_r"), maxCol("disk_r_max", "disk_r"), avgCol("disk_w_avg", "disk_w"),
			maxCol("disk_w_max", "disk_w")},
		metrics: []metricDef{
			{key: "cpu.percent", unit: "percent", raw: "cpu", roll: "cpu_avg", scale: 0.01},
			{key: "cpu.percent.max", unit: "percent", raw: "cpu", roll: "cpu_max", max: true, scale: 0.01},
			{key: "memory.used_bytes", unit: "bytes", raw: "mem_used", roll: "mem_used_avg", scale: 1},
			{key: "memory.used_bytes.max", unit: "bytes", raw: "mem_used", roll: "mem_used_max", max: true, scale: 1},
			{key: "memory.total_bytes", unit: "bytes", raw: "mem_total", roll: "mem_total", max: true, scale: 1},
			{key: "memory.cache_bytes", unit: "bytes", raw: "mem_cache", roll: "mem_cache", scale: 1},
			{key: "memory.zfs_arc_bytes", unit: "bytes", raw: "mem_arc", roll: "mem_arc", scale: 1},
			{key: "swap.used_bytes", unit: "bytes", raw: "swap_used", roll: "swap_used", scale: 1},
			{key: "swap.total_bytes", unit: "bytes", raw: "swap_total", roll: "swap_total", max: true, scale: 1},
			{key: "load.1", unit: "load", raw: "load1", roll: "load1", scale: 0.01},
			{key: "load.5", unit: "load", raw: "load5", roll: "load5", scale: 0.01},
			{key: "load.15", unit: "load", raw: "load15", roll: "load15", scale: 0.01},
			{key: "network.rx_bytes_per_second", unit: "bytes_per_second", raw: "net_rx", roll: "net_rx_avg", scale: 1},
			{key: "network.rx_bytes_per_second.max", unit: "bytes_per_second", raw: "net_rx", roll: "net_rx_max", max: true, scale: 1},
			{key: "network.tx_bytes_per_second", unit: "bytes_per_second", raw: "net_tx", roll: "net_tx_avg", scale: 1},
			{key: "network.tx_bytes_per_second.max", unit: "bytes_per_second", raw: "net_tx", roll: "net_tx_max", max: true, scale: 1},
			{key: "block.read_bytes_per_second", unit: "bytes_per_second", raw: "disk_r", roll: "disk_r_avg", scale: 1},
			{key: "block.read_bytes_per_second.max", unit: "bytes_per_second", raw: "disk_r", roll: "disk_r_max", max: true, scale: 1},
			{key: "block.write_bytes_per_second", unit: "bytes_per_second", raw: "disk_w", roll: "disk_w_avg", scale: 1},
			{key: "block.write_bytes_per_second.max", unit: "bytes_per_second", raw: "disk_w", roll: "disk_w_max", max: true, scale: 1},
		},
	},
	domain.MetricContainer: {
		kind:    domain.MetricContainer,
		rawCols: []string{"cpu", "mem", "mem_limit", "net_rx", "net_tx", "blk_r", "blk_w", "pids"},
		rollup: []rollupCol{flagsCol, avgCol("cpu_avg", "cpu"), maxCol("cpu_max", "cpu"), avgCol("mem_avg", "mem"), maxCol("mem_max", "mem"),
			maxCol("mem_limit", "mem_limit"), avgCol("net_rx", "net_rx"), avgCol("net_tx", "net_tx"), avgCol("blk_r", "blk_r"),
			avgCol("blk_w", "blk_w"), maxCol("pids", "pids")},
		metrics: []metricDef{
			{key: "cpu.percent", unit: "percent", raw: "cpu", roll: "cpu_avg", scale: 0.01},
			{key: "cpu.percent.max", unit: "percent", raw: "cpu", roll: "cpu_max", max: true, scale: 0.01},
			{key: "memory.used_bytes", unit: "bytes", raw: "mem", roll: "mem_avg", scale: 1},
			{key: "memory.used_bytes.max", unit: "bytes", raw: "mem", roll: "mem_max", max: true, scale: 1},
			{key: "memory.limit_bytes", unit: "bytes", raw: "mem_limit", roll: "mem_limit", max: true, scale: 1},
			{key: "network.rx_bytes_per_second", unit: "bytes_per_second", raw: "net_rx", roll: "net_rx", scale: 1},
			{key: "network.tx_bytes_per_second", unit: "bytes_per_second", raw: "net_tx", roll: "net_tx", scale: 1},
			{key: "block.read_bytes_per_second", unit: "bytes_per_second", raw: "blk_r", roll: "blk_r", scale: 1},
			{key: "block.write_bytes_per_second", unit: "bytes_per_second", raw: "blk_w", roll: "blk_w", scale: 1},
			{key: "pids", unit: "count", raw: "pids", roll: "pids", max: true, scale: 1},
		},
	},
	domain.MetricDisk: {
		kind:    domain.MetricDisk,
		rawCols: []string{"used", "total"},
		rollup:  []rollupCol{flagsCol, avgCol("used_avg", "used"), maxCol("used_max", "used"), maxCol("total", "total")},
		metrics: []metricDef{
			{key: "disk.used_bytes", unit: "bytes", raw: "used", roll: "used_avg", scale: 1},
			{key: "disk.total_bytes", unit: "bytes", raw: "total", roll: "total", max: true, scale: 1},
		},
	},
	// Temperature sensors (#146): hundredths of a degree Celsius.
	domain.MetricSensor: {
		kind:    domain.MetricSensor,
		rawCols: []string{"temp"},
		rollup:  []rollupCol{flagsCol, avgCol("temp_avg", "temp"), maxCol("temp_max", "temp")},
		metrics: []metricDef{
			{key: "temperature.celsius", unit: "celsius", raw: "temp", roll: "temp_avg", scale: 0.01},
			{key: "temperature.celsius.max", unit: "celsius", raw: "temp", roll: "temp_max", max: true, scale: 0.01},
		},
	},
}

// MetricKeys returns the queryable keys of a kind (host also covers the
// disk and sensor keys).
func MetricKeys(kind string) []string {
	var out []string
	add := func(k string) {
		for _, m := range kinds[k].metrics {
			out = append(out, m.key)
		}
	}
	add(kind)
	if kind == domain.MetricHost {
		add(domain.MetricDisk)
		add(domain.MetricSensor)
	}
	return out
}

func table(kind, lvl string) string { return kind + "_" + lvl }

// rollupSQL aggregates the finer level of kind into lvl for [from, to):
// arguments kind, from, to.
func rollupSQL(k kindSpec, lvl level, finer level) string {
	cols := []string{"series_id", "ts", "n"}
	sel := []string{"r.series_id", "(r.ts / " + secs(lvl.res) + ") * " + secs(lvl.res)}
	if finer.name == LevelRaw {
		sel = append(sel, "COUNT(*)")
	} else {
		sel = append(sel, "SUM(r.n)")
	}
	for _, c := range k.rollup {
		cols = append(cols, c.name)
		if finer.name == LevelRaw {
			sel = append(sel, c.fromRaw)
		} else {
			sel = append(sel, c.fromRollup)
		}
	}
	// CROSS JOIN makes series the outer loop so the (series_id, ts) key
	// serves each series' time range.
	return "INSERT OR REPLACE INTO " + table(k.kind, lvl.name) + " (" + strings.Join(cols, ", ") + ") SELECT " + strings.Join(sel, ", ") +
		" FROM series s CROSS JOIN " + table(k.kind, finer.name) + " r ON r.series_id = s.id AND r.ts >= ? AND r.ts < ?" +
		" WHERE s.kind = ? GROUP BY r.series_id, r.ts / " + secs(lvl.res)
}

func secs(d time.Duration) string {
	return itoa(int64(d / time.Second))
}
