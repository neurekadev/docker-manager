# Self-protection: Docker Manager's own containers, images and volumes (#32)

Docker Manager cannot disable or delete itself through its own UI, API, API
tokens, policies or jobs, but it can import, redeploy and update its own
Compose project (see "Managing itself" below). Its resources appear in the inventory as
protected (badge "Docker Manager") with a reason, and destructive operations on
them fail with `409 protected` — for everyone, the owner included (no
override in v1; Docker on the host is the escape hatch).

| Package | Role |
| --- | --- |
| `internal/protection` | The decisions, shared by manager and agent: roles, `Check(p, action, confirmed)`, `Excluded(p)`, `Filter(items, protectionOf)`, `Refusal` (a `jobexec.ClassedError`: job class `protected` / `confirmation_required`). |
| `internal/agent/protect` | Identification on the agent: `Guard` (own container ID, manager identity, stacks volume), `Identify` → `Set` (containers, images, volumes, networks, Compose projects, Docker data root), the `manager.identity` request handler, `AcceptGeneration` (refuses a manager with a lower generation than `<state>/manager.json` at the welcome and at `manager.identity`, see [manager-move.md](manager-move.md)). |
| `internal/selfid` | The ID of the container the process runs in, from `/proc/self/mountinfo` (fallback `/proc/self/cgroup`); used by the agent (itself) and the manager (co-located manager). |
| `internal/agent/resources` | Annotates every inventory answer with `protocol.Protection` and checks before every destructive step. |
| `internal/manager/resources` (`protection.go`) | Manager-side checks before a job exists (`ContainerProtection` adds the role labels and the manager's own container ID to the agent's annotation), `ProjectProtection`, `ProtectedContainers`. |

## Identification (per Engine, at every inventory read and job step)

