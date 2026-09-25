# Self-protection: DockYard's own containers, images and volumes (#32)

DockYard cannot disable or delete itself through its own UI, API, API
tokens, policies or jobs. Its resources appear in the inventory as
protected ("DockYard system") with a reason, and destructive operations on
them fail with `409 protected` — for everyone, the owner included (no
override in v1; Docker on the host is the escape hatch).

| Package | Role |
| --- | --- |
| `internal/protection` | The decisions, shared by manager and agent: roles, `Check(p, action, confirmed)`, `Excluded(p)`, `Filter(items, protectionOf)`, `Refusal` (a `jobexec.ClassedError`: job class `protected` / `confirmation_required`). |
| `internal/agent/protect` | Identification on the agent: `Guard` (own container ID, manager identity, stacks volume), `Identify` → `Set` (containers, images, volumes, networks, Compose projects, Docker data root), the `manager.identity` request handler. |
| `internal/selfid` | The ID of the container the process runs in, from `/proc/self/mountinfo` (fallback `/proc/self/cgroup`); used by the agent (itself) and the manager (co-located manager). |
| `internal/agent/resources` | Annotates every inventory answer with `protocol.Protection` and checks before every destructive step. |
| `internal/manager/resources` (`protection.go`) | Manager-side checks before a job exists (`ContainerProtection` adds the role labels and the manager's own container ID to the agent's annotation), `ProjectProtection`, `ProtectedContainers`. |

## Identification (per Engine, at every inventory read and job step)

| Resource | How | Role |
| --- | --- | --- |
| The connected agent's container | its own container ID (`selfid`, verified against the container list) | `agent` (self) |
| A co-located manager | the manager's container ID from `manager.identity` (sent by the manager after every reconnect; the manager finds its own ID with `selfid`) | `manager` (self) |
| Other DockYard containers | label `dev.neureka.dockyard.role=agent|manager` (deploy examples) — also when no ID matched (another installation, or detection failed) | `agent` / `manager` |
| DockYard's Compose project | the Compose project of the containers above; every other container in it (e.g. the reverse proxy) | `dockyard_project` |
| Their images | image IDs of the containers above | `dockyard_image` |
| Manager data volume | the manager container's volume at `/var/lib/dockyard` | `manager_data` |
| Agent state volume | the agent container's volume at `/var/lib/dockyard-agent` | `agent_state` |
| Stacks volume (#28) | `DOCKYARD_STACKS_VOLUME` (default `dockyard_stacks`) | `stacks` |
| Other volumes of DockYard containers | e.g. a local backup repository (#10), the proxy's data; volumes labeled with DockYard's project | `dockyard_volume` |
| Networks | the networks of the containers above (not `bridge`/`host`/`none`) and those labeled with DockYard's project | `dockyard_network` |

Detection works on a host with manager and agent (the co-located deploy
examples) and on a host with only an agent (`deploy/remote-agent`): tests
`TestHostWithManagerAndAgent`, `TestHostWithOnlyAnAgent`,
`TestSelfProtectionOnTwoHosts` (real sessions over in-memory Engines).
Detection against real Engines is not verified by automated tests any more
(`TestEngineSelfProtection` was removed on 2026-09-25).

## Decisions (`protection.Check`)

| Action | Connected agent | Manager / DockYard project containers | Volumes, images, networks |
| --- | --- | --- | --- |
| start, unpause | allowed | allowed | — |
| stop, pause, update/recreate, remove | refused | refused | remove refused |
| restart | refused (it would kill its own job) | only with `confirm: true` (`409 confirmation_required` otherwise: the UI disconnects while the manager restarts) | — |
| mount into a new container | — | — | refused, as is binding the Docker data root, a directory inside it or an ancestor |
| Compose deploy / stop / restart / down / remove of DockYard's project (#7) | — | refused: manager `stacks.Service` (via `resources.Service.ProjectProtection`) and agent (`Guard.GuardStacks` wraps the stack executors); start stays allowed | — |

The manager checks before enqueueing (so no job is created); the agent
checks again right before acting, whatever the manager sent (a job
enqueued past the API is refused by the agent, `TestSelfProtectionOnTwoHosts`).

## For other workstreams (exclusion API)

- **Prune (#14):** wired — `internal/agent/prune` identifies DockYard's
  objects with `guard.Identify` on every plan and again before every
  removal (`protection.Check` refuses them); previews list them as
  `protected` with the reason ([maintenance.md](maintenance.md)).
- **Updates (#20):** never add a protected container or project to an
  update policy or run (`protection.Excluded`); DockYard upgrades follow
  #34.
- **Backups (#10):** backup-time and restore shutdown plans skip protected
  containers (`protection.Filter`); the backup repository mount becomes a
  protected `dockyard_volume` once it is mounted into the agent.
- **Bulk selections and migrations (#35):** `protection.Filter` and show the
  excluded items with their reason. Migrations (wired): the source agent's
  preview marks DockYard's own project, images and volumes (a blocker, or
  an excluded volume with its reason), and `migration.send/stop` and
  `stack.remove_source` refuse them on the agent.
- **Stacks (#7):** wired — `stacks.Service` calls `ProjectProtection` before
  deploy, stop, restart, down and removal (`TestDockYardProjectIsProtected`);
  the agent's stack executors are wrapped by `Guard.GuardStacks`
  (`TestGuardStacks`).
