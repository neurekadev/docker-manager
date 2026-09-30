# Observation (#5)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/metrics.md`. Agent: `internal/agent/observe`
(procfs sampler, container stats, ring, `engine.info`/`host.metrics`, Docker
event relay), `internal/agent/health` and `internal/agent/smartctl` (disk
health, `host.health`). Manager: `internal/manager/observe` (collector,
inventory cache, disk health, event journal) and `internal/manager/metrics`
(separate `<data>/metrics.db`, own migrations in
`internal/db/metricsmigrations`).

- **Read metrics:** `Store.Query(ctx, domain.MetricQuery{Kind:
  domain.MetricContainer, Name: containerName, ...})` (container charts,
  #6/#7; kind `host` also returns the disk series labelled `Mount` and the
  temperature sensor series labelled `Sensor`), `Store.QueryContainers`
  (every container of an environment with values in the range, one query
  per 200 series and storage level; the environment's per-container
  charts), `Store.Latest`; values are `nil` for gaps, never 0. Units: CPU
  % of the environment's total cores, bytes, bytes/s, degrees Celsius.
- **Temperatures** (#146): the agent reads hwmon only through the
  sampler's `fs.FS` (`Options.Sys`, `DOCKER_AGENT_HOST_SYS`); a sensor is
  named `<chip>: <label>` and never by a host path, an unreadable or
  implausible reading is left out (a gap), at most
  `protocol.MaxTemperatureSamples` per batch. The manager stores one
  `sensor` series per environment and sensor name, counted against the
  series cap like disks.
- **Inventory:** `observe.Service.Inventory(envID)` (last known, also
  offline). Refreshes are triggered by `docker.event`,
  `agent.capabilities_updated` and `environment.resync` on the bus.
- **Live invalidations:** `metrics.sampled` (`Members` = container names,
  internal: filter per member with `authz.ContainerMetricsVisible`) and
  `inventory.updated` (Engine inventory or disk health) on the bus;
  `stream-environment-events` relays them
  through the per-environment `observe.Journal` (cursor replay, resets).
- **Current CPU and memory** (`metrics.live`, `observe/live.go` on both
  sides): read the current values only through `Service.Latest` /
  `Service.LatestContainers` (fresh live values over the stored sample,
  per field); never store live values or send them to the journal. The
  manager asks only while `LiveDemand` holds (a browser live stream is
  open) and only agents that serve `metrics.live`; the `metrics.live` bus
  event carries IDs like `metrics.sampled`, never values. Container CPU is
  always a delta of the cumulative counters of two one-shot stats reads
  (`cpuShare`), never an Engine-side prior sample.
- **Disk health** (#143, `host.health`; agent `internal/agent/health` and
  `internal/agent/smartctl`, manager `observe/health.go`): read it through
  `Service.HostHealth(envID)` (last known, also offline); checks go
  through `Service.CheckHealth` (rate limited per environment and scope).
  A report is stored (`metrics.db`, `host_health`) and announced as
  `inventory.updated` with `Attributes["health"] = "true"` only when it
  changed. smartctl runs only through `internal/agent/smartctl.Runner`
  (the second lint-exempt process execution, ADR 0005): read-only flags,
  `-n standby`, never a self-test. Serial numbers are data for
  `environment.system.read` holders: never log them. The manager sends
  `host.health` only to agents that serve it (`EnvironmentServes`).
- Sample keys are `(series, 10 s slot)`: ingestion is idempotent; never add
  a path that writes samples without going through `Store.Ingest`.
- `metrics.db` is expendable and excluded from manager-state backups (#10).
