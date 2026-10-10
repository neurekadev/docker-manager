# Stack archives (#313)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/stack-archives.md`. Manager
`internal/manager/stackarchives` (`app.Manager.StackArchives()`), API
`internal/manager/api/stack_archives.go`, stack records
`internal/manager/stacks/archive.go`, agent side: the migration streams
(`internal/agent/migration`, `MigrationVolumeSpec.Compose`).

- The archive format lives in `format.go` only: one tar.gz, the manifest
  (`docker-manager-stack.json`) first, then `project/` and each
  `volumes/<key>/` contiguous, each part the migration transfer's PAX tar
  re-rooted below its prefix. Bump `FormatVersion` for any change a version
  1 reader would misread; keep `Writer`, `Reader` and `PinnedName` pure and
  spec-tested (owners, modes, links, ordering and refusals).
- Archive bytes never reach an agent unverified: an upload is validated
  while it is written (`inspect`), and every part an import sends is
  re-encoded by `Reader.WriteTo` (names below the part, hard links to
  earlier files of the same part, supported types); the agent's
  `ExtractTree` checks again. Never hand an uploaded tar to an agent as is.
- Exports read only through `migration.send` and imports write only through
  `migration.receive`, `migration.commit` and `migration.cleanup` (the
  stack-file rule of [stacks.md](stacks.md)): no other path writes a
  project directory or a volume. Volumes of an import are created with
  `MigrationVolumeSpec.Compose` after the project is committed, so their
  name, labels and configuration hash are Compose's for the new stack;
  send it only to agents announcing `protocol.FeatureMigrationComposeVolume`.
- An export stops the stack (`migration.stop`) only after registering the
  `start_stack` compensation, and starts it again before the job ends; Docker
  Manager's own stack (#32) is refused before a job exists and again in
  `prepare`. A written archive stays downloadable even when starting the
  stack again failed.
- An import's stack record exists from the request on (the job's target);
  until `keep` (before the deploy) every failure runs `remove_archive_import`
  and the finish hook forgets the stack (`stacks.Service.ForgetArchiveStack`);
  after it nothing is undone, a failed deploy included.
- Archives hold `.env` values and volume data: an export needs
  `stack.export` plus `stack.files.download`, `stack.definition.read` and
  `volume.files.download` on each included volume, checked at the API and
  again in the job's `prepare`; a download needs the same of whoever
  downloads it (`ExportFile.VolumeNames`), not only of who exported it; an
  upload is visible to its uploader only.
  Never log, audit or put in job inputs anything read from an archive but
  names, counts, sizes and checksums.
- Files live in `<data dir>/stack-archives` (`exports/<jobId>.tar.gz`,
  `uploads/<id>.tar.gz` with a `<id>.json` sidecar), mode 0600; `.part`
  files are removed at start, and `Sweep` removes exports and unused uploads
  after `Retention` (a day). Size limit: `DOCKER_MANAGER_STACK_ARCHIVE_MAX_MB`
  (`config.Config.StackArchiveMax`), for the data an export writes and for an
  upload's `Content-Length`. Relays share the migrations' bandwidth cap
  (`migrations.Service.Limiter`).
