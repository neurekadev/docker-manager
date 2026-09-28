package migrations

func init() {
	// Job engine (#26): a retry (POST /jobs/{id}/retries) is a new job that
	// names the job it re-runs ("" = not a retry), and GET /jobs filters by
	// the policy that started a job (?policyId=), newest first.
	Migrations.MustRegister(
		Tx(Exec(
			`ALTER TABLE jobs ADD COLUMN retry_of TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX jobs_policy ON jobs (policy_id, id) WHERE policy_id IS NOT NULL`,
		)),
		Tx(Exec(
			`DROP INDEX jobs_policy`,
			`ALTER TABLE jobs DROP COLUMN retry_of`,
		)),
	)
}
