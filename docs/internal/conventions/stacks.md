# Compose stacks (#7)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/stacks.md`. Manager: `internal/manager/stacks`
(`app.Manager.Stacks()`); agent: `internal/agent/stacks`; lifecycle:
`internal/agent/lifecycle`; payloads: `internal/protocol/compose.go`.

- The on-disk definition is the source of truth (#25 Q1). Never write
  Compose/override/env files except through `compose.write` (creation,
  explicit restores) or an import by copy (`stack.import`, which creates a
  new project directory from the original's bytes and never modifies the
  original); deploys and updates only read and report the bytes they used
  (`protocol.StackJobOutput.Sources`).
- Import by copy (`internal/agent/stacks/import.go`): projects outside the
  stack roots are read through the agent's optional import mounts (at or
  below `/import`, `storage.Result.ImportSource`), stopped, copied with
  `migration.CopyTree`/`VerifyTree`/`CopyXattrs` into `<stacks>/<project>`
  (same project name, so volumes keep their names), recreated from the copy
  and resumed; before the journaled switch every failure rolls back.
  `prepare` refuses when a container differs from what the files create
  (`drift.go`: settings a previous tool injected); environment values stay
  in memory (`engine.ConfigInspector`), only names are reported.
- Revisions are immutable and sealed; record observed changes with
  `stacks.Service.RecordObserved` (#23) / `RecordFileSave` (#15); resolve a
  stack's files with `Root`; paths needing `stack.definition.*`:
  `stacks.IsDefinitionFile`.
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
