# Backups (#10, #24)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/backups.md`. restic runs only through
`internal/restic` (`Runner`, the one lint-exempt process execution; tests
use `restic/restictest`). Shared helpers: `internal/backup` (destinations
and scopes `docker-manager` / `docker-manager-env-<id>`, tags, the portable
manifest, `Plan` retention, `OpenLocation`). Manager: `internal/manager/backups`
(`app.Manager.Backups()`); agent: `internal/agent/backups`.

- One instance-wide Recovery Key (#25 Q7) opens every repository: never
  return, log, audit or put it in inputs; agents get it only in
  `CommandSecrets.Repositories` (jobs) or the request/stream `credential`.
  Audit key administration by fingerprint (`rk_…`) only.
- Retention runs once per finished set and location, never per stack or
  volume, and prunes only after it forgot snapshots. A new follow-up or
  trigger must keep both (prune costs downloads at remote destinations).
  The rules and the deleted-item expiry are one decision (`backup.Plan`
  then `RetentionPlan.Expire`) shared by the preview and the executor.
- Volumes are left out by the user-set label
  `docker-manager.backup.exclude=true` (on the volume or a container using
  it) and buildx builder volumes by default; change the rule in
  `standaloneVolumes`, `planStackVolumes` and the UI's `coveredVolumes`
  together.
- Stop/restart containers for backups/restores only through
  `internal/agent/lifecycle`, registering the `start_containers`
  compensation before stopping anything.
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
- Manager-state restores happen only in a fresh manager (setup import,
  `backup.import`, then `app.Run`'s controlled restart applying
  `<data>/restore-pending`); never swap the database of a running manager.
  Anything new that must not survive a restore (sessions, tokens, agent
  credentials) is revoked in `app.(*Manager).finishRestore`.
- Manager data kept outside the database goes into the manager-state
  snapshot next to it (template drafts: `templates.tar.gz`, flagged in
  `state.json`) and is put back by `ApplyPendingRestore`, keeping the
  replaced copy in the pre-restore directory; every step stays
  repeatable.
- The policy list carries what the detail shows (`recentSets`, the next
  run) through `addPolicyRuns`, batched per page: never a query per
  policy on the server or a detail request per policy in the UI. Set
  members name their backup (`backupId`, only backups the caller sees);
  the UI links by it and never matches members to backups itself.
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
  marked out of date, until **Preview again**. Policy edits send
  `policyEdits(draft)`: the update has no `scope`.
