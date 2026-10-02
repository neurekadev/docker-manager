# Compose stacks (#7)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/stacks.md`. Manager: `internal/manager/stacks`
(`app.Manager.Stacks()`); agent: `internal/agent/stacks`; lifecycle:
`internal/agent/lifecycle`; payloads: `internal/protocol/compose.go`.

- The on-disk definition is the source of truth (#25 Q1). Never write
  Compose/override/env files except through `compose.write` (creation,
  explicit restores), an import by copy (`stack.import`, which creates a
  new project directory from the original's bytes and never modifies the
  original) or a creation from a template (`CreateFromTemplate`, which
  streams the version's tar through `migration.receive` into the stacks
  volume's staging area and `migration.commit`s it into a new directory,
  never an existing one); deploys and updates only read and report the
  bytes they used (`protocol.StackJobOutput.Sources`).
- Links of stacks and templates (`domain.Link`, documentation, website,
  repository) are display metadata like the description: check them only
  with `domain.NormalizeLinks` (untrusted data such as another instance's
  registry with `domain.SanitizeLinks`, which drops invalid links), keep
  them apart from `DisplayMeta` (per-service metadata converts to its
  storage type), and never log or audit a URL (it may carry a query
  string): audit the number of links.
- Stacks and services have no icon: never add one to `DisplayMeta`, the
  store or a response. The deprecated `icon` request members stay
  accepted and ignored, the response members are never set, and the
  former `dev.neureka.docker-manager.icon` label is not read (details in
  [architecture/stacks.md](../architecture/stacks.md#details-and-links)).
- Stacks created from a template carry `domain.Stack.Template` (registry
  instance ID, template ID, name, version): informational only, never a
  dependency. The user's own `.env` reaches the new stack through the file
  routes afterwards, never through the creation request.
- Import by copy (`internal/agent/stacks/import.go`): projects outside the
  stack roots are read through the agent's optional import mounts (at or
  below `/import`, `storage.Result.ImportSource`), stopped, copied with
  `migration.CopyTree`/`VerifyTree`/`CopyXattrs` into `<stacks>/<project>`
  (same project name, so volumes keep their names), recreated from the copy
  and resumed; before the journaled switch every failure rolls back. The
  copy is made durable with `migration.SyncTree`/`SyncDir` (fsync of the
  copy only, cancellable); never call a host-wide `sync(2)` from a job step:
  it waits for every filesystem of the host and ignores cancellation.
  `prepare` refuses when a container differs from what the files create
  (`drift.go`: settings a previous tool injected, or files edited after the
  last deploy, named by `editedAfter`); environment values stay in memory
  (`engine.ConfigInspector`), only names are reported. Build-only services
  keep the image they run (tagged with Compose's name before the
  recreate). Docker Manager's own project is copied while it runs
  (`import.live`): never stopped nor recreated; its next deploy moves it
  onto the copy. A containerless project (discovered through its Compose
  file, `discover.go`; `StackImportSource.Containerless`, sent only to
  agents announcing `protocol.FeatureStackImportContainerless`) is never
  stopped, recreated or started: its import only copies and switches, and
  the steps refuse it once the project has containers.
- Rename (`internal/agent/stacks/rename.go`, `stack.rename`): the project
  name changes only through this job, which moves the volumes whose names
  follow the project (a local volume's `_data` is renamed, never copied;
  new volumes get Compose's labels and config hash from
  `compose.Project.VolumeSpec`), recreates outside containers with
  `engine.Cloner` and undoes everything before the switch. A Compose file's
  top-level `name:` pins the project name (`compose.DeclaredName`): deploys
  refuse a `name:` that differs from the stack's (`stack_project_renamed`).
  Anything the manager keys by a stack's project name or its volume names
  follows a rename through `stacks.Service.OnRenamed` (transactional, like
  `Migrations().OnStackMoved`); outside containers need the caller's own
  container rights (`StackJobRequest.MayRecreate`).
- A revision is a version of the definition files, never a deploy: the
  deploy, import and rename finish hooks resolve reported sources with
  `stacks.Service.deployedRevision` (reuses the observed or applied
  revision of the same hash), and a digest update never records one. An
  import of a containerless project deployed nothing: its files are only
  observed (`observe`, `external`), no revision is applied and the stack
  stays `undeployed`, in place or by copy.
- Revisions are immutable and sealed; record observed changes with
  `stacks.Service.RecordObserved` (#23) / `RecordFileSave` (#15); resolve a
  stack's files with `Root`; paths needing `stack.definition.*`:
  `stacks.DefinitionPaths` (observed revision, creation names, every
  Compose and env file; clean project-relative paths), which
  `StackFileRoot` hands to the file manager, and `stacks.IsDefinitionFile`.
- Stop/start containers of a stack (backups #10, updates #20, migrations
  #35, container actions #9) with `internal/agent/lifecycle`
  (`GraphFromContainers` + `EngineRuntime`, `Stop`/`Start`/`Restart`/`Resume`),
  never by looping over containers.
- Job results: steps set output with `sc.SetOutput`; the manager reacts in
  `jobs.Engine.OnFinish` hooks (transactional, `j.ResultOutput`).
- Recreated containers keep their anonymous volumes (`compose.Adapter.Up`
  and `Create` set the SDK's `Inherit`; its zero value loses the data).
  Any new SDK call building `api.CreateOptions` must do the same.
- Agent payloads address a project with `protocol.ProjectRef` (root +
  project-relative dir + project name); `protocol.StackRef` is #6's
  "which Compose project a Docker object belongs to".
