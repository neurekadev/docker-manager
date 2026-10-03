# Environment migration (#35)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/migrations.md`. Manager
`internal/manager/migrations` (`app.Manager.Migrations()`), agent
`internal/agent/migration`, framing `internal/transfer`, payloads
`internal/protocol/migration.go`.

- An environment migration (`environment.migrate`) never moves data
  itself: each stack moves as its own `stack.migrate` job, so every rule
  below (relay, hold, compensation, policies following the stack) applies
  per stack. It locks only the source host (shared) and must never take a
  stack lock (its children would wait for it). It has no stack limit:
  never put per-stack data in its input or output (bounded by
  `jobs.MaxInputSize` and `protocol.MaxResultOutput`); keep it in the
  `environment_migrations` record. The order between stacks
  comes only from their definitions (`orderStacks`: networks and volumes
  one creates and another joins as external); keep `planEnvironment` and
  `orderStacks` pure and spec-tested.
- Byte relays between two agents go through `migrations.Relay` (end-to-end
  credit, `transfer.Verifier`, three-way checksum comparison, shared
  `transfer.Limiter`); never buffer a part in memory or on disk.
- Agent-side trees are read and written only through `migration.FS`
  (an `os.Root` opened on a verified root, `Sub` for directories below it);
  `WriteTree`/`ExtractTree` never follow symlinks. Tests use
  `migrationtest.Host`/`Env` (in-memory, owners and special bits included).
- Policies that target a stack (#10 backups, #20 updates) follow a migrated
  stack: register `Migrations().OnStackMoved(func(ctx, db, stackID, from,
  to) error)` (runs in the completing transaction; use `db`, never another
  service's reads). Wired in `app`: `updates.Service.StackMoved` (policy
  re-homed, candidates unchecked), `backups.Service.StackMoved` (audit).
- Kinds whose extra targets only take locks set `jobspec.Spec.LockOnly`;
  the engine authorizes the capability on `Spec.AuthorizationTargets`.
- A manager step that loses a party mid-way returns an error wrapping
  `jobexec.ErrStepInterrupted` (job ends interrupted, compensations run).
- The engine refuses at dispatch any job whose stack target is no longer
  in the job's environment (`target_moved`); a kind that must act on a
  stack's former environment sets `jobspec.Spec.FormerStackLocation`.
- A migrated stack's stopped source is held until its removal is
  confirmed (`Migrations().RetainedSources`): prune keeps it
  (`maintenance.Service.AddReferences`) and resource removals refuse it
  (`resources.Service.StackManaged` includes it). New destructive or bulk
  features must consult the same hold.
