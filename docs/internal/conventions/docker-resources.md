# Docker resources (#6)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/docker-resources.md`. Manager:
`internal/manager/resources` (`app.Manager.Resources()`); agent:
`internal/agent/resources` (wired by the runtime); wire types and the
create-form validation: `internal/protocol/docker.go`; in-memory Engine for
tests: `internal/agent/engine/enginefake`.

- Read Docker objects through `resources.Service` (`ListContainers`,
  `InspectContainer`, ...: agent requests scoped by environment, errors are
  `*domain.DockerError` with stable codes); never a second path to the
  agent for the same data.
- Docker Manager's labels (`protocol.Label*`, prefix `docker-manager.`),
  their legacy prefix `dev.neureka.docker-manager.` and Compose's are
  reserved: user input may not set them (`protocol.ValidateLabels`,
  `OwnLabel`). The only exceptions are the user-set exclusions
  (`protocol.UserLabels`), which read `true` in any case:
  `docker-manager.update.exclude`, `docker-manager.backup.exclude` and
  `docker-manager.maintenance.exclude` (`protocol.UpdateExcluded`,
  `BackupExcluded`, `MaintenanceExcluded`); a new one follows the same
  `docker-manager.<feature>.exclude` pattern and joins `UserLabels`.
- Docker never relabels an existing volume and deploys never recreate one,
  so the `UserLabels` a stack's Compose file declares on a volume with a
  value the volume lacks are **Compose labels**: every `stack.deploy`
  records them (`internal/agent/volumelabels`, `<state>/volume-labels.json`,
  keyed by volume name and creation time; `stack.remove` forgets the
  project). Code that decides by a volume's labels on the agent reads
  `volumelabels.Effective(v.Labels, store.Compose(v.Name, v.CreatedAt))`
  (Compose labels win); volume details return them apart as
  `composeLabels` (the UI's Labels card groups them, with an (i)). Never
  write them onto a volume and never take any other label from the file.
- The Engine creates anonymous volumes without a Compose project label, so
  after a Compose down nothing ties them to their stack: `stack.down`
  records the anonymous volumes of the containers it removes before it
  runs (`internal/agent/downvolumes`, `<state>/down-volumes.json`, keyed by
  project; temporary containers left out; a down of a project without
  containers keeps the record), a successful deploy and `stack.remove`
  forget it, a rename moves it. Backups use the record only while the
  project has no containers (#276); never treat it as a list of the
  stack's current volumes.
- Write Docker Manager's labels only under the current keys; read them only
  with `protocol.LookupLabel` / `LabelValue` / `HasRole`, which also accept
  the legacy key (objects created before 2026-09-28 keep it forever;
  `protocol.legacyLabels` lists the labels that existed then, a new label
  has no legacy key). Ownership labels sent to an agent go through
  `resources.Service.Ownership` (legacy keys for agents without
  `protocol.FeatureLabels`). Details:
  [architecture/docker-resources.md](../architecture/docker-resources.md#label-keys).
- Stack membership: `protocol.StackRef` (`Managed`: working directory in a
  verified stack root) plus `resources.Service.StackManaged`; containers of
  a managed stack are changed through the stack, never directly
  (`stack_managed`).
- Job executors return `jobexec.ClassedError` (class + recovery) for
  failures users must tell apart (Engine/registry codes, refusals).
- Hooks: #7 installs `SetStackResolver` (Compose project -> stack ID).
  Pulls select their registry connection through `registries.Service`
  (#19; `jobspec.CredentialRefs` in the input, `regauth` on the agent).
- Recreate specifications of standalone containers created through
  Docker Manager: `resources.Service.ManagedSpec` (sealed; never return
  environment values). Anything that renames Docker objects a
  specification references updates it in the same transaction (stack
  renames: `resources.Service.StackRenamed`).
