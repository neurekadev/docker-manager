# Support matrix

What DockYard v1 supports on a host, with the evidence behind it. Engine
integration design: [architecture/engine-integration.md](architecture/engine-integration.md).
Decisions are recorded in #25 (Q2 host boundary, Q5 file watching).

## Docker Engine versions

Tested by the extended workflow's `engine-matrix` job against the official
`docker:<version>-dind` images pinned in `test/matrix/engines.json`, on
`ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` (arm64) runners, with the
agent's Moby client v0.6.0 (API ≤ 1.56). Each v1 operation is a subtest of
`TestEngineOperations`; Compose and the agent image have their own tests.

Results are identical on amd64 and arm64 for 25.0.5, 28.5.2 and 29.8.1;
24.0.9 was tested on amd64 before it was dropped from the matrix.

| operation (test) | 24.0.9 (API 1.43) | 25.0.5 (API 1.44) | 28.5.2 (API 1.51) | 29.8.1 (API 1.56) |
| --- | --- | --- | --- | --- |
| API negotiation + identity (`TestEngineAdapterNegotiatesAndIdentifies`) | pass (negotiated 1.43) | pass (1.44) | pass (1.51) | pass (1.56) |
| container create/inspect/list/update/remove (`container.crud`) | pass | pass | pass | pass |
| start/stop/restart/pause/unpause/kill/wait (`container.lifecycle`) | pass | pass | pass | pass |
| private-registry pull with auth, 401/429/404 (`image.pull.auth`) | pass | pass | pass | pass |
| image list/inspect/tag/remove (`image.crud`) | pass | pass | pass | pass |
| volumes (`volume.crud`) | pass | pass | pass | pass |
| networks (`network.crud`) | pass¹ | pass | pass | pass |
| events stream (`events.stream`) | pass | pass | pass | pass |
| logs stream, follow, cancel (`logs.stream`) | pass | pass | pass | pass |
| stats stream (`stats.stream`) | pass | pass | pass | pass |
| exec, stdin, exit code, TTY resize (`exec`) | pass | pass | pass | pass |
| BuildKit build, local context (`image.build.local`) | pass | pass | pass | pass |
| BuildKit build, Git context (`image.build.git`) | pass | pass | pass | pass |
| BuildKit build, private base image via session auth (`image.build.private_base`) | **fail**² | pass | pass | pass |
| Compose up with build, depends_on healthy, restart propagation, stop/start, down (`TestEngineComposeLifecycle`) | pass³ | pass³ | pass | pass |
| agent image connects, healthy, no listener (`TestEngineAgentImage`) | pass | pass | pass | pass |

¹ Docker 24 accepts a second network with an existing name (API < 1.44
does not check duplicates by default); the adapter checks names before
creating, so `CreateNetwork` returns `conflict` on every Engine.
² Docker 24's BuildKit ignores the daemon's `insecure-registries` for base
images: `FROM <plain-HTTP registry>/…` fails with "server gave HTTP response
to HTTPS client". Fixed in Docker 25.
³ Engines before 26 can return from a container stop while their container
list still reports the container as running; the Compose SDK's next start
then skips it and fails waiting for dependencies. The adapter waits for the
list to settle after every stop (`awaitStopped`), after which the lifecycle
passes on 24.0.9 and 25.0.5.

