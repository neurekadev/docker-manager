package migrations

func init() {
	// Updates (#20): when the candidate image was created, from the image
	// config ("created") of the candidate's host-platform manifest. Shown
	// next to the new version only; never used to decide an update. NULL
	// while unknown (not fetched yet, not recorded or not available).
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE update_candidates ADD COLUMN candidate_published_at TEXT`,
		)),
		Tx(Exec(
			`ALTER TABLE update_candidates DROP COLUMN candidate_published_at`,
		)),
	)
}
