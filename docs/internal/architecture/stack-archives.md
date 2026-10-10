# Stack archives (#313)

A user exports a managed stack (its project directory and its plain local
named volumes) as one archive file and downloads it, then uploads that file
to any Docker Manager and creates a stack from it in an environment of their
choice, under the same or a new name. Exports run as a job that stops the
stack for a consistent copy; nothing is written on a host before an import's
check passes.

| Package | Role |
| --- | --- |
| `internal/manager/stackarchives` | The format (`format.go`), export previews and the `stack.export` executor (`export.go`), uploads (`uploads.go`), import previews and the `stack.import_archive` executor (`import.go`), the sweep. |
| `internal/manager/stacks` (`archive.go`) | The stack record of an import: reserve, attach the job, record the committed files as the first revision, forget. |
| `internal/manager/api` (`stack_archives.go`) | Export preview, start and download; upload, get and discard; import preview and start. |
| `internal/agent/migration` | The streams both directions use; `migration.receive` creates an import's volumes from the committed project (`composeVolume`). |
| `internal/protocol` (`migration.go`) | `MigrationVolumeSpec.Compose`, `FeatureMigrationComposeVolume`. |

## Format

One tar, gzip-compressed (a plain tar is read too), version 1:

| member | holds |
| --- | --- |
| `docker-manager-stack.json` | the manifest: format and version, export time and manager version, the stack (name, display name, description, links, configured Compose and env files), the project directory's and each volume's entry and byte counts, each volume's key, name, whether it follows the project name and its user-set labels, what is not included (volumes, anonymous volumes, binds outside the project directory, with the reason), the services (image, build, from a registry, container names, published ports), networks and external volumes |
| `project/…` | the project directory |
| `volumes/<key>/…` | each included volume, by Compose key, in manifest order |

Each part is the migration transfer's PAX tar (root first, numeric owners,
permission and special bits, nanosecond times, symlinks as links, hard
links, FIFOs; see [migrations](migrations.md#transfer)) with its names and
hard link targets moved below the prefix, so `tar -xzf` unpacks an archive
with owners intact. Directory entries tools add above the parts (`./`,
`volumes/`) are skipped when reading, so an archive repacked with GNU tar in
the same order still reads.

## Export

`stack.export` (manager executor; locks: host shared, the stack exclusive;
capability `stack.export`, the job's `prepare` re-checks the other grants):

1. **prepare** — the check again (environment online, agent support, not
   Docker Manager's own stack, the volumes still included); free space on
   the manager.
2. **stop** — records the running services, registers `start_stack`, then
   `migration.stop` (reverse dependency order). Nothing runs: nothing stops.
3. **write_archive** — the manifest (from a fresh `migration.preview`), then
   per part `migration.send` read through `transfer.Reader` into the archive
   (`exports/<jobId>.tar.gz.part`, fsync, rename); the agent's part result
   must match the manager's framing. A lost session or a checksum mismatch
   writes the whole archive again (3 attempts; a lost agent is awaited for
   2 minutes). Progress by bytes against the measured size.
4. **start** — `migration.start` with the recorded services; releases the
   compensation.
5. **finalize** — reports the file.

The check (`create-stack-export-preview`) lists the named volumes: included
are plain local volumes that exist; external volumes, Docker Manager's own,
not yet created, with driver options or another driver, and those the caller
may not download are not (the last block the export until left out). It
sizes the data against `DOCKER_MANAGER_STACK_ARCHIVE_MAX_MB` and the
manager's free space, estimates the downtime (10 s plus the data at 50 MB/s)
and returns the newest archive still available. Archives are kept for a
day; the download is a ranged file response.

## Import

1. **Upload** (`create-stack-archive`): the body streams to
   `uploads/<id>.tar.gz.part` while `inspect` reads it through a pipe
   (manifest, every part re-encoded to nowhere, sizes, the root Compose
   files for a pinned `name:`); invalid archives are deleted, valid ones get
   a sidecar and live a day. At most five per user.
2. **Check** (`create-stack-archive-import-preview`, pure `evaluateImport`
   over the agent's `migration.preview` destination facts): the new name
   (taken, a running Compose project, pinned by the archive), the folder,
   the containers, volumes and networks the stack creates (names that follow
   the old project name follow the new one), ports, external networks and
   volumes, free space, images built on the source that are not there.
3. **Start** (`create-stack-archive-import`, stored idempotency): the stack
   is reserved (undeployed, no files) and `stack.import_archive` queued
   (targets: the stack and its new volumes, lock only).

`stack.import_archive` (manager executor; locks: host shared, the stack and
the new volumes exclusive; capability `stack.create`, `prepare` re-checks
`volume.create` and `stack.deploy`):

1. **prepare** — the check again (ignoring the stack's own record).
2. **transfer_project** — registers `remove_archive_import`, then the
   project part through `migration.receive`.
3. **commit** — `migration.commit` into `<stacks>/<name>` (never an
   existing directory).
4. **check_definition** — `compose.validate` and `compose.read` of the
   committed files under the new name: the first (observed) revision, the
   services; a definition that pins another name fails here, before any
   volume exists.
5. **transfer_volumes** — each volume through `migration.receive` with
   `compose: {stack, key}`: the agent creates it exactly as Compose would for
   the new stack (name, labels, configuration hash) and fills it.
6. **deploy** — from here the stack keeps its files (`keep`: the
   compensation is released, the staging directory removed); a
   `stack.deploy` job when asked, awaited. A failed deploy fails the job
   but keeps the stack.
7. **finalize**.

A failure before `keep` removes the committed directory and the volumes the
job created (labeled with its ID) and forgets the stack; the upload stays
for another try. A successful (or kept) import removes the upload.

## Audit

Requests are audited by `api.Register` (`stack.export.preview`,
`stack.export`, the download, `stack.create` for the upload and the import,
`stack.import_archive.preview`) with counts, sizes and IDs; job lifecycles
by the engine. Nothing read from an archive is recorded but names, counts
and sizes.
