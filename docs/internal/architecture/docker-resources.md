# Docker resources: containers, images, volumes and networks (#6)

Users inspect and operate the Docker objects of each environment through
`/api/v1/environments/{environmentId}/{containers,images,volumes,networks}`.
The manager never talks to an Engine: reads are named agent requests, every
mutation is a job of the #26 engine, and the agent performs both through the
Moby adapter (#21). There is no Engine API passthrough.

| Package | Role |
| --- | --- |
| `internal/protocol` (`docker.go`) | Wire types of the requests and job inputs, the create-form validation shared by manager and agent, Docker Manager's label keys. |
| `internal/agent/resources` | Agent side: request handlers (`container.list`, `container.inspect`, `image.list`, `image.inspect`, `image.tag`, `volume.list`, `volume.inspect`, `volume.usage`, `network.list`, `network.inspect`) and job executors (`container.*`, `image.pull`, `image.remove`, `volume.*`, `network.*`). Wired by `internal/agent/runtime`. |
| `internal/manager/resources` | Manager side: requests through the hub, job requests, input validation, stack/in-use refusals, recreate specifications, permission Locators, reconnect reconciliation, the #19/#7 hooks. |
| `internal/manager/api` (`docker.go`, `containers.go`, `images.go`, `volumes.go`, `networks.go`) | The routes, DTOs, #17 shaping. |
| `internal/agent/engine/enginefake` | In-memory `engine.Engine` for Docker-free tests (agent unit tests, API tests, app tests with real agent sessions). |

## Identity and environment scoping

- Every route is under `{environmentId}` and every lookup goes to that
  environment's own agent, which asks its own Engine. A container ID of
  another environment is simply not found (tests with two environments
  holding a container of the same name: `TestContainersAcrossEnvironments`,
  `TestDockerOperationsThroughAgents`, `TestEngineTwoAgentsEnrollAndServeJobs`).
- Path identifiers: containers by name, ID or unique ID prefix; images by ID
  (`sha256:<hex>` or 12+ hex digits: references contain `/` and can move);
  volumes by name; networks by name, ID or ID prefix.
- Permission rules (#17) name Docker objects by environment + name
  (containers, volumes, networks) and images by ID. A job's target is the
  container/volume/network name, the pulled reference or the removed
  image ID. Container jobs also carry the container ID the manager
  resolved: a container recreated under the same name in between is never
  touched (`container_replaced`).
- The environment must be visible to the caller (404 before any agent is
  asked); an offline environment answers `503 environment_offline`, an
  agent without the Engine `503 engine_unavailable`, an Engine below API
  1.44 `409 unsupported_api_version`, an agent that predates a request
  `501 agent_unsupported`, other Engine failures `502 engine_error`,
  agent timeouts `504 timeout`.

## Uptime, addresses and volume sizes

- `container.list` reports each container's network endpoints with their
  IPv4/IPv6 addresses (`networkList`; empty addresses while stopped) and,
  for running, paused and restarting containers, `startedAt`: the Engine's
  list has no start time, so the agent inspects those containers (at most
  eight at a time; one that vanished meanwhile simply has none). The API
  `Container` shows them as `startedAt` and `networks` (full view); stack
  services (`compose.services`) carry the same `networks` per container,
  and its `volumes`: the volume mounts (name, destination, read-only;
  never bind mounts or tmpfs), `anonymous` when the name has the form the
  Engine gives anonymous volumes (64 hex digits,
  `protocol.AnonymousVolumeName`; no volume inspect per request). The API
  `StackContainer` shows them as `volumes` (full view, like `networks`).
  Older agents omit the fields; the manager and the UI show "—".
- `container.inspect` reports the on-failure restart policy's maximum
  retry count (`restartMaxRetries`; absent when unlimited or for other
  policies); the API `ContainerDetails` shows it as `restartMaxRetries`
  and the UI as "On failure (up to 5 retries)".
- `network.inspect` reports each attached container's addresses on the
  network (`ipAddress`, `ipv6Address`); the API `Network.containers`
  carries them (GET only). For older agents, which omit them, the UI takes
  the addresses from the containers list.
- `GET …/disk-usage/volumes` (`list-volume-usage`, `volume.read`) lists the
  size of every volume the caller reads in full. The agent request
  `volume.usage` asks the Engine's disk usage report, which walks every
  local volume, so `resources.Service.VolumeUsage` keeps one answer per
  environment for a minute (`VolumeUsageTTL`) and lets concurrent callers
  share one computation (detached from the request, bounded to two
  minutes; failures are not cached). An agent that does not advertise
  `volume.usage` is never asked (`supported: false`); sizes the Engine does
  not know (other drivers) are absent.

## Shaping (#17)

The type's read capability (`container.details.read`, `image.read`,
`volume.read`, `network.read`) shows everything; any other capability on
the object shows only its minimal fields (`catalog.ResourceType.Minimal`)
plus `view` and `actions`. Metrics-only and restart-only containers show id,
name, environmentId, state, health and stack/service identity. Label
filters apply to full views only. Environment variables are never returned
(inspect omits them; the saved specification shows variable names only).

Containers, volumes and networks of a Compose project that is a Docker Manager
stack carry the stack (and service) as authorization parents, so
stack-scoped rules apply to them. Handlers pass these parents; the
`container`, `volume` and `network` Locators serve the job engine's checks
from the membership last seen (list, inspect, reconnect reconciliation).
Removing an object through Docker Manager drops its exact rules
(`ForgetResource`) once the removal job succeeded.

## Mutations

| Route | Job kind | Refusals before the job |
| --- | --- | --- |
| `POST …/containers` | `container.create` (`create` → `connect_networks` → `start`) | invalid form (422), name in use (`resource_name_taken`), image not present (422 `body.image`: pull first), unknown network (422) |
| `PATCH …/containers/{id}` | `container.update` | recreate fields (`recreate_required`), stack container (`stack_managed`) |
| `DELETE …/containers/{id}?force&removeVolumes` | `container.remove` | stack container (`stack_managed`), running without force (`container_running`) |
| `POST …/containers/{id}/{start,stop,restart,pause,unpause}` | `container.<verb>` | — (already in the target state: the job succeeds without change) |
| `POST …/images/pulls` | `image.pull` | invalid reference/platform; unknown or mismatching `registryConnectionId` (422), ambiguous match (`ambiguous_registry_connection`), revoked connection (`registry_connection_revoked`) |
| `DELETE …/images/{id}?force` | `image.remove` | used by any container (`image_in_use`); several tags without force (409) |
| `POST …/images/{id}/tags` | — (bounded `image.tag` request, 200) | invalid target |
| `POST …/volumes` / `DELETE …/volumes/{name}` | `volume.create` / `volume.remove` | name in use; stack volume (`stack_managed`), mounted (`volume_in_use`) |
| `POST …/networks` / `DELETE …/networks/{id}` | `network.create` / `network.remove` | name in use or predefined; predefined (`network_builtin`), stack network (`stack_managed`), attached containers (`network_in_use`) |

Every mutation accepts `Idempotency-Key` (passed to the job engine) and is
audited automatically (#30) with its target. The agent re-checks
`stack_managed` and in-use conditions right before it acts, whatever the
manager decided.

Stack-managed means: the container's Compose working directory lies in one
of the agent's verified stack roots (#28), or the #7 stack resolver knows
the project. Runtime actions (start, stop, restart, pause, unpause) on stack
containers are allowed; updates and removals must go through the stack.

### Job error classes

Engine and registry failures keep the adapter's stable code as the job's
error class, with operator guidance: `not_found`, `conflict`,
`unauthorized`, `forbidden`, `rate_limited`, `registry_unavailable`,
`unsupported_api_version`, `engine_unavailable`, `timeout`,
`invalid_argument`, `engine_error`. Agent refusals: `stack_managed`,
`image_in_use`, `volume_in_use`, `network_in_use`, `network_builtin`,
`resource_name_taken`, `container_replaced`, `invalid_input`. Executors
return them as `jobexec.ClassedError`.

## Create form and recreation

The v1 create form has the common options only: image, name, command,
entrypoint, env, labels, working directory, user, ports, mounts (bind,
volume, tmpfs), networks (the first is the network mode; others are
connected after creation), restart policy, resources (CPUs, CPU shares,
memory, memory+swap, PIDs), health check and whether to start. Anything
else (privileged mode, capabilities, devices, security options, ulimits,
sysctls, log drivers, DNS, extra hosts, init, IPC/PID modes, GPUs, ...) is
refused by the schema: use a Compose stack (#7). Labels under
`dev.neureka.docker-manager.` and `com.docker.compose.` are reserved. Binding the
Docker socket (or a directory containing it) is refused. Bind mounts
otherwise give the container access to host files: `container.create` is
an advanced capability.

- In place (`PATCH`, `container.update`): restart policy and resources.
- Needs recreation (refused with `recreate_required`): name, image,
  command, entrypoint, env, labels, working directory, user, ports,
  mounts, networks, health check.

Containers Docker Manager creates carry `dev.neureka.docker-manager.managed=standalone`,
`dev.neureka.docker-manager.instance=<manager instance ID>` and
`dev.neureka.docker-manager.spec=<spec ID>`. The complete create form (including
environment values) is saved sealed (`secrets.Keyring`,
`managed_containers/<id>/spec`) in `managed_containers`, updated with
in-place changes and when a stack rename (#7) moved a volume it mounts
(`resources.Service.StackRenamed`, a `stacks.Service.OnRenamed` hook in the
rename's finishing transaction, rewrites the mount sources to the new
volume names; the agent recreated the container with the same name and
labels), and dropped when the container is removed through Docker Manager
or when a reconnect shows the container gone after its create job ended.
Automatic updates (#20) recreate a managed standalone container from
`resources.Service.ManagedSpec` with its unchanged tagged reference.
The create job's input also holds the form (manager job record and the
agent journal until acknowledged); neither is exposed by the API, audit or
logs.

## Hooks for other workstreams

- **#19 registry connections:** pulls select the connection with
  `registries.Service.Select` (explicit `registryConnectionId`, else the
  matching one; none = anonymous) and put only its ID into the input
  (`registryConnections`, `jobspec.CredentialRefs`). The job engine
  resolves it to the credential at every dispatch; the agent executor
  takes it from the attempt (`regauth.ForReference`) and never pulls
  anonymously when a connection was named (`credential_unavailable`).
- **#7 stacks:** `resources.Service.SetStackResolver` maps Compose projects
  to stack IDs: they become authorization parents and count as managed.
- **#32 self-protection:** Docker Manager's own resources carry `protection`
  and are refused on both sides ([self-protection.md](self-protection.md)).
- **#5:** container metrics (`GET …/containers/{id}/metrics`, served here
  from the #5 store by container name; readable while the environment is
  offline) and Docker event ingestion (live invalidation of these views,
  #23).

## Tests

- Agent: `internal/agent/resources` (requests, error codes, every executor
  with enginefake). The operations against real Engines and a registry
  with auth and injected 429 are not verified by automated tests any more
  (the integration test was removed on 2026-09-25).
- Manager: `internal/manager/resources` (Locators, reconciliation,
  forget-on-success, error mapping); `internal/manager/api/docker_test.go`
  (authztest matrices for Restricted, metrics-only and restart-only incl.
  stack scope, two environments, offline and Engine errors, validation,
  lifecycle, images/volumes/networks); `internal/manager/app/docker_test.go`
  (real agent sessions: jobs end to end, pull failure classes, sealed
  specs, API token restart-only, metrics-only user).
- Docker events of the operations (#5 relay): enginefake emits the
  Engine's events for every Docker API operation (`Events`,
  `EmittedEvents`); `TestDockerOperationsReachTheEventBus` (app, real
  agent session with the event relay) shows container lifecycle,
  creation/removal, image pull/tag/removal and volume/network
  creation/removal reaching the manager's bus as `docker.event` of their
  environment only.
- Two real agents on two Engines: `TestEngineTwoAgentsEnrollAndServeJobs`
  (also: each restart's and a volume creation's Docker event on the
  manager's bus, per environment).