| Resource | How | Role |
| --- | --- | --- |
| The connected agent's container | its own container ID (`selfid`, verified against the container list) | `agent` (self) |
| A co-located manager | the manager's container ID from `manager.identity` (sent by the manager after every reconnect; the manager finds its own ID with `selfid`) | `manager` (self) |
| Other Docker Manager containers | label `docker-manager.role=agent|manager` (the documented compose files and the `docker run` install command), or its legacy key `dev.neureka.docker-manager.role` (files and commands written before 2026-09-28; `protocol.HasRole`) — also when no ID matched (another installation, or detection failed) | `agent` / `manager` |
| Docker Manager's Compose project | the Compose project of the containers above; every other container in it (e.g. the reverse proxy) | `docker_manager_project` |
| Their images | image IDs of the containers above | `docker_manager_image` |
| Manager data volume | the manager container's volume at `/var/lib/docker-manager` | `manager_data` |
| Agent state volume | the agent container's volume at `/var/lib/docker-agent` | `agent_state` |
| Stacks volume (#28) | `DOCKER_AGENT_STACKS_VOLUME` (default `docker-manager_stacks`) | `stacks` |
| Other volumes of Docker Manager containers | e.g. the proxy's data; volumes labeled with Docker Manager's project | `docker_manager_volume` |
| Networks | the networks of the containers above (not `bridge`/`host`/`none`) and those labeled with Docker Manager's project | `docker_manager_network` |

`Identify` works from the container list alone (each entry carries its
networks with their IDs) and inspects no container: the Engine can block
an inspection for minutes while it removes a container on a loaded host,
and every listing identifies (#307). A list of one Compose project's
containers (`container.list` with `project`) identifies that project's
containers exactly as the full list does.

Detection works on a host with manager and agent (the Quickstart's
compose file) and on a host with only an agent ("Add more servers"): tests
`TestHostWithManagerAndAgent`, `TestHostWithOnlyAnAgent`,
`TestSelfProtectionOnTwoHosts` (real sessions over in-memory Engines).
Detection against real Engines is not verified by automated tests any more
(`TestEngineSelfProtection` was removed on 2026-09-25).

## Decisions (`protection.Check`)

| Action | Connected agent | Manager / Docker Manager project containers | Volumes, images, networks |
| --- | --- | --- | --- |
| start, unpause | allowed | allowed | — |
| stop, pause, update/recreate, remove | refused | refused | remove refused |
| restart | refused (it would kill its own job) | only with `confirm: true` (`409 confirmation_required` otherwise: the UI disconnects while the manager restarts) | — |
| mount into a new container | — | — | refused, as is binding the Docker data root, a directory inside it or an ancestor |
| Compose stop / restart / down / remove of Docker Manager's project (#7) | — | refused: manager `stacks.Service` (via `resources.Service.ProjectProtection`) and agent (`Guard.GuardStacks` wraps the stack executors); start stays allowed | — |
| Compose deploy (redeploy, pull, force recreate) and digest updates (#20) of Docker Manager's project | allowed: the agent's own service goes to a helper container after the job | allowed | — |

The manager checks before enqueueing (so no job is created); the agent
checks again right before acting, whatever the manager sent (a job
enqueued past the API is refused by the agent, `TestSelfProtectionOnTwoHosts`).

## For other workstreams (exclusion API)

- **Prune (#14):** wired — `internal/agent/prune` identifies Docker Manager's
  objects with `guard.Identify` on every plan and again before every
  removal (`protection.Check` refuses them); previews list them as
  `protected` with the reason ([maintenance.md](maintenance.md)).
- **Updates (#20):** Docker Manager's own Compose project can have a stack
  policy (its members report `stack_managed`, not `protected`); protected
  standalone containers are never a target (`protection.Excluded`). The
  environment-wide policy skips protected standalone containers.
- **Backups (#10):** backup-time and restore shutdown plans skip protected
  containers (`protection.Filter`); the backup repository mount becomes a
  protected `docker_manager_volume` once it is mounted into the agent.
- **Bulk selections and migrations (#35):** `protection.Filter` and show the
  excluded items with their reason. Migrations (wired): the source agent's
  preview marks Docker Manager's own project, images and volumes (a blocker, or
  an excluded volume with its reason), and `migration.send/stop` and
  `stack.remove_source` refuse them on the agent.
- **Stacks (#7):** wired — `stacks.Service` calls `ProjectProtection` before
  stop, restart, down and removal (`TestDockerManagerProjectIsProtected`);
  the agent's stack executors are wrapped by `Guard.GuardStacks`
  (`TestGuardStacks`). `get-stack` reports `protection` for Docker Manager's
  own project; the UI shows the refused actions disabled.

## Managing itself

Docker Manager imports, redeploys and updates its own Compose project like any
other stack. An import by copy (`stack.import`) copies it while it runs and
neither stops nor recreates it (`import.live`, see the stacks guide); the
next deploy moves it onto the copy. The agent cannot recreate the container it runs in (Compose
would stop it in the middle of the job), so `internal/agent/selfupdate`
splits the work:

1. `stack.deploy` (`apply`) and `update.run` (`recreate`) ask the
   `Launcher` for the agent's own service (`OwnService`: the Compose labels
   of its own container). `Handoff` moves that service and every service
   depending on it out of the job; the job converges the rest (the manager
   container may be recreated meanwhile: the agent keeps running and
   journals the outcome, #26). A deploy that would remove the agent as an
   orphan (its service dropped with `removeOrphans`) is refused.
2. The step schedules a `Plan` (the exact definition bytes the job loaded)
   and reports an `agent_self_update` warning. The job's applied images
   name, for the handed-off services, the image their reference names
   after the pull (the one the helper runs), not the old container that
   still runs when the job reports; otherwise the stack would show image
   drift for the agent after every self-update.
3. After the job's outcome is journaled and sent (`jobs.Options.OnFinished`),
   a succeeded job starts a helper container: the agent's image and mounts,
   `docker-agent self-update <plan>`, label
   `docker-manager.role=self-update` (protected like an agent),
   auto-removed, no health check. A failed job drops the plan.
4. The helper waits `DefaultGrace`, runs Compose up for those services
   without recreating their dependencies, starts the previous agent again
   if Compose fails, and writes a result the next agent logs at startup
   (`Collect`).

Tests: `TestHandoff`, `TestLauncher`, `TestCollect`
(`internal/agent/selfupdate`), `TestDeployOfDockerManagerHandsTheAgentOver`,
`TestDeployReportsTheHandedOverAgentsNewImage` (`internal/agent/stacks`), `TestGuardStacksLetsDockerManagerUpdateItself`,
`TestDockerManagerProjectCanBeUpdated`.
