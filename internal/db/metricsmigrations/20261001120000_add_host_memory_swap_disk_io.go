package metricsmigrations

func init() {
	// Host memory breakdown, swap and disk throughput
	// (docs/internal/architecture/metrics.md): the buffers and page cache
	// and the ZFS ARC (not part of mem_used), swap in use and configured
	// (bytes), and the bytes per second read and written by the host's
	// disks. NULL is unknown (older agents, a host without ZFS).
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE host_raw ADD COLUMN mem_cache INTEGER`,
			`ALTER TABLE host_raw ADD COLUMN mem_arc INTEGER`,
			`ALTER TABLE host_raw ADD COLUMN swap_used INTEGER`,
			`ALTER TABLE host_raw ADD COLUMN swap_total INTEGER`,
			`ALTER TABLE host_raw ADD COLUMN disk_r INTEGER`,
			`ALTER TABLE host_raw ADD COLUMN disk_w INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN mem_cache INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN mem_arc INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN swap_used INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN swap_total INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN disk_r_avg INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN disk_r_max INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN disk_w_avg INTEGER`,
			`ALTER TABLE host_1m ADD COLUMN disk_w_max INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN mem_cache INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN mem_arc INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN swap_used INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN swap_total INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN disk_r_avg INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN disk_r_max INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN disk_w_avg INTEGER`,
			`ALTER TABLE host_15m ADD COLUMN disk_w_max INTEGER`,
		)),
		Tx(Exec(
			`ALTER TABLE host_15m DROP COLUMN disk_w_max`,
			`ALTER TABLE host_15m DROP COLUMN disk_w_avg`,
			`ALTER TABLE host_15m DROP COLUMN disk_r_max`,
			`ALTER TABLE host_15m DROP COLUMN disk_r_avg`,
			`ALTER TABLE host_15m DROP COLUMN swap_total`,
			`ALTER TABLE host_15m DROP COLUMN swap_used`,
			`ALTER TABLE host_15m DROP COLUMN mem_arc`,
			`ALTER TABLE host_15m DROP COLUMN mem_cache`,
			`ALTER TABLE host_1m DROP COLUMN disk_w_max`,
			`ALTER TABLE host_1m DROP COLUMN disk_w_avg`,
			`ALTER TABLE host_1m DROP COLUMN disk_r_max`,
			`ALTER TABLE host_1m DROP COLUMN disk_r_avg`,
			`ALTER TABLE host_1m DROP COLUMN swap_total`,
			`ALTER TABLE host_1m DROP COLUMN swap_used`,
			`ALTER TABLE host_1m DROP COLUMN mem_arc`,
			`ALTER TABLE host_1m DROP COLUMN mem_cache`,
			`ALTER TABLE host_raw DROP COLUMN disk_w`,
			`ALTER TABLE host_raw DROP COLUMN disk_r`,
			`ALTER TABLE host_raw DROP COLUMN swap_total`,
			`ALTER TABLE host_raw DROP COLUMN swap_used`,
			`ALTER TABLE host_raw DROP COLUMN mem_arc`,
			`ALTER TABLE host_raw DROP COLUMN mem_cache`,
		)),
	)
}
