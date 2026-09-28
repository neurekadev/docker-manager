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
- Stop/restart containers for backups/restores only through
  `internal/agent/lifecycle`, registering the `start_containers`
  compensation before stopping anything.
- Snapshot contents hold secrets: authorize browsing with
  `backups.ContentsCapabilities` (stack.definition.read / volume.files.read,
  manager state owner-only).
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
- UI (`$lib/features/backups`, `routes/(app)/backups`): users see names,
  not internals. Scopes (`env:<id>`, `docker-manager-env-<id>`), restic
  locations, snapshot IDs, host paths, key generations and fingerprints,
  permission bits and owners go under an "Advanced" disclosure or a
  tooltip (`scopeName`, `restoreTargetName`); never show configuration
  variable names or `restic` commands in copy (point to the
  documentation). Retention is chosen as a preset (`RETENTION_PRESETS`,
  `retentionPreset`/`applyRetentionPreset`) with Custom for the rules, and
  shown in words (`retentionText`, `retentionShort`); verification amounts
  are the choices of `VERIFY_READ_OPTIONS`. Sizes say what they measure: a
  run's size is the data it backed up, storage is what the repositories
  hold after deduplication and compression.