Evidence: extended runs
[36066420980](https://github.com/neurekadev/dockyard/actions/runs/36066420980)
(amd64, all four Engines, including 24.0.9) and
[36067416403](https://github.com/neurekadev/dockyard/actions/runs/36067416403)
(amd64 + arm64, 25.0.5 / 28.5.2 / 29.8.1). The job summaries list every
test and subtest per Engine.

### Minimum Engine (recommendation for #25 Q2)

**Docker Engine 25.0 (API 1.44)** is the lowest tested Engine that passes
every planned v1 operation; 24.0.9 fails BuildKit base-image pulls from
insecure registries (²). The agent enforces it: Engines below API 1.44 are
refused with `unsupported_api_version` and the agent reports the reason in
its capabilities (`engine.MinSupportedAPIVersion`, `test/matrix/engines.json`
role `minimum`). Docker 24 and 25 are end-of-life upstream; hosts should run
a maintained Engine (28 or 29 today).

## Hosts

| host | v1 | why |
| --- | --- | --- |
| Linux amd64 / arm64, standalone Docker Engine ≥ 25.0, default or custom data root with the identical-path volume mount | supported | the tested configuration (#28 verifies the mount at startup) |
| Rootless Docker Engine | unsupported | the data root lives in the user's home (`~/.local/share/docker`) and the socket in `$XDG_RUNTIME_DIR`, so the documented identical-path layout does not apply, and the agent's UID 0 is an unprivileged host user (#25 requires root for file access). Detected from the Engine's security options (`Identity.Rootless`); stack operations are refused (#28) |
| Docker Desktop (macOS, Windows, Linux) | unsupported | the Engine runs in a VM: volume paths and bind sources are VM paths, not host paths. Detected (`Identity.DockerDesktop`); stack operations are refused (#28) |
| NAS vendor Engines (Synology Container Manager, QNAP Container Station, Unraid, TrueNAS apps) | unsupported (untested) | vendor-patched Engines, often older than 25.0, custom data roots (e.g. `/volume1/@docker`); not in the matrix. They are refused below API 1.44; above it the #28 identical-path check decides |
| Windows Engines | unsupported | refused at connect (`unsupported`) |
| Docker Swarm | out of the roadmap | |

## Compose features

The agent loads projects with compose-go and runs them with the Compose SDK
v5.5.1 (`internal/agent/compose`). Verified by `TestCompose*`
(`compose-fixtures` job) and `TestEngineComposeLifecycle` (every Engine).

| feature | status |
| --- | --- |
| services with `image` or `build`, `command`, `environment`, `env_file`, `healthcheck`, `restart`, ports, labels | supported |
| `depends_on` with `service_started`, `service_healthy`, `service_completed_successfully` | supported (`TestComposeDependsOnConditions`: start order measured from the Engine's timestamps) |
| unhealthy dependency / failed one-shot | the dependent is not started; error `dependency_failed` (`TestComposeUnhealthyDependencyFails`, `TestComposeFailedOneShotBlocksDependents`) |
| `required: false` dependencies | supported; a required dependency on a disabled service is `invalid_project` |
| `restart: true` propagation | supported for restart and for recreation of a dependency |
| `.env` / `env_file` interpolation | project files only; the agent's own environment never feeds interpolation |
| profiles | supported (`ProjectSpec.Profiles`) |
| named volumes and networks (local drivers) | supported |
| relative bind mounts (`./data`) | resolved against the project directory; correct Engine paths require the #28 identical-path layout |
| Compose `configs` / `secrets` from files | supported by the SDK (file-based; no DockYard secret store, #25) |
| `include` / `extends` with local files | supported |
| remote `include` (Git, OCI) | not loadable (no remote loaders) |
| `post_start` / `pre_stop` hooks | run by the SDK through the Engine's exec API |
| private registry images | per-operation credentials in memory (`TestComposePrivateRegistryPull`, `TestCredentialsNeverTouchDisk`) |
| top-level `version:` | accepted with an "obsolete" warning |
| `use_api_socket` | **rejected**: would mount the Docker socket and copy registry credentials into the container |
| `provider` services, `models` | **rejected**: they execute external plugins / Docker Model Runner |
| `develop` / watch | ignored by deploy (DockYard's own watcher is #23) |

### Stack operations (#7)

| operation | how | evidence |
| --- | --- | --- |
| deploy (`stack.deploy`) | Compose SDK `up` from the on-disk bytes the job reports as the applied revision; missing images pulled/built, `pull: always` / `build: true` on request | `TestComposeStackDeployUpdateAndLifecycle` (written, runs in `compose-fixtures`), unit `internal/agent/stacks` |
| update (redeploy of a changed definition) | only changed services are recreated | same |
| start / stop / restart (`stack.*`) | the shared lifecycle over the deployed containers: dependencies first with condition waits (2 min per dependency), dependents first on stop, `restart: true` propagation | same; unit `internal/agent/lifecycle` |
| down (`stack.down`, stack deletion) | Compose SDK `down` by project name; volumes and files are kept | same |
| build (`stack.build`, and the deploy's `build_images` step) | the build sections through the Engine's BuildKit (not the Compose SDK's build path): every section on request (`noCache`, `pull`), missing images only in a deploy without `build: true`; streamed, credential-scrubbed progress; cancellation stops BuildKit (the interrupted image keeps its previous version); build timeout (default 1 h, at most 6 h); per-environment build cap | `TestComposeStackBuildRebuildAndCancel` (written, runs in `compose-fixtures`), unit `internal/agent/stacks` (`TestStackBuild*`, `TestDeployBuildsMissingImagesThroughTheSamePath`), `internal/manager/stacks` (`TestStackBuild*`) |
| unhealthy dependency / failed one-shot | deploy fails with `dependency_failed`; the dependent is not started; the last applied revision is kept | `TestComposeStackDeployUnhealthyDependency` (written) |

### Build keys (#33)

Build sections are built by the agent through the Engine's BuildKit before
the SDK runs (`TestComposeBuildLocalContext`, `image.build.*`).

| key | status |
| --- | --- |
| `context` (local directory) | supported; `.dockerignore` honored |
| `context` (http(s) Git URL, `#ref:subdir`) | supported for repositories BuildKit can fetch anonymously (fetched by the Engine's BuildKit); Git credentials apply to manual Git builds only in v1; SSH Git URLs rejected (SSH Git access is out of v1) |
| `dockerfile`, `dockerfile_inline` (local context), `args`, `target`, `labels`, `tags`, `no_cache`, `pull`, `extra_hosts`, `shm_size`, `cache_from` | supported |
| `network` | `default`, `host`, `none` only |
| `platforms` | only the Engine's own platform |
| `secrets`, `ssh` | **rejected** (v1) |
| `additional_contexts`, `cache_to`, `no_cache_filter`, `ulimits`, `privileged`, `entitlements`, `isolation`, `provenance`, `sbom`, multi-platform | **rejected** |
| Git context with `dockerfile_inline` | **rejected** |

## Known SDK limitations

- The Compose SDK reaches BuildKit only through the buildx CLI plugin (bake)
  and otherwise falls back to the deprecated legacy builder, which also
  shells out to `git` for Git contexts. DockYard therefore builds images
  itself with the Moby client's BuildKit API; bake-only features (service
  build contexts, multi-platform, build secrets/SSH, cache export) are not
  available in v1.
- Compose v5.5.1's own go.mod asks for Moby client v0.5.1 / API v1.55.0;
  DockYard pins v0.6.0 / v1.56.0 (minimal version selection) and verifies
  the combination with the tests above. The Moby client is pre-1.0: bump
  the SDK modules together and re-run the matrix.
- Registry errors reach the client as text inside the pull/build stream,
  usually without a status code; the adapter classifies the Engine's
  messages (`unauthorized`, `forbidden`, `rate_limited`, `not_found`,
  `registry_unavailable`). The registry's `Retry-After` is not passed
  through by the Engine.
- Private Git repositories for BuildKit Git contexts need credentials in the
  BuildKit session: manual Git builds (#33) serve the Git credential as the
  session secret `GIT_AUTH_HEADER.<host>`; Compose build sections with Git
  contexts get no Git credential in v1.
- Engine-version quirks handled by the adapter: duplicate network names on
  Docker 24 (¹), container list lag after stop before Engine 26 (³).

## Storage layout and volumes (#28)

The agent verifies the identical-path layout at startup
(`internal/agent/storage`, operator guide in
[deployment.md](deployment.md#host-storage-layout-28)); stack operations are
refused with a diagnostic when it does not hold.

| layout | status | evidence |
| --- | --- | --- |
| default data root, `/var/lib/docker/volumes` identical mount, stacks in `dockyard_stacks` | supported | `TestComposeStacksVolumeDefaultDataRoot`: a stack with `./data`, `env_file` and a local build context deploys from the stacks volume inside the agent image; the Engine mounts the real host path |
| custom data root with the matching identical mount | supported | `TestComposeStacksVolumeCustomDataRoot` (`--data-root /srv/docker-data`), `TestComposeAgentVerifiesStorageAtStartup` |
| default mount on a custom data root / volume directory mounted from another path | refused (`storage_mount_missing` / `storage_path_mismatch`) | `TestComposeMisconfiguredMountRefused`: diagnostic at startup, no deploy, the agent stays healthy |
| extra stack roots (`DOCKYARD_STACK_ROOTS`) at identical paths | supported; a mismatched root is refused on its own (`storage_root_mismatch`) | `TestComposeStackRoots` |
| rootless Engine, Docker Desktop | refused (`storage_rootless_engine`, `storage_docker_desktop`) | unit tests (`internal/agent/storage`) |

**Volume drivers (decision for #25 Q2/Q5):** only **local-driver volumes
stored under Docker's volume directory** are supported for file browsing
(#15), watching (#23) and backup (#10) in v1. Volumes of other drivers
(plugins such as `rexray`, `rclone`, cloud block storage) and local volumes
backed by remote storage (`type=nfs|nfs4|cifs|smb|…` or `o=addr=…`) are
listed read-only with the reason (`storage.Result.AccessFor`): their data is
not under the volume directory, or only while a container mounts it. A
short-lived helper container per operation was the alternative; it is
deferred past v1. The file manager (#15) additionally refuses the stacks
volume as a volume (stacks are browsed per stack, where the Compose source
rules apply) and every volume mounted by DockYard's own containers (label
`dev.neureka.dockyard.role`), answering `409 volume_files_unsupported`
([files.md](api/files.md#volumes)).

## File watching (#23)

The agent watches the file scopes the manager declares (`files.watch`):
every stack's project directory, and each volume with an open file view
(a live stream's `volume` filter, or 5 minutes after a listing). Changes
reach open views through the live stream ([streams.md](api/streams.md#file-changes));
external edits of a stack's Compose files become revisions (#25 Q1).
Implementation: `internal/agent/watch`; protocol:
[agent-v1.md](protocol/agent-v1.md#fs_invalidation-and-rescan-15-23).

| filesystem of the scope | how | target (#25 Q5) |
| --- | --- | --- |
| local filesystems of the Docker data root (ext4, xfs, btrfs, zfs, …) | inotify, one kernel watch per directory, debounced 200 ms; a safety reconciliation every 10 min and right after a kernel queue overflow | visible within 2 s at p95 |
| NFS, SMB/CIFS, FUSE, Ceph, Lustre, GPFS, 9p, AFS (statfs magic) | polled: bounded reconciliation scan every 30 s | within 60 s |
| scopes beyond the watch budget, or an agent without kernel notifications | polled like remote filesystems (`reason: watch_limit` / `notify_unavailable` in the `files.watch` answer and the manager log) | within 60 s |
| non-local volume drivers, DockYard's own volumes, the stacks volume as a volume | not watched (not served by the file manager, #28) | — |

**Measured latency** (`TestRealFilesystemLatency`, `internal/agent/watch`:
file create, append, rename and delete in a root and a nested directory,
real kernel notifier, 49 changes, time from the change to the
invalidation):

| platform | notifier | p50 | p95 | max |
| --- | --- | --- | --- | --- |
| windows/amd64 (development machine, NTFS) | ReadDirectoryChangesW | 200.6 ms | 200.9 ms | 201.0 ms |
| linux/amd64 (CI runner, ext4) | inotify | pending: runs in the `go` CI job, not executed while GitHub Actions is unavailable | | |

The latency is dominated by the 200 ms debounce; the manager's coalescing
window (250 ms, first event immediate) and the live stream add network
time only. The end-to-end check through the proxies is
`e2e/tests/live.spec.ts` (written; needs an agent environment).

**Watch limits.** inotify watches are per user and shared by every root
process on the host (containers included): `fs.inotify.max_user_watches`
defaults to 8 192 on older kernels and scales with memory (up to 1 048 576)
since Linux 5.11. The agent uses at most `DOCKYARD_WATCH_MAX` watches,
default half the kernel limit, clamped to 1 024 – 524 288 (8 192 when the
limit cannot be read). One watch per directory of every watched scope;
nested scopes share watches. A scope that does not fit is polled as a whole
and holds no watches; the kernel's own `ENOSPC` is treated the same. Each
watch costs about 1 KiB of unswappable kernel memory. For large trees raise
the host limit (`sysctl fs.inotify.max_user_watches=524288`) and
`DOCKYARD_WATCH_MAX`.

**Scan budgets.** A reconciliation scan walks at most 200 000 entries per
scope (without following symlinks) and keeps one 64-bit hash per
directory (names, sizes, modification times and modes of its entries), not
per file; the directories past the budget are not compared (the scope
reports `scan_truncated`; inotify still covers them in inotify mode, and
listings are always read live). A manager `rescan` (after a lost
notification sequence) walks at most 200 000 entries. One agent watches at
most 4 096 scopes; one invalidation names at most 256 paths (more become a
whole-scope overflow).
