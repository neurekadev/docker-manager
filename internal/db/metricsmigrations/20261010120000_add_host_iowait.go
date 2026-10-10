package metricsmigrations

func init() {
	// Host I/O wait (docs/internal/architecture/metrics.md): the share of
	// CPU time the cores were idle with disk I/O outstanding, in hundredths
	// of a percent like cpu. NULL is unknown (older agents).
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE host_raw ADD COLUMN iowait INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN iowait_avg INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN iowait_max INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN iowait_avg INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN iowait_max INTEGER`,
		)),
		Tx(Exec(
			`ALTER TABLE host_15m DROP COLUMN iowait_max`,
			`ALTER TABLE host_15m DROP COLUMN iowait_avg`,
			`ALTER TABLE host_1m DROP COLUMN iowait_max`,
			`ALTER TABLE host_1m DROP COLUMN iowait_avg`,
			`ALTER TABLE host_raw DROP COLUMN iowait`,
		)),
	)
}
