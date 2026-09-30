package metricsmigrations

func init() {
	// Disk health (#143, docs/internal/architecture/metrics.md, "Host
	// health"): the last host.health answer per environment (SMART
	// devices, md arrays, ZFS pools) as the agent's JSON document, written
	// when it changes and served while the environment is offline.
	// collected_at is the agent's clock, received_at the manager's.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE host_health (
				environment_id TEXT PRIMARY KEY,
				data           TEXT NOT NULL CHECK (json_valid(data)),
				collected_at   TEXT NOT NULL,
				received_at    TEXT NOT NULL
			) STRICT`,
		)),
		Tx(Exec(`DROP TABLE host_health`)),
	)
}
