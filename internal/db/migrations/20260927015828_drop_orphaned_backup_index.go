package migrations

func init() {
	// Backups (#10): removing a repository now also removes its part of the
	// backup index. Repositories removed before that left snapshots (and
	// sets held only by them) behind that can no longer be browsed or
	// restored: drop them. Set members are domain.BackupSetMember JSON; a
	// set with a member of a remaining repository (or of none) stays. The
	// restic data at the destinations is not touched. Nothing to undo.
	Migrations.MustRegister(
		Tx(Exec(
			`DELETE FROM backup_snapshots WHERE repository_id NOT IN (SELECT id FROM backup_repositories)`,
			`DELETE FROM backup_sets
			 WHERE EXISTS (SELECT 1 FROM json_each(members))
			   AND NOT EXISTS (
			     SELECT 1 FROM json_each(members)
			     WHERE coalesce(json_extract(json_each.value, '$.RepositoryID'), '') = ''
			        OR json_extract(json_each.value, '$.RepositoryID') IN (SELECT id FROM backup_repositories))`,
		)),
		Tx(Exec()),
	)
}
