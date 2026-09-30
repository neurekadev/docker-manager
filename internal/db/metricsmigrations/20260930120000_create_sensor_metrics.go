package metricsmigrations

func init() {
	// Host temperature sensors (#146, docs/internal/architecture/metrics.md):
	// a series kind sensor (one series per environment and sensor name,
	// such as "coretemp: Package id 0") with its raw, 1 min and 15 min
	// tables. temp is in hundredths of a degree Celsius; NULL is unknown.
	// SQLite cannot change a CHECK constraint, so series is rebuilt with
	// the new kind, keeping every ID (the sample tables refer to them).
	Migrations.MustRegister(
		Tx(Exec(
			seriesTable("series_new", "'host', 'container', 'disk', 'sensor'"),
			`INSERT INTO series_new (id, environment_id, kind, name, first_ts, last_ts)
				SELECT id, environment_id, kind, name, first_ts, last_ts FROM series`,
			`DROP TABLE series`,
			`ALTER TABLE series_new RENAME TO series`,
			`CREATE TABLE sensor_raw (
				series_id INTEGER NOT NULL,
				ts        INTEGER NOT NULL,
				flags     INTEGER NOT NULL DEFAULT 0,
				temp      INTEGER,
				PRIMARY KEY (series_id, ts)
			) STRICT, WITHOUT ROWID`,
			sensorRollup("sensor_1m"),
			sensorRollup("sensor_15m"),
		)),
		Tx(Exec(
			`DROP TABLE sensor_15m`, `DROP TABLE sensor_1m`, `DROP TABLE sensor_raw`,
			`DELETE FROM series WHERE kind = 'sensor'`,
			seriesTable("series_old", "'host', 'container', 'disk'"),
			`INSERT INTO series_old (id, environment_id, kind, name, first_ts, last_ts)
				SELECT id, environment_id, kind, name, first_ts, last_ts FROM series`,
			`DROP TABLE series`,
			`ALTER TABLE series_old RENAME TO series`,
		)),
	)
}

// seriesTable is the DDL of the series table with the given kinds (fixed
// text per migration).
func seriesTable(name, kinds string) string {
	return `CREATE TABLE ` + name + ` (
		id             INTEGER PRIMARY KEY,
		environment_id TEXT    NOT NULL CHECK (length(environment_id) BETWEEN 1 AND 64),
		kind           TEXT    NOT NULL CHECK (kind IN (` + kinds + `)),
		name           TEXT    NOT NULL CHECK (length(name) <= 255),
		first_ts       INTEGER NOT NULL,
		last_ts        INTEGER NOT NULL,
		UNIQUE (environment_id, kind, name)
	) STRICT`
}

func sensorRollup(name string) string {
	return `CREATE TABLE ` + name + ` (
		series_id INTEGER NOT NULL,
		ts        INTEGER NOT NULL,
		n         INTEGER NOT NULL,
		flags     INTEGER NOT NULL DEFAULT 0,
		temp_avg  INTEGER,
		temp_max  INTEGER,
		PRIMARY KEY (series_id, ts)
	) STRICT, WITHOUT ROWID`
}
