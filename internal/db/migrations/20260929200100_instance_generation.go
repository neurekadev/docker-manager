package migrations

func init() {
	// The instance's generation (docs/internal/architecture/manager-move.md):
	// every copy a manager hands to a new server carries it raised by one;
	// agents refuse managers with a lower generation than they have seen.
	Migrations.MustRegister(
		Tx(Exec(`ALTER TABLE instance ADD COLUMN generation INTEGER NOT NULL DEFAULT 1`)),
		Tx(Exec(`ALTER TABLE instance DROP COLUMN generation`)),
	)
}
