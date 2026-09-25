# Engine integration: Moby SDK and Compose SDK on the agent

How the agent talks to Docker Engine (#21, #2 Compose SDK spike, #19
credentials, #33 builds). Supported Engines, operations and Compose
features: [../support-matrix.md](../support-matrix.md).

## Boundaries

Only two packages touch Docker Engine, both on the agent:

| package | SDK | used for |
| --- | --- | --- |
| `internal/agent/engine` | `github.com/moby/moby/client` + `api`, BuildKit session | every direct Engine operation, builds |
| `internal/agent/compose` | `github.com/docker/compose/v5` (+ `docker/cli`, `compose-go`) | Compose project lifecycle |

The manager never connects to an Engine and links none of these modules
(`scripts/build-static.sh` fails otherwise). Everything else in the agent
uses the `engine.Engine` interface and the domain-neutral types in
`internal/agent/engine/types.go`; no SDK type crosses the package boundary.

## Pinned versions

| module | version | note |
| --- | --- | --- |
| `github.com/moby/moby/client` | v0.6.0 | `MaxAPIVersion` 1.56, `MinAPIVersion` 1.40 |
| `github.com/moby/moby/api` | v1.56.0 | |
| `github.com/docker/compose/v5` | v5.5.1 | its go.mod asks for client v0.5.1 / api v1.55.0; Go's minimal version selection uses the pins above, verified by the tests below |
| `github.com/docker/cli` | v29.7.2+incompatible | exactly what compose v5.5.1 requires |
| `github.com/moby/buildkit` | v0.33.0 | exactly what compose v5.5.1 requires |
| `github.com/containerd/containerd/v2` | v2.3.5 | one patch above compose's v2.3.4 to fix GO-2026-6444 (govulncheck) |

All pins are exact in `go.mod`. `scripts/build-static.sh` builds the agent
with `CGO_ENABLED=0` for linux/amd64 and linux/arm64 and checks from the
binary's build info that it links exactly these versions of the Moby
client/API, Compose SDK, docker/cli and BuildKit (CI job `static-build`).
The client is pre-1.0: bump the Moby, Compose, docker/cli and BuildKit
modules together, re-run the extended Engine matrix and update the support
matrix.

## Engine adapter (`internal/agent/engine`)

- `engine.Connect(ctx, Options{Host})` creates the SDK client, forces API
  version negotiation with a ping (the SDK otherwise negotiates lazily and
  silently falls back to its maximum version when negotiation fails),
  refuses Engines below `MinSupportedAPIVersion` (1.44, Docker Engine 25.0)
  and non-Linux Engines, and loads the `Identity`: Engine ID, name, version,
  API/min API, negotiated API, OS/arch, `DockerRootDir`, storage driver,
  cgroup version, security options, rootless and Docker Desktop detection
  and the capability list of the planned v1 operations.
- Operations: container list/inspect/create/start/stop/restart/pause/
  unpause/kill/update/wait/remove, image list/inspect/pull/tag/remove/load,
  volume and network list/inspect/create/remove (+ connect/disconnect),
  events, logs, stats, exec create/attach/resize/inspect, `Build`.
- Bounds: every non-streaming call gets `RequestTimeout` (default 60 s; stop
  and restart add the grace period). Streams (`Events`, `Logs`, `Stats`,
  `AttachExec`, `PullImage`, `Build`) live exactly as long as their context
  and end when the callback returns an error, which is returned unchanged.
  Canceling the context closes the HTTP stream; the error code is
  `canceled`.
- Environment variables are not part of `ContainerDetails`: they often hold
  secrets (#7).
- `CreateNetwork` refuses an existing name with `conflict` on every Engine
  (Docker 24 would otherwise create a duplicate).
- `Close` releases idle connections; running streams end with their
  contexts.

### Error model

Every error is an `*engine.Error{Code, Op, Message}`. `Code` is stable and
part of the agent contract (job results, capability frames):

| code | when |
| --- | --- |
| `not_found` | missing container/image/volume/network, `manifest unknown` |
| `conflict` | name in use, removing a running container, volume or network in use |
| `invalid_argument` | bad spec (mount type, platform, Git URL, Dockerfile outside the context) |
| `unauthorized` / `forbidden` | registry 401 (`no basic auth credentials`, `authentication required`, `pull access denied`) / 403 |
| `rate_limited` | registry 429 / `toomanyrequests` |
| `registry_unavailable` | registry 502/503/504 |
| `unsupported_api_version` | Engine API below 1.44 (or below the client's 1.40) |
| `unsupported` | not implemented by this Engine, non-Linux Engine |
| `engine_unavailable` | socket unreachable, daemon down |
| `timeout` / `canceled` | request deadline / context canceled |
| `build_failed` | BuildKit reported an error |
| `invalid_project`, `unsupported_compose_feature`, `dependency_failed` | Compose adapter (below) |
| `engine_error` | anything else (message kept for diagnostics) |

Registry failures arrive as messages inside pull/build streams, usually
without a status code, so the adapter classifies the message text; the
classification is unit-tested with the Engine's real messages and
integration-tested against the registry fixture's fault proxy.

### Registry credentials (#19)

- Credentials come from the manager's registry connections inside one job
  command (`protocol.CommandSecrets`, [registries.md](registries.md));
  executors convert them with `internal/agent/regauth`.
- Pulls: `PullOptions.Auth` is encoded to `X-Registry-Auth` for that one
  request (base64 JSON in memory only). `RegistryAuth.Password` and
  `IdentityToken` are `logging.Secret`, so they redact in logs.
- Builds: base-image credentials are served over a BuildKit session that
  the adapter opens on the Engine connection (`/session`, hijacked HTTP/2).
  It wraps BuildKit's Docker auth provider with an in-memory credential
  lookup and disables the client-side token authority, whose key seeds
  BuildKit would otherwise write into the Docker config directory.
- Nothing is written to disk: `TestSessionAuthServesCredentialsFromMemory`
  and the Compose test below assert empty `HOME`/`DOCKER_CONFIG` trees.

### Builds (#33)

Builds always use the Engine's BuildKit (`version=2` on `/build`), never
the legacy builder and never buildx:

- local context: the directory is archived by the adapter (`.dockerignore`
  honored, the Dockerfile and `.dockerignore` kept, symlinks archived as
  links, UID/GID 0) and uploaded; `dockerfile_inline` is added to the
  archive;
- Git context: `http(s)` URLs with `#ref[:subdir]` are fetched by the
  Engine's BuildKit itself (`remote`); SSH URLs and URLs with embedded
  credentials are refused (private Git credentials are #33's job);
- progress: BuildKit status messages (`moby.buildkit.trace`) are decoded
  into `BuildEvent`s (step started/cached/done/error and step logs); the
  result is the image ID from `moby.image.id`.

## Compose adapter (`internal/agent/compose`)

The Compose SDK is built on a docker/cli `command.Cli`. The adapter
implements that interface itself (`memoryCLI`) instead of using
`command.NewDockerCli` + `Initialize`, which would read `~/.docker` and may
execute credential helpers or CLI plugins:

- one fresh in-memory `configfile.ConfigFile` per operation with only that
  operation's credentials; no file name (`Save` fails), no `credsStore`, no
  `credHelpers`; `DOCKER_AUTH_CONFIG` is removed from the agent environment
  at startup;
- `BuildKitEnabled()` is false, so the SDK never looks up or executes the
  buildx plugin; services with a `build:` section are built by the adapter
  through the Engine adapter's BuildKit *before* the SDK runs and marked
  `pull_policy: never` for that operation (without buildx the SDK would fall
  back to the deprecated legacy builder and shell out to `git` for Git
  contexts);
- destructive SDK prompts (e.g. recreating a volume whose configuration
  changed) are answered "no": data is never removed implicitly;
- `Stop` returns only once the Engine's container list no longer shows the
  stopped services as running: Engines before 26 can lag, and the SDK's next
  `Start` would skip a container it still believes running (seen on 24.0.9
  and 25.0.5; polling uses the injectable clock, 30 s bound);
- SDK output and logrus messages are routed to the agent logger at debug
  level; progress events are forwarded as `compose.Event`.

Loading (`Adapter.Load`) uses compose-go directly with the project
directory as working directory:

- only the project's env files (`.env` or explicit ones inside the project
  directory) feed interpolation — the agent's own environment (which holds
  its enrollment token) never does;
- Compose files are found in the project directory only (no parent-directory
  search); explicit file and env-file paths must stay inside it;
- remote `include` sources (Git/OCI) are not loadable (no remote loaders);
- the labels the Compose CLI sets (`com.docker.compose.project`, `.service`,
  `.project.working_dir`, `.project.config_files`, ...) are added, so
  `docker compose` recognizes DockYard's projects and vice versa;
- unsupported features are rejected with `unsupported_compose_feature`
  (list in the support matrix); the obsolete top-level `version:` produces a
  warning.

Operations: `Up` (with build), `Down`, `Start`, `Stop`, `Restart`, `Ps`,
`Pull`, `Build`. The SDK's own dependency engine orders starts and stops and
waits for `service_started`, `service_healthy` and
`service_completed_successfully`; an unhealthy dependency or a failed
one-shot returns `dependency_failed`. `required: false` dependencies on
disabled services are ignored; `restart: true` restarts dependents when a
dependency is restarted or recreated.

## Agent runtime

`runtime.Agent` connects to the Engine (and opens the Compose adapter) at
startup, retries with exponential backoff (2 s .. 60 s) while the Engine is
unreachable or unsupported, pings it on every health tick and refreshes the
identity after an outage. It logs the identity (`engine_id`,
`engine_version`, `api_version`, `negotiated_api_version`,
`docker_root_dir`, `rootless`, `docker_desktop`, ...). The session transport
(#3) calls `Agent.Capabilities()` for its hello/capabilities frame and
`Options.OnCapabilities` for updates; `EngineError` carries the stable code
when the Engine cannot be used. The health file records the Engine status
(`connected` or the error code); an unreachable Engine does not make the
agent container unhealthy (it keeps reporting to the manager).

After connecting, the agent runs the host storage check (`internal/agent/storage`,
#28) and passes `Agent.StackGuard` to the Compose adapter (`compose.Options.Guard`):
no project is loaded or deployed from a directory outside a verified stack
root. The result feeds the capabilities (`roots`, the `stacks` feature,
`diagnostics`) and `health.json` (`storage`). Operator guide:
`docs/deployment.md` ("Host storage layout").

## Repository checks

| check | where | rule |
| --- | --- | --- |
| legacy module | `scripts/policy-check.sh`, depguard | no `github.com/docker/docker` import, in `go.mod`, or anywhere in `go list -deps ./cmd/...` |
| CLI execution | `scripts/policy-check.sh`, forbidigo | no `exec.Command`/`LookPath` of docker, docker-compose, buildx or docker-credential-*; forbidigo forbids `os/exec` outright except in tests and `internal/testharness` (add restic's runner when #10 lands) |
| direct Engine HTTP | `scripts/policy-check.sh` | no Docker socket literal or raw Engine API path (`/_ping`, `/containers/json`, `/v1.NN/...`, ...) outside the exceptions below |
| SDK boundary | `scripts/policy-check.sh`, depguard `sdk-boundary` | Moby client/API, Compose SDK, docker/cli, BuildKit and compose-go only in `internal/agent/engine`, `internal/agent/compose`, `internal/testharness`, `test/` and `*integration_test.go` files (which drive the harness fixtures) |
| graph | `scripts/build-static.sh` | agent links the pinned SDK versions, manager links none |

Documented exceptions to "direct Engine HTTP": `internal/agent/engine/`
(the adapter and its fake Engine `enginetest` for unit tests),
`internal/agent/compose/*_test.go` (tests scripting that fake Engine),
`internal/agent/config/config.go` (the `DOCKER_HOST` default value only),
`internal/testutil/fscorpus/` (a path-traversal test string),
`internal/testharness/` (DinD readiness probes and the agent container's
mounts in the CI fixtures), `test/deploy/*_test.go` (tests asserting that
the deploy examples mount the socket) and `internal/manager/agents/install.go`
(the agent install command shown to operators, which bind-mounts the socket
into the agent container; the manager never dials it).

## Tests

- Unit (Docker-free): `internal/agent/engine` against `enginetest`, a
  scripted fake Engine API over TCP (negotiation per API version, old-Engine
  refusal, identity, error mapping, pull auth header, logs demultiplexing,
  stats math, events, BuildKit trace decoding, context archive, session
  auth); `internal/agent/compose` (loading, env isolation, rejected
  features, build mapping, `TestCredentialsNeverTouchDisk`);
  `internal/agent/runtime` (connection, backoff, capabilities).
- Integration (`-tags integration`, extended workflow): `TestEngine*` in
  `engine-matrix` for every Engine of `test/matrix/engines.json`
  (`TestEngineAdapterNegotiatesAndIdentifies`, `TestEngineOperations` with
  one subtest per v1 operation, `TestEngineComposeLifecycle`,
  `TestEngineAgentImage`); `TestCompose*` in `compose-fixtures`
  (depends_on conditions and restart propagation, unhealthy dependency,
  failed one-shot, local build, private-registry pull). Containers run the
  hermetic workload image (`test/fixtures/workload`,
  `testharness.WorkloadImage`); no test pulls from Docker Hub inside DinD.
