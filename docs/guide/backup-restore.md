# Backup and restore

DockYard backs up with [restic](https://restic.net) (0.19.1, shipped in
both images) to local directories or S3-compatible storage. A backup can
hold DockYard's own state (the manager database and its secret key),
stacks (their project directory: Compose files, `.env` and relative bind
directories next to `compose.yaml`) and volumes. Reference:
[architecture/backups.md](../architecture/backups.md).

## The Recovery Key

One **Recovery Key** per DockYard instance encrypts every repository
DockYard creates: the manager's and every host's, local and S3. It is the
one thing you must keep outside DockYard.

- It is generated with the first repository and **shown once**
  (`DYRK-…`). Store it in a password manager or on paper, away from the
  servers.
- Before a repository is used you **confirm** it by typing it again
  (*Confirm the key*). Confirming proves you copied it, not that it is
  stored safely.
- DockYard never shows, logs or audits it again (only its fingerprint
  `rk_…`).
- Lost key = lost backups: restic encryption cannot be bypassed by anyone.
- Rotate it (owner, *Recovery Key* → rotate) if it may have leaked. Each
  location moves to the new key the next time a job opens it; until then
  the rotation is shown as in progress and both keys work.

## Set up backups

1. **Backups → Repositories → Add repository** (owner): a local directory
   on the manager (below `DOCKYARD_BACKUP_LOCAL_ROOTS`), a local directory
   on a host (below that agent's `DOCKYARD_BACKUP_LOCAL_ROOTS`), or an S3
   bucket (endpoint, bucket, prefix, key pair; write-only, never shown
   again). *Test connection* checks access and Object Lock.
2. Save and confirm the Recovery Key.
3. **Backups → Policies → Create backup policy**: choose All Environments
   or a Single Environment. All managed stacks and standalone volumes are
   included by default; exclude specific stacks or volumes as needed. Choose
   whether to include manager state, where to store backups, the
   schedule (off until you turn it on), retention (a minimum number of
   snapshots is always kept), and whether to **stop containers during the
   backup** (off by default). With shutdown on, containers are stopped in
   dependency order and restarted afterwards, also after a failure or a
   cancellation.
4. *Back up now* runs a policy once; runs appear in the backup history with
   each member's result (a partial set says which member failed and why).

Include **manager state** in at least one policy with a repository that
does not live on the manager's own disk (S3, or a host directory on
another machine): it is what a lost manager is restored from.

## Restore stacks, volumes and files

Open a backup in **Backups**, browse its contents and choose **Restore**:

| Scope | What happens |
| --- | --- |
| a stack | its project directory is restored (Compose files, `.env`, relative bind data); volumes are untouched; redeploy afterwards |
| a volume | its content is replaced; containers using it are stopped first and started again afterwards; a missing volume is recreated |
| a single file | restored in place, or next to the original |

Restores are jobs, need `backup.restore` on every target, and keep the
original if they fail. Browsing and downloading snapshot contents need the
same permissions as reading those files live (snapshots can contain
secrets).

## Restore a lost manager

Manager state is only restored into a **fresh** manager, never over a
running one:

1. Deploy DockYard again with an empty `dockyard_data` volume
   ([Deployment](deployment.md)). For a local repository, mount it at a
   path below `DOCKYARD_BACKUP_LOCAL_ROOTS`.
2. On the setup screen choose **Import from backup**. Enter the
   destination (for S3 a new key pair is fine) and the Recovery Key;
   *Check access*, *Show backups*, pick a backup set and import. The
   manager restarts and applies it.
3. Sign in with the restored owner account. Users, groups, permissions,
   factors, stacks, policies, registry and Git credentials come back.
   Sessions and API tokens do not (create new tokens), and every host must
   be **re-attached**: *Environments* lists them; create a re-attach token
   for each and enroll its agent again.

Errors explain what to do: a wrong key (`backup_import_key_rejected`), a
set written after a key rotation (enter the previous key too), a damaged
manifest (pick another set), a newer DockYard version (install it first).
If the manager repository itself is lost, host repositories are plain
restic repositories encrypted with the same Recovery Key: `restic
snapshots --tag dockyard-manifest` lists what they hold.

## Test your backups

- Schedule **repository verification** (restic `check`) per repository.
- Once in a while, run the import on a throw-away manager (a second
  deployment with another project name and data volume) against a copy
  of the repository. It is the only proof that a restore works with the
  key you saved.
