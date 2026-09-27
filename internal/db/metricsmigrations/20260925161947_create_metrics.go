package metricsmigrations

func init() {
	// Metrics storage (#5, docs/internal/architecture/metrics.md).
	//
	//   - series maps (environment, kind, name) to a small integer ID so the
	//     sample tables stay narrow; kind is host (name ''), container
	//     (the container name) or disk (a mount role such as docker).
	//   - <kind>_raw holds 10 s samples (ts = unix seconds of the 10 s
	//     slot), <kind>_1m and <kind>_15m the rollups (ts = bucket start,
	//     n = samples aggregated). The primary key (series_id, ts) makes
	//     ingestion idempotent (INSERT OR IGNORE) and serves per-series range
	//     queries; WITHOUT ROWID stores each row once.
	//   - Values are integers in fixed units: CPU and load in hundredths
	//     (percent, load average), memory and disk in bytes, rates in bytes
	//     per second. NULL is "unknown" (a gap), never zero.
	//   - flags: 1 = timestamp corrected for agent clock skew, 2 = clamped
	//     to the manager's receive time, 4 = container list incomplete,
	//     8 = Engine unavailable during the sample.
	//   - collector_state is the per-environment fetch cursor (agent sampler
	//     epoch and last sequence) and the clock-skew estimate;
	//     rollup_state the rollup watermarks; inventory the last Engine
	//     inventory per environment.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE series (
				id             INTEGER PRIMARY KEY,
				environment_id TEXT    NOT NULL CHECK (length(environment_id) BETWEEN 1 AND 64),
				kind           TEXT    NOT NULL CHECK (kind IN ('host', 'container', 'disk')),
				name           TEXT    NOT NULL CHECK (length(name) <= 255),
				first_ts       INTEGER NOT NULL,
				last_ts        INTEGER NOT NULL,
				UNIQUE (environment_id, kind, name)
			) STRICT`,
			`CREATE TABLE host_raw (
				series_id INTEGER NOT NULL,
				ts        INTEGER NOT NULL,
				flags     INTEGER NOT NULL DEFAULT 0,
				cpu       INTEGER,
				mem_used  INTEGER,
				mem_total INTEGER,
				load1     INTEGER,
				load5     INTEGER,
				load15    INTEGER,
				net_rx    INTEGER,
				net_tx    INTEGER,
				PRIMARY KEY (series_id, ts)
			) STRICT, WITHOUT ROWID`,
			hostRollup("host_1m"),
			hostRollup("host_15m"),
			`CREATE TABLE container_raw (
				series_id INTEGER NOT NULL,
				ts        INTEGER NOT NULL,
				flags     INTEGER NOT NULL DEFAULT 0,
				cpu       INTEGER,
				mem       INTEGER,
				mem_limit INTEGER,
				net_rx    INTEGER,
				net_tx    INTEGER,
				blk_r     INTEGER,
				blk_w     INTEGER,
				pids      INTEGER,
				PRIMARY KEY (series_id, ts)
			) STRICT, WITHOUT ROWID`,
			containerRollup("container_1m"),
			containerRollup("container_15m"),
			`CREATE TABLE disk_raw (
				series_id INTEGER NOT NULL,
				ts        INTEGER NOT NULL,
				flags     INTEGER NOT NULL DEFAULT 0,
				used      INTEGER,
				total     INTEGER,
				PRIMARY KEY (series_id, ts)
			) STRICT, WITHOUT ROWID`,
			diskRollup("disk_1m"),
			diskRollup("disk_15m"),
			`CREATE TABLE collector_state (
				environment_id TEXT    PRIMARY KEY,
				epoch          TEXT    NOT NULL,
				last_seq       INTEGER NOT NULL CHECK (last_seq >= 0),
				skew_ms        INTEGER NOT NULL DEFAULT 0,
				updated_at     TEXT    NOT NULL
			) STRICT`,
			`CREATE TABLE rollup_state (
				level      TEXT    PRIMARY KEY CHECK (level IN ('1m', '15m')),
				done_until INTEGER NOT NULL
			) STRICT`,
			`CREATE TABLE inventory (
				environment_id TEXT PRIMARY KEY,
				data           TEXT NOT NULL CHECK (json_valid(data)),
				collected_at   TEXT NOT NULL,
				received_at    TEXT NOT NULL
			) STRICT`,
		)),
		Tx(Exec(
			`DROP TABLE inventory`, `DROP TABLE rollup_state`, `DROP TABLE collector_state`,
			`DROP TABLE disk_15m`, `DROP TABLE disk_1m`, `DROP TABLE disk_raw`,
			`DROP TABLE container_15m`, `DROP TABLE container_1m`, `DROP TABLE container_raw`,
			`DROP TABLE host_15m`, `DROP TABLE host_1m`, `DROP TABLE host_raw`, `DROP TABLE series`,
		)),
	)
}

// The rollup tables of one kind share a layout; these build their DDL
// (fixed text: the migration never changes when code changes).

func hostRollup(name string) string {
	return `CREATE TABLE ` + name + ` (
		series_id    INTEGER NOT NULL,
		ts           INTEGER NOT NULL,
		n            INTEGER NOT NULL,
		flags        INTEGER NOT NULL DEFAULT 0,
		cpu_avg      INTEGER,
		cpu_max      INTEGER,
		mem_used_avg INTEGER,
		mem_used_max INTEGER,
		mem_total    INTEGER,
		load1        INTEGER,
		load5        INTEGER,
		load15       INTEGER,
		net_rx_avg   INTEGER,
		net_rx_max   INTEGER,
		net_tx_avg   INTEGER,
		net_tx_max   INTEGER,
		PRIMARY KEY (series_id, ts)
	) STRICT, WITHOUT ROWID`
}

func containerRollup(name string) string {
	return `CREATE TABLE ` + name + ` (
		series_id INTEGER NOT NULL,
		ts        INTEGER NOT NULL,
		n         INTEGER NOT NULL,
		flags     INTEGER NOT NULL DEFAULT 0,
		cpu_avg   INTEGER,
		cpu_max   INTEGER,
		mem_avg   INTEGER,
		mem_max   INTEGER,
		mem_limit INTEGER,
		net_rx    INTEGER,
		net_tx    INTEGER,
		blk_r     INTEGER,
		blk_w     INTEGER,
		pids      INTEGER,
		PRIMARY KEY (series_id, ts)
	) STRICT, WITHOUT ROWID`
}

func diskRollup(name string) string {
	return `CREATE TABLE ` + name + ` (
		series_id INTEGER NOT NULL,
		ts        INTEGER NOT NULL,
		n         INTEGER NOT NULL,
		flags     INTEGER NOT NULL DEFAULT 0,
		used_avg  INTEGER,
		used_max  INTEGER,
		total     INTEGER,
		PRIMARY KEY (series_id, ts)
	) STRICT, WITHOUT ROWID`
}
