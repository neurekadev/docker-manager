# Backups (#10, #24)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/backups.md`. restic runs only through
`internal/restic` (`Runner`, lint-exempt like the smartctl runner; tests
use `restic/restictest`). Shared helpers: `internal/backup` (destinations
and scopes `docker-manager` / `docker-manager-env-<id>`, tags, the portable
manifest, `Plan` retention, `OpenLocation`). Manager: `internal/manager/backups`
(`app.Manager.Backups()`); agent: `internal/agent/backups`.

- There is one backup setup (#246, `domain.BackupSetup`, one row): never
  bring back per-policy scopes. A run writes every member to the Primary
  repository and then to the Secondary one; anything matching set members
  (hooks, retries, failures of jobs not queued, import reconcile, the set
  manifest) keys them by repository, scope and item (`memberKey`). Roles
  live on the setup row only; removing a repository updates them in its
  transaction (`store.DeleteBackupRepository`), and every change that can
  leave backups on without a Primary calls `primaryChanged` (the
  backups-paused alert).
- Retention judges every snapshot a backup run took in a location (any
  policy tag, `backup.RetentionScope{AnyPolicy: true}`), sent to agents
  only with `protocol.FeatureBackupAnyPolicy`; never narrow it back to the
  setup's tag, or earlier policies' backups never expire.
- One instance-wide Recovery Key (#25 Q7) opens every repository: never
  return, log, audit or put it in inputs; agents get it only in
  `CommandSecrets.Repositories` (jobs) or the request/stream `credential`.
  Audit key administration by fingerprint (`rk_…`) only.
- Retention runs once per finished set and location, never per stack or
  volume, and prunes only after it forgot snapshots, or when the
  location's last prune did not finish (`backup.PrunePending`: marked
  before every prune, cleared after a successful one). A new follow-up or
  trigger must keep both (prune costs downloads at remote destinations).
  The rules and the deleted-item expiry are one decision (`backup.Plan`
  then `RetentionPlan.Expire`) shared by the preview and the executor.
- Volumes are left out by the user-set label
  `docker-manager.backup.exclude=true` (on the volume, as a Compose label
  of the volume, or on a container using it) and buildx builder volumes
  by default; change the rule in `standaloneVolumes`, `planStackVolumes`
  (`includeVolume`) and the UI's `coveredVolumes` together. The settings
  dialog lists label-excluded volumes locked (unchecked, disabled, an (i) from
  `labelLockReason`), never as a choice.
- Docker Manager's temporary objects never get into backups: temporary
  containers are recognized only by `protocol.IsHelperContainer` (UI:
  `isHelperContainer`, same patterns) and never count as users of a
  volume; a new kind of temporary container gets its pattern there (its
  name built from the `protocol` constants). A standalone volume carrying
  `protocol.LabelMigration` is selected only when that migration
  succeeded (`migrationSucceeded`). Prune's backup references
  (`VolumeReferences`) do not apply these two rules.
- An item that no longer exists when its turn comes (Engine Not Found for
  a volume; a deleted project directory whose parent is there) is
  **skipped** (`backup.StateSkipped`, `backup.ClassItemGone`), never
  failed; every other error fails the member. Skipped members count
  neither for nor against a set (`backup.Completeness`), are never
  retried and hold no snapshot; a set of skipped members only is
  `skipped`.
- A backup's snapshot step stops on cancellation mid-item: restic runs
  under `StepContext.WatchCancel`'s context and the step returns
  `jobexec.ErrStepCancelled`, keeping in its output the members backed up
  so far (they keep their snapshots) and leaving the rest pending. A new
  long restic call in a backup step follows the same pattern; restores,
  retention and verification still stop only at safe points.
- Stop/restart containers for backups/restores only through
  `internal/agent/lifecycle`, registering the `start_containers`
  compensation before stopping anything.
- The manager's own HTTP requests to an S3 endpoint use
  `s3probe.NewClient` (a nil client in `s3probe.Probe`/`ListDirs`): its
  dialer refuses loopback, link-local, metadata, unspecified and multicast
  addresses at connect time (after DNS), it follows no redirects and uses
  no environment proxy; the refusal is the class `address_not_allowed`.
  Never probe an endpoint with another client in production, never block
  private ranges (LAN/Docker MinIO), and run no restic in a connection
  test after a refused address.
- Snapshot contents hold secrets: authorize browsing with
  `backups.ContentsCapabilities` (stack.definition.read / volume.files.read,
  manager state owner-only).
- A repository's compression mode reaches restic only through
  `backup.Destination.Compression` → `restic.Location.Compression`
  (`--compression` on backup and prune only; dropped for repository
  format version 1 in `backup.OpenLocation`). Repository references sent
  to agents never carry it; `Service.CommandInput` adds it at dispatch to
  `backup.run`/`backup.retention` for agents announcing
  `backup.compression`.
- Features that remove environments (#34) or migrate data (#35) must keep
  backup repositories, sets and snapshots (instance history).
- A location's measured size changes only through
  `store.UpsertBackupLocation` with `Stats`, which also appends the
  storage-history sample (`backup_storage_samples`); anything that stops
  a repository from counting (removal) appends zero samples with
  `store.EndBackupStorage` in the same transaction. Never delete samples
  by hand outside `PruneBackupStorageSamples`.
- Manager-state restores happen only in a fresh manager (setup import,
  `backup.import`, then `app.Run`'s controlled restart applying
  `<data>/restore-pending`); never swap the database of a running manager.
  Anything new that must not survive a restore (sessions, tokens, agent
  credentials) is revoked in `app.(*Manager).finishRestore`. The copy of a
  manager move shares this path (marker kind `move`, `StageRestore`) and is
  finished by `finishMove`, which keeps them ([manager-move.md](../architecture/manager-move.md)).
- Manager data kept outside the database goes into the manager-state
  snapshot next to it (template drafts: `templates.tar.gz`, flagged in
  `state.json`) and is put back by `ApplyPendingRestore`, keeping the
  replaced copy in the pre-restore directory; every step stays
  repeatable.
- The backup settings carry the recent sets and the next run; set
  members name their repository and their backup (`backupId`, only
  backups the caller sees); the UI links by it and never matches members
  to backups itself.
- Running backups and retentions show only through `RunningBackups` (GET
  `/backup-activity`, one fixed-height line per job), never as generic job
  cards on the Backups pages. A step that runs restic for long (a
  snapshot, a prune) stops on cancellation through
  `StepContext.WatchCancel`; forget is never interrupted.
- UI (`$lib/features/backups`, `routes/(app)/backups`): users see names,
  not internals. Scopes (`env:<id>`, `docker-manager-env-<id>`), restic
  locations, snapshot IDs, host paths, key generations and fingerprints,
  permission bits and owners go under an "Advanced" disclosure or a
  tooltip (`scopeName`, `restoreTargetName`); never show configuration
  variable names or `restic` commands in copy (point to the
  documentation). Retention is chosen as a preset (`RETENTION_PRESETS`,
  `retentionPreset`/`applyRetentionPreset`) with Custom for the rules, and
  shown in words (`retentionText`, `retentionShort`); verification amounts
  are the choices of `VERIFY_READ_OPTIONS`, a repository's compression
  those of `COMPRESSION_OPTIONS` (`CompressionField`, shown with
  `compressionText`: Automatic, Maximum, Off). Sizes say what they measure: a
  run's size is the data it backed up, storage is what the repositories
  hold after deduplication and compression. An opened retention preview
  follows unsaved rule changes (debounced, newest answer only); a scope
  preview asks every agent, so a changed selection keeps it visible,
  marked out of date, until **Preview Again**. Policy edits send
  `policyEdits(draft)`: the update has no `scope`.
