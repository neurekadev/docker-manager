# Observation (#5)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/metrics.md`. Agent: `internal/agent/observe`
(procfs sampler, container stats, ring, `engine.info`/`host.metrics`, Docker
event relay). Manager: `internal/manager/observe` (collector, inventory
cache, event journal) and `internal/manager/metrics` (separate
`<data>/metrics.db`, own migrations in `internal/db/metricsmigrations`).

- **Read metrics:** `Store.Query(ctx, domain.MetricQuery{Kind:
  domain.MetricContainer, Name: containerName, ...})` (container charts,
  #6/#7), `Store.Latest`; values are `nil` for gaps, never 0. Units: CPU %
  of the environment's total cores, bytes, bytes/s.
- **Inventory:** `observe.Service.Inventory(envID)` (last known, also
  offline). Refreshes are triggered by `docker.event`,
  `agent.capabilities_updated` and `environment.resync` on the bus.
- **Live invalidations:** `metrics.sampled` (`Members` = container names,
  internal: filter per member with `authz.ContainerMetricsVisible`) and
  `inventory.updated` on the bus; `stream-environment-events` relays them
  through the per-environment `observe.Journal` (cursor replay, resets).
- Sample keys are `(series, 10 s slot)`: ingestion is idempotent; never add
  a path that writes samples without going through `Store.Ingest`.
- `metrics.db` is expendable and excluded from manager-state backups (#10).
