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
  1 reader would misread; keep `Writer`, `Reader`, `LoadedComposeFiles`,
  `PinnedName` and `ExplicitVolumeNames` pure and spec-tested (owners,
  modes, links, ordering, bounds and refusals). The reader keeps no
  per-member state (millions of files cost no memory): bound it by members
  (`MaxEntries`), file bytes and the whole decompressed stream
  (`NewLimitedReader`), never by a set of names; sparse members are
  refused (their holes would escape the bounds). `Manifest.Validate` bounds
  every list (volumes, services, networks, exclusions, Compose and env
  files, and the names one check asks the destination about: the agents'
  4096) and refuses repeated volume keys, volume names and definition
  files; an export's check validates the manifest it would write, so no
  export produces an archive an upload refuses. Resolve default Compose
  files exactly as the agent does (`compose.configFiles`). An import step
  sends its parts through one open reader (`archiveCursor`), in archive
  order.
- Archive bytes never reach an agent unverified: an upload is validated
  while it is written (`inspect`), and every part an import sends is
  re-encoded by `Reader.WriteTo` (names below the part, hard links to
  names of the same part, supported types); the agent's `ExtractTree`
  checks the rest (hard links only to earlier regular files, parents,
  duplicates, free space). Never hand an uploaded tar to an agent as is.
- Exports read only through `migration.send` and imports write only through
  `migration.receive`, `migration.commit` and `migration.cleanup` (the
  stack-file rule of [stacks.md](stacks.md)): no other path writes a
  project directory or a volume. Volumes of an import are created with
  `MigrationVolumeSpec.Compose` after the project is committed, so their
  name, labels and configuration hash are Compose's for the new stack;
  send it only to agents announcing `protocol.FeatureMigrationComposeVolume`.
  The names come from the destination's view of the committed project
  (`composeVolumeNames`), never from the archive alone.
- An export stops the stack (`migration.stop`) only after registering the
  `start_stack` compensation, and starts it again before the job ends; Docker
  Manager's own stack (#32) is refused before a job exists and again in
  `prepare`. A written archive stays downloadable even when starting the
  stack again failed.
- An import's stack record exists from the request on (the job's target);
  until `keep` (before the deploy) every failure runs `remove_archive_import`
  and the finish hook forgets the stack (`stacks.Service.ForgetArchiveStack`);
  after it nothing is undone. Release the compensation before recording
  `kept`. The deploy is queued as its own `stack.deploy` job and never
  awaited: it needs the stack's lock, which the import holds.
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
  upload's `Content-Length`. Writes claim their size of the free space
  (`Service.reserve`) until they end; uploads in progress count toward the
  per-user limit; an upload unpacks to at most `maxRatio` times its size.
  Job inputs hold no measured sizes (idempotent retries). Relays share the
  migrations' bandwidth cap
  (`migrations.Service.Limiter`).
