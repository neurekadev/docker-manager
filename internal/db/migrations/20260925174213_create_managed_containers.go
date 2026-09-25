package migrations

func init() {
	// Recreate specifications of DockYard-managed standalone containers
	// (#6): the create-container form a container was created from, so
	// automatic updates (#20) can recreate it with its tagged image and prior
	// running state. The container carries the row's ID in its
	// dev.neureka.dockyard.spec label; (environment, name) is not unique
	// because a failed or removed container's row may remain until the
	// environment's next reconciliation. The spec holds environment values
	// and is sealed (secrets.Keyring) under managed_containers/<id>/spec.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE managed_containers (
				id             TEXT    PRIMARY KEY,
				environment_id TEXT    NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
				name           TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
				create_job_id  TEXT    NOT NULL,
				spec           TEXT    NOT NULL,
				start          INTEGER NOT NULL CHECK (start IN (0, 1)),
				revision       INTEGER NOT NULL CHECK (revision >= 1),
				created_at     TEXT    NOT NULL,
				updated_at     TEXT    NOT NULL
			) STRICT`,
			`CREATE INDEX managed_containers_env_name ON managed_containers (environment_id, name)`,
		)),
		Tx(Exec(
			`DROP TABLE managed_containers`,
		)),
	)
}
