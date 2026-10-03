# Docker maintenance (#14, #238)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/maintenance.md`. Manager:
`internal/manager/maintenance` (`app.Manager.Maintenance()`); agent:
`internal/agent/prune`; payloads: `internal/protocol/maintenance.go`.

- Never call an Engine prune endpoint: list, filter, revalidate right
  before removing, remove one object per targeted call (build cache: one
  record ID per builder prune, `engine.RemoveBuildCache`).
- One maintenance setup for the instance (`maintenance_settings`, one
  row): it covers every active environment except `excludeEnvironments`,
  also ones added later. Never add per-environment maintenance
  configuration; the settings need instance grants
  (`maintenance_policy.read`/`.manage` are instance-only), one-off prunes
  of one environment need `maintenance.preview`/`.run` there.
- Every rule and the setup start disabled; volume rules need their own
  `volumeOptIn`; manual runs need `confirm: true`.
- An object labeled `docker-manager.maintenance.exclude=true` is never
  removed: `ruleDecision` checks it first, so planning and the removal
  re-check agree.
- Objects to protect from pruning: Docker Manager's own (#32, agent guard),
  Docker Manager stacks' projects and images, saved container specifications
  (`resources.Service.ManagedSpecRefs`), backups (#10 installs
  `maintenance.Service.SetBackupReferences`).
- `prune.run` takes shared `*` locks on stacks, containers, images,
  networks and volumes: it serializes with deploys, builds, updates, pulls,
  migrations, backups and restores.
