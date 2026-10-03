package migrations

// localRepos selects the local backup repositories.
const localRepos = `(SELECT id FROM backup_repositories WHERE kind = 'local')`

// firstS3Repo selects the oldest S3 backup repository.
const firstS3Repo = `(SELECT id FROM backup_repositories WHERE kind = 's3' ORDER BY created_at, id LIMIT 1)`

// droppedRuleScope matches the permission rules on what the migration
// removes: local repositories, their backups and the policies deleted with
// them (policies are deleted first; the ids are read from what remains).
const droppedRuleScope = `scope_kind = 'resource' AND (
	(resource_type = 'backup_repository' AND resource_id IN ` + localRepos + `)
	OR (resource_type = 'backup' AND resource_id IN (SELECT id FROM backup_snapshots WHERE repository_id IN ` + localRepos + `))
	OR (resource_type = 'backup_policy' AND resource_id NOT IN (SELECT id FROM backup_policies)))`

func init() {
	// Backups go to S3-compatible storage only (#244). Local repositories
	// are removed the way removing one in the app does; the restic data in
	// their directories is not touched:
	//
	//   - per-environment repository choices of a local repository go (the
	//     policy's repository is used there);
	//   - a policy writing to a local repository moves to the oldest S3
	//     repository and is disabled, so nothing runs before someone looks
	//     at it; with no S3 repository it is deleted (its sets and
	//     backups of other repositories stay in the index);
	//   - permission rules on the removed repositories, their backups and
	//     the deleted policies go;
	//   - the storage of a removed repository stops counting (a zero
	//     sample); its locations, its backups and the sets held only by
	//     it leave the index.
	//
	// The executor and path columns stay (empty): rebuilding the table is
	// not worth it. Nothing to undo.
	Migrations.MustRegister(
		Tx(Exec(
			`UPDATE backup_policies
			 SET environment_repos = (SELECT json_group_object(key, value) FROM json_each(backup_policies.environment_repos)
				WHERE value NOT IN `+localRepos+`)
			 WHERE EXISTS (SELECT 1 FROM json_each(backup_policies.environment_repos) WHERE value IN `+localRepos+`)`,
			// SQLite's clock only because a migration has no injected clock.
			`UPDATE backup_policies
			 SET repository_id = `+firstS3Repo+`, enabled = 0, revision = revision + 1,
				updated_at = strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')
			 WHERE repository_id IN `+localRepos+` AND `+firstS3Repo+` IS NOT NULL`,
			`DELETE FROM backup_policies WHERE repository_id IN `+localRepos,
			`UPDATE groups SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT group_id FROM group_permission_rules WHERE `+droppedRuleScope+`)`,
			`UPDATE users SET permissions_revision = permissions_revision + 1
			 WHERE id IN (SELECT user_id FROM user_permission_rules WHERE `+droppedRuleScope+`)`,
			`DELETE FROM group_permission_rules WHERE `+droppedRuleScope,
			`DELETE FROM user_permission_rules WHERE `+droppedRuleScope,
			`INSERT INTO backup_storage_samples (repository_id, scope, hour, at, size_bytes, uncompressed_bytes)
			 SELECT DISTINCT repository_id, scope, CAST(strftime('%s', 'now') AS INTEGER) / 3600,
				strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'), 0, 0
			 FROM backup_storage_samples WHERE repository_id IN `+localRepos+`
			 ON CONFLICT (repository_id, scope, hour) DO UPDATE
			 SET at = excluded.at, size_bytes = 0, uncompressed_bytes = 0`,
			`DELETE FROM backup_snapshots WHERE repository_id IN `+localRepos,
			// Members are domain.BackupSetMember JSON; a set with a member
			// of another repository (or of none) stays.
			`DELETE FROM backup_sets
			 WHERE EXISTS (SELECT 1 FROM json_each(members))
			   AND NOT EXISTS (SELECT 1 FROM json_each(members)
			     WHERE coalesce(json_extract(json_each.value, '$.RepositoryID'), '') NOT IN `+localRepos+`)`,
			`DELETE FROM backup_locations WHERE repository_id IN `+localRepos,
			`DELETE FROM backup_repositories WHERE kind = 'local'`,
		)),
		Tx(Exec()),
	)
}
