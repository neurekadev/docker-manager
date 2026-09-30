# Observation: inventory, metrics and events (#5)

How Docker Manager observes its environments: the Engine inventory, host and
container metrics, and Docker events, from the agent that samples them to
the manager API that serves charts and live invalidations.

| Piece | Package | Protocol / API |
| --- | --- | --- |
| Host sampler, container stats, sample ring, live reads, Engine inventory, Docker event relay | `internal/agent/observe` | `engine.info`, `host.metrics`, `metrics.live`, `event` frames ([agent-v1.md](../protocol/agent-v1.md)) |
| Disk health monitor (SMART through smartctl, md and ZFS state from procfs) | `internal/agent/health`, `internal/agent/smartctl` | `host.health` |
| Collector, live metrics, inventory cache, disk health, event journal | `internal/manager/observe` | — |
| Metrics database | `internal/manager/metrics`, `internal/db/metricsmigrations` | — |
| Routes | `internal/manager/api/observe.go`, `environments.go`, `disk_health.go` | `get-environment-system`, `create-environment-disk-health-check`, `get-environment-metrics`, `get-environment-capacity`, `get-overview`, `stream-environment-events` ([streams.md](../api/streams.md)) |

## Engine inventory

`engine.info` returns the Engine identity (Engine ID, host name, version,
maximum/minimum/negotiated API version, OS/architecture, operating system,
kernel, storage driver, cgroup version, rootless, Docker Desktop), capacity
(cores, memory) and Docker object counts (containers by state, images,
volumes, networks; `-1` when a list failed). It is read through the Moby
adapter (#21); host paths such as the Docker root directory are never sent.

The manager refreshes it

- after every reconnect, as a session reconciler (#3) that runs before the
  environment is reported online (a failure is logged and never blocks the
  environment);
- 1 s after a change (Docker event, capabilities update, `environment.resync`;
  debounced per environment: a burst of events is one refresh);
- every 5 minutes for online environments.

The last inventory per environment is stored in `metrics.db` (`inventory`
table) and served while the environment is offline. `GET
…/environments/{id}/system` merges it with the agent's capabilities report:
the editable environment name (#3) sits next to the Engine host name, so two
hosts always appear separately with their own identity.

## Host telemetry

The agent reads procfs (`DOCKER_AGENT_HOST_PROC`, default `/proc`) every 10 s:

| Value | Source | Notes |
| --- | --- | --- |
| CPU % | `stat` (aggregate `cpu` line) | busy = total − idle − iowait between two samples, percent of all cores (0–100); the first sample has none |
| memory used / total / available | `meminfo` | used = `MemTotal` − `MemAvailable` (page cache is not used); kernels without `MemAvailable` use free + buffers + cached |
| load 1/5/15 | `loadavg` | as reported by the kernel |
| uptime | `uptime` | seconds |
| network rx/tx | `1/net/dev` | bytes per second summed over non-virtual interfaces (loopback, veth, bridges and overlay/CNI devices are excluded) |
| disks | `statfs(2)` of the verified storage roots (#28) | one entry per distinct filesystem, labelled by role: `docker` (Docker's volume directory, i.e. the Docker root filesystem), `stacks`, `bind-N`; never a host path |

**Mount and namespace caveats.** `/proc/stat`, `/proc/meminfo`,
`/proc/loadavg` and `/proc/uptime` are not namespaced: inside the agent
container they describe the whole host (unless the host runs something like
lxcfs that virtualizes them; then the agent sees the container's view).
Network counters are per network namespace. The agent reads those of PID 1:

- default deployment (the documented compose files): PID 1 is the agent itself, so the rates
  cover only the agent container's traffic; `networkScope` is `agent`;
- with `pid: host` on the agent service, PID 1 is the host's init and the
  rates are the host's; `networkScope` is `host` (detected by comparing the
  network namespaces of PID 1 and the agent);
- with `network_mode: host` the agent shares the host's namespace and the
  rates are the host's; `networkScope` is `host` (detected by Docker's
  default bridge `docker0` being visible).

Docker Manager does not enable `pid: host` by default (it exposes every host
process to the agent). Operators who want host network rates add it to the
agent service. Mounting the host's procfs elsewhere (`/proc:/host/proc:ro`
and `DOCKER_AGENT_HOST_PROC=/host/proc`) is equivalent for network counters.
The Docker root filesystem is measured through the identical-path mount of
Docker's volume directory that every agent already has (#28); an Engine
whose volumes live on another filesystem than its images reports the
volumes' filesystem.

A value that cannot be read (missing file, parse error, counter going
backwards after a reboot or wrap) is absent from that sample: a gap, never
zero. Each problem is logged once until it clears.

## Container metrics

Every 10 s the sampler lists the running containers and reads one stats
sample each through the Engine adapter (`Stats`, not streaming, one-shot:
the Engine answers at once instead of waiting a second for a prior
sample), at most 8 at a time and within 80% of the interval; a tick
normally takes milliseconds per container.

- **CPU:** percent of the environment's total cores, 0–100, from the
  cumulative counters of this and the previous tick (`cpuShare`): the
  container's CPU time delta over the host's CPU time delta (Docker's
  `system_cpu_usage`, all cores) times the online cores is the share of one
  core (as `docker stats`), divided by the Engine's `NCPU`. A container
  using two full cores on an 8-core host is 25%. The first sample of a
  container (and after a restart, when its counters reset) has no CPU.
- **Memory:** usage without inactive page cache (like `docker stats`), and
  the limit only when one is set below the host memory; charts compare usage
  with the limit when present, otherwise with the environment total.
- **Rates:** network rx/tx and block read/write in bytes per second from
  counter deltas; a restarted container (counters reset) has no rate for
  that sample. PIDs as reported.
- Containers are identified by name (the #17 authorization identity).
- A container that stops between the list and its stats is simply absent.
  A timeout or Engine error flags the batch `containers incomplete`; the next
  tick starts with the containers that were not reached (round robin by
  name), so on a host with more containers than one interval can sample,
  each is still sampled regularly; they keep their previous counters, so
  their next sample has CPU and rates. At most 1 000 containers per batch.
- Without an Engine the batch holds host values only and is flagged
  `engine unavailable`.

Per-stack aggregation (sum of a stack's container CPU and memory) is a
query over container series and lands with stacks (#7); the per-container
route `get-container-metrics` needs #6's container identity routing (the
store already answers container queries).

## Agent buffer and collection

The agent keeps the newest **180 batches (30 min)** in a ring. Each batch has
a sequence number that increases per sampler epoch (one epoch per agent
process). `host.metrics {epoch, afterSeq}` returns the batches after the
manager's cursor, at most 60 batches or 768 KiB per answer (`more: true`
asks for the next page); another epoch returns the whole ring.

The manager's collector asks every online environment every 10 s, 2 s
after each 10 s slot (the agents sample on the slot boundaries, so a new
sample is stored about 2 s after it was taken), at most 8 environments at
once, 8 s per request, 40 pages per round. An environment
whose previous fetch is still running, or for which no worker is free, is
skipped for that round; its agent keeps buffering and the next round
catches up. An agent that does not serve `host.metrics` is skipped for 5
minutes. Offline environments are not asked at all: collection stays
bounded when environments disconnect or the manager is loaded.

**Reconnect never duplicates samples.** The cursor (epoch, last sequence)
is stored in the same transaction as the samples, and every sample is keyed
by `(series, 10 s slot)` with `INSERT OR IGNORE`: a batch delivered twice
(lost response, manager restart before the cursor was written, a new agent
process re-sending its ring) is ignored. A manager that was down for less
than 30 minutes backfills the agent's ring; longer outages and offline
agents are gaps.

**Clock skew.** The manager estimates the agent's clock offset from the
agent's `now` at the midpoint of each request. Within ±2 s the timestamps
are used as sent; beyond it every timestamp is shifted by the offset (flag
`skew corrected`, logged once per change, shown as `clockSkewSeconds` on
the system route). A timestamp still in the future of the manager's receive
time is clamped to it (flag `clamped`). Timestamps are then aligned to the
10 s slot, so a corrected sample can never collide with a later one.

## Live metrics

The stored samples are 10 s apart; the current CPU and memory the UI shows
(dashboard, container list and detail, stack KPIs and services) refresh
about every second while someone is looking, without storing anything.

- **Demand:** the manager asks only while at least one browser live stream
  (`GET /live/stream`) is open (`observe.Options.LiveDemand`: the live
  hub's subscriber count). Without one it stops asking and forgets the
  values; nothing is sent to the agents to stop (no lease to release).
- **Request:** every second (`protocol.LiveMetricsInterval`) the manager
  sends `metrics.live` to every online environment whose agent advertises
  it (`Hub.EnvironmentServes`; older agents are never asked), one request
  per environment at a time, at most 8 at once, 2 s timeout; an environment
  whose previous request still runs is skipped for that second. An agent
  answering `unsupported_request` is skipped for 5 minutes.
- **Agent (`observe/live.go`):** reads procfs (host CPU and memory as the
  sampler does) and one one-shot stats sample per running container (at
  most 8 at a time, within 800 ms; containers not reached are flagged,
  keep their counters and are read first next time). CPU is the delta
  against the previous `metrics.live` read, kept separately from the 10 s
  sampler's counters; a previous read older than 5 s is dropped, so the
  first answer after a pause has memory but no CPU. A request costs the
  host a `/proc/stat` and `/proc/meminfo` read and one Engine stats call per
  running container.
- **Manager (`observe/live.go`):** keeps the newest answer per environment
  in memory (stamped with the manager's receive time, forgotten after a
  minute). `Service.Latest` and `Service.LatestContainers` prefer values
  younger than 3 s (`LiveFresh`) over the newest stored sample, field by
  field (an answer without CPU keeps the stored CPU); a container only the
  live answer knows (just started) is added, and one missing from a
  complete live answer (stopped since) is dropped. Charts, rollups and the
  history never see live values.
- **Invalidation:** each answer publishes `metrics.live` on the bus (IDs
  only: `host` and the container names, filtered per reader like
  `metrics.sampled`); the live stream relays it as `invalidate` kind
  `live_metrics`, coalesced to one per environment per second and not kept
  for replay, and the browser refetches only the current-value queries
  (`…/metrics/containers`, `…/capacity`, `/overview`), never the charts.
  The per-environment event journal does not record it.

Tests: `TestLiveMetricsDeltasAndBaseline`, `TestCPUShare` (agent),
`TestLiveValuesServedWhileFresh`, `TestLiveRoundsFollowDemand` (manager),
`TestLiveMetricsAreNotReplayed` (live hub).

## Storage

Metrics (and the last Engine inventory and disk health report per
environment) live in their own SQLite file, `<data dir>/metrics.db`, with their
own migrations (`internal/db/metricsmigrations`), a single writer
connection and a separate 4-connection read-only pool (WAL). Sample writes,
rollups and retention therefore never contend with jobs and authentication
in `docker-manager.db`. The file is migrated at startup without a pre-migration
snapshot (its content is expendable); moving it away starts an empty one.
**Manager-state backups (#10) exclude `metrics.db` by default.**

| Level | Resolution | Default retention | Tables |
| --- | --- | --- | --- |
| raw | 10 s | 24 h (`DOCKER_MANAGER_METRICS_RETENTION_RAW`) | `host_raw`, `container_raw`, `disk_raw` |
| 1 min | 60 s | 7 d (`DOCKER_MANAGER_METRICS_RETENTION_1M`) | `host_1m`, `container_1m`, `disk_1m` |
| 15 min | 900 s | 90 d (`DOCKER_MANAGER_METRICS_RETENTION_15M`) | `host_15m`, `container_15m`, `disk_15m` |

- `series` maps (environment, kind, name) to an integer ID; sample tables
  are `WITHOUT ROWID` with primary key `(series_id, ts)` and store integers
  in fixed units (CPU and load in hundredths, bytes, bytes per second).
  `NULL` means unknown.
- Rollup rows hold the sample count `n`, the sample-weighted average and the
  maximum of each value, and the OR of the sample flags. 15 min rollups are
  built from 1 min rollups (weighted by `n`).
- **Rollups** run every minute as bounded manager service work (not user
  jobs): a bucket is rolled up 30 s after it ends, each transaction covers
  at most 1 h (1 min level) or 6 h (15 min level), at most 24 transactions
  per pass. A late sample (backfill after reconnect) moves the watermark
  back so the affected buckets are recomputed.
- **Retention** runs every 10 minutes: per series and level, rows older than
  the retention are deleted in index range deletes, 250 series per
  transaction; series without data are removed; free pages beyond a fifth of
  the file are returned with incremental vacuum.

### Storage limits

| Limit | Default | Behaviour |
| --- | --- | --- |
| database size (`DOCKER_MANAGER_METRICS_MAX_SIZE_MB`) | 2 048 MiB | above it every level's retention is shortened by 20% per step (oldest data first) until the used pages fit; at 5% of the configured retention new container/disk series are refused (hosts are always kept) until space is free; retention recovers by 25% per pass once below 80% of the cap |
| series (`DOCKER_MANAGER_METRICS_MAX_SERIES`) | 5 000 | samples of new containers/filesystems beyond it are dropped; host series are always accepted |
| agent ring | 180 batches | older batches are overwritten (gap if the manager was away longer) |
| containers per batch | 1 000 | more are left out and the batch is flagged |
| disks per batch | 16 | |
| query | 1 000 points | a longer range needs a larger step (422 otherwise) |
| event journal | 1 000 events or 15 min per environment | older cursors get `reset` `cursor_expired` |
| stream queue | 256 events per stream | overflow → `reset` `overflow` |
| journal bus subscription | 4 096 events | overflow → `reset` `gap` on every environment |
| agent event relay | 50 events/s, burst 200; 1 s coalescing; 4 096 coalescing keys | dropped events become a sequence gap → `environment.resync` |

Tests: `TestStorageLimitsAreEnforced` (series cap, expired samples, size
cap with shortened retention, refused series, recovery),
`TestJournalBoundsReplayAndResets` (journal size/age, overflow and gap
resets), `TestRelayCoalescesRateLimitsAndResumes` (coalescing, rate bound as
sequence gaps), `TestRingIsBoundedAndServedByCursor` and
`TestResponseSizeIsBounded` (agent ring and answer size).

## Queries and charts

`GET /api/v1/environments/{id}/metrics?from&to&stepSeconds&series` returns
host and per-filesystem series for a range: one value per step bucket.

- The storage level is the coarsest one whose resolution fits the step among
  the levels still holding data for `from`; the step is rounded up to a
  multiple of that resolution. Default range: the last hour; default step:
  about 300 points. Recent buckets not yet rolled up are read from the finer
  level, so a chart never has a hole at its right edge.
- Averages are sample-weighted; `.max` keys are the maximum within the
  bucket (`cpu.percent.max`, `memory.used_bytes.max`, network maxima).
- **Gaps, not zeros:** a bucket without samples is `null` (the agent was
  offline, the manager did not collect, the value was unknown). An
  environment that was offline for 20 minutes shows 20 minutes of `null`s
  (`TestOfflineIntervalsAreGaps`).
- `skewCorrected` and `incomplete` summarize the sample flags in the range;
  `online` tells the UI whether new samples are expected.

`GET …/metrics/containers` (`list-latest-container-metrics`,
`observe.Service.LatestContainers`) is the current usage of every
container sampled within the last minute: the fresh live values
([Live metrics](#live-metrics)) or else the newest raw sample, CPU %,
memory used and the memory limit, each absent when unknown (never zero).
It lists only the containers the caller holds `container.metrics.read` on,
resolved by name like the `metrics.sampled` events, and serves the CPU and
memory columns of the container and stack service tables and the KPIs of
the container and stack pages (one request per environment instead of a
range query per container; the web keys it with
`liveKeys.metrics(envId, 'containers-latest')`, so live metrics refresh it
about every second).

`GET …/metrics/containers/history` (`list-container-metrics-history`,
`observe.Service.QueryContainers`) is the range query of every container
at once: the same buckets, levels and keys as
`GET …/containers/{id}/metrics`, one entry per container with at least one
value in the range (sorted by name, a stopped or removed container stays
while its samples are in the range), filtered to the containers the caller
holds `container.metrics.read` on (by name, like the current usage; the
store reads no other container's series). It reads them in batches of 200
per storage level
(`series_id IN (…)`), not one query per container. It feeds the
environment page's per-container charts (Docker CPU, memory, network and
disk I/O: rx + tx and block read + write per container, about 60 buckets
per range), refreshed by `metrics` invalidations like the host charts.
There is no per-container storage chart: volumes can be shared by several
containers, so a per-container figure would count them twice.

`GET …/capacity` is the latest sample (cores, memory, load, network with its
scope, uptime, filesystems with free space). `GET /api/v1/overview` lists
every active environment the caller may see with its connection state, its
latest usage (with `environment.metrics.read`) and Docker counts (with
`environment.system.read`); totals count only what the caller may see. The
CPU and memory of both are live while fresh (`observe.Service.Latest`).

**Live updates.** Each collected page publishes `metrics.sampled` on the
internal bus (normally one per environment per 10 s, more while a backfill
is paged in; it carries no values, only `host` and the container names). `stream-environment-events` relays it as a
`metrics` invalidation to callers who may read the host or at least one of
the sampled containers' metrics, and open charts refetch. Docker events
(`engine`), status changes (`status`, incl. `resync`) and inventory
refreshes (`inventory`) go the same way, each filtered by the #17 event
rules; a metrics-only user sees metric invalidations, the environment's
status and the minimal status events of the containers they may chart, and
nothing else (`TestEnvironmentEventStreamIsPermissionFiltered`). Cursor
replay, gap and overflow resets and heartbeats: [streams.md](../api/streams.md).

## Docker events

The agent streams Engine events through the Moby adapter and relays an
allowlist of lifecycle actions (container create/start/restart/stop/die/
kill/pause/unpause/destroy/rename/update/oom/health_status; image
pull/delete/tag/untag/import/load; volume create/destroy/prune; network
create/destroy/remove/connect/disconnect/prune; daemon reload). Noise such as
`exec_*`, `attach`, `top` or volume `mount` is dropped at the source.
Attributes are an allowlist (`name`, `image`, `exitCode`, `signal`,
`health`); environment variables and labels are never sent. An immediate
repeat of the same action on the same resource within 1 s of Engine time is
coalesced; relayed events are rate-limited, and anything dropped consumes a
sequence number so the manager sees a gap (#3) and publishes
`environment.resync`. When the Engine stream breaks the agent reconnects
with backoff and asks for the events since the last one it relayed.

The manager republishes each event as `docker.event` on the internal bus
(permission-filterable invalidations: the payload identifies the resource,
the reader's capabilities decide who sees it) and triggers a debounced
inventory refresh. Docker events do not cover file content changes; stack
and volume file watching is #15.

## Host health

Each environment reports the health of its disks (SMART) and of its RAID
arrays (#143, [ADR 0005](../adr/0005-disk-health.md)); the System tab shows
them with "Check disks now" and "Check RAID now".

**Agent** (`internal/agent/health`, served as `host.health`):

- **SMART** through the image's pinned `smartctl`
  (`internal/agent/smartctl`, `DOCKER_AGENT_SMARTCTL_BINARY`): `smartctl
  --scan-open --json` lists the devices at start, every 6 h and on "Check
  disks now"; each device is read with `smartctl --json -a -n standby,3 -d
  <type> <name>` (at most 4 at once, 30 s each) at start, every
  `DOCKER_AGENT_SMART_INTERVAL` (default 30 min, 5 min–24 h) and on "Check
  disks now". A disk in standby is not woken (exit status 3 plus the
  standby message): it keeps its previous values with state `sleeping`.
  A failed read keeps the last good values. The exit status is a bitmask
  (bits 3–7 describe the disk and still come with complete JSON, so the
  values decide).
- **Values and state** per device: model, serial, firmware, capacity,
  rotation rate, overall self-assessment (`passed`), temperature, power-on
  hours; ATA raw values of attributes 5 (reallocated), 187 (reported
  uncorrectable), 197 (pending), 198 (offline uncorrectable) and the
  attributes at or below their threshold (`when_failed` now or past); NVMe
  critical warning, available spare and its threshold, percentage used,
  media errors; SCSI grown defects and uncorrected errors (read + write +
  verify). `protocol.DeriveDiskState`: **failing** for a failed
  self-assessment, an attribute failing now or an NVMe critical warning;
  **warning** for any reallocated, pending or uncorrectable sector, media
  error, grown defect or uncorrected error, wear of 90 % or more, spare
  below its threshold or an attribute that failed in the past (a hot day
  marks temperature attributes so: warning, not failing); else **ok**.
  **error** with `permission_denied`, `open_failed` or `unsupported` (no
  SMART data: virtual disks, unknown USB bridges).
- **Report status**: `ok`, `disabled` (`DOCKER_AGENT_SMART_ENABLED=false`),
  `not_installed` (no smartctl), `error` (the scan failed) or `no_access`:
  `<proc>/partitions` lists whole disks (`sd*`, `hd*`, `vd*`, `xvd*`,
  `nvme*n*`) but none of their nodes exists in the agent's `/dev` (the
  container is not privileged), or every device refused to open with a
  permission error. Visible disks without SMART data (a VM's virtio disks,
  which smartctl does not list) are `ok` with no devices.
- **RAID** is read on every request from the sampler's procfs
  (`DOCKER_AGENT_HOST_PROC`): `/proc/mdstat` (name, level, active or
  inactive, read-only, members with `(F)` failed / `(S)` spare / `(W)`
  write-mostly / `(R)` / `(J)` flags, `[n/m]`, size, the recovery,
  resync, reshape, check or repair line with percent, finish estimate and
  speed, `=DELAYED` / `=PENDING`) and `/proc/spl/kstat/zfs/<pool>/state`
  (`ONLINE` healthy, `DEGRADED` degraded, `FAULTED`/`UNAVAIL`/`SUSPENDED`/
  `REMOVED` failed, `OFFLINE` inactive). An md array is **failed** when it
  lost more members than its level tolerates (raid1: all, raid4/5: more
  than one, raid6: more than two, raid0/linear: any), **rebuilding**
  during (or waiting for) a recovery, resync or reshape, **degraded** with
  missing or failed members, **checking** during a check or repair,
  **inactive** when stopped, else **healthy**. btrfs and hardware RAID
  controllers are not covered.
- `host.health {refresh}`: `smart` starts a fresh scan and read of every
  disk and waits up to 3 s; a longer read answers with `smart.checking`
  and the result comes with the next request. `raid` answers at once.

**Manager** (`internal/manager/observe/health.go`): asks every online
environment whose agent serves `host.health` about once a minute, 5 s after
it comes online (or its agent's capabilities change) and every 5 s while
the agent reads its disks (at most 10 minutes). An agent answering
`unsupported_request` is skipped for 5 minutes. A report is kept in memory,
stored in `metrics.db` (`host_health`: the agent's JSON, `collected_at`,
`received_at`) only when its content changed (ignoring the read times) and
announced as `inventory.updated` with `Attributes["health"] = "true"` (the
inventory event's visibility rule; the System tab refetches). The stored
reports are restored at startup and served while the environment is
offline. A restarted agent whose first read is still running keeps the last
known devices (marked checking). `CheckHealth` serves the check route: at
most one check per environment every 30 s (smart) or 5 s (raid), `429`
with `Retry-After` before.

**API**: `GET …/environments/{id}/system` carries `diskHealth` (status as
above plus `agent_outdated` when the agent's capabilities lack
`host.health` and `unknown` before the first report; `checking`,
`checkedAt`, the devices) and `raid` (the md arrays and ZFS pools as
`arrays` with `kind`). `POST …/environments/{id}/disk-health/checks
{scope: smart|raid}` (`environment.system.read`: a read that changes
nothing on the host, audited as `environment.disk_health.check`) answers
the fresh state.

## Scale budget

Target (#5): 25 environments, 1 000 containers in total and 20 concurrent UI
sessions on a 2 vCPU / 2 GB manager, with 10 s samples for 24 h, 1 min
rollups for 7 d and 15 min rollups for 90 d.

**Historical measurement, not re-verified.** The numbers below come from
the scale-budget test and benchmarks (`TestScaleBudget`, `Benchmark*` in
the former `internal/manager/metrics/bench_test.go`), which were removed on
2026-09-25 with the move to a lint-and-unit-test CI. Nothing checks the
budget automatically any more; treat the table as the last known result.

Measured 2026-09-25 on an AMD Ryzen 9 9950X with `GOMAXPROCS=2` (Windows,
pure-Go SQLite). A 2 vCPU cloud VM is slower per core; allow a factor of
2–4.

| Benchmark | What one operation is | Result | Share of the 10 s budget |
| --- | --- | --- | --- |
| `TestScaleBudget` | worst of 6 steady-state cycles: ingest 25 hosts + 25 disks + 1 000 containers (one transaction per environment), a rollup pass, 20 sessions × (1 h host chart + 1 h container chart) concurrently, over 30 min of history | 282 ms wall (asserts < 5 s) | 2.8% wall |
| `BenchmarkIngestTick` | one full tick (1 050 series) | 12.2 ms, 1.6 MB allocated | 0.12% of one core |
| `BenchmarkLoadBudget` | the steady-state cycle above over 6 h of history | 89.7 ms, 18 MB allocated | 0.9% of one core |
| `BenchmarkQueryRanges/1h` | host chart of 1 h, default step (20 s, 180 points, raw samples, 14 series) | 1.3 ms | — |
| `BenchmarkQueryRanges/6h` | 6 h (2 min step, 180 points, 1 min rollups) | 1.5 ms | — |
| `BenchmarkQueryRanges/24h` | 24 h (5 min step, 288 points, 1 min rollups) | 3.5 ms | — |
| `BenchmarkStorageFootprint` | 2 h of the full budget, then rollups | 39 B per raw row, 47 B per rollup row | — |

**Disk.** At 39 B per raw row and 47 B per rollup row, the full budget
(1 050 series × (8 640 raw + 10 080 one-minute + 8 640 fifteen-minute rows))
needs about **1.2 GiB**, below the 2 GiB default cap
(`DOCKER_MANAGER_METRICS_MAX_SIZE_MB`); a smaller cap shortens retention instead of
failing.

**Memory.** The metrics store keeps only the series map in memory (≤ 5 000
entries) plus SQLite's page cache (default 2 MiB per connection, 5
connections). The event journal holds at most 1 000 entries per
environment (25 000 for the budget, a few MiB), each stream at most 256
queued entries, the inventory cache one document per environment. A 10 s
cycle allocates about 18 MB of short-lived garbage. Together this stays in
the low tens of MiB of the 2 GB budget. On each agent the ring holds 180
batches; with 40 containers that is about 1 MiB (the 1 000-container
maximum on one host about 25 MiB).

**CPU.** Collection, ingestion, rollups and 20 dashboards refreshing every
10 s use about 1% of one core on the benchmark machine; the retention pass
every 10 minutes deletes per series in index ranges (250 series per
transaction). Collection is bounded by 8 concurrent fetches and skips
environments still in flight, so a slow or loaded manager falls behind
gracefully (agents buffer 30 minutes) instead of queueing work.

**Live metrics** (not covered by the historical numbers above) add, only
while a browser live stream is open, one small request per online
environment and second (the full budget: 25 requests/s, answers of about
100 bytes per running container), one Engine stats call per running
container and second on each host, and per open tab at most one refetch
per second of the current-value queries (`/overview`, and
`…/metrics/containers` / `…/capacity` of the environments it shows), each
a read of the in-memory values plus one indexed `metrics.db` lookup per
environment.
