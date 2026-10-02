# Support matrix

What Docker Manager v1 supports, with the evidence behind it (#12). Engine
integration design: [architecture/engine-integration.md](architecture/engine-integration.md).
Decisions are recorded in #25 (Q2 host boundary, Q5 file watching, Q9
topology, Q16 root containers).

## Verification status

Since 2026-09-25 (on Forgejo, and on GitHub again since 2026-09-30,
`https://github.com/neurekadev/docker-manager`) CI
(`.github/workflows/CI.yaml`, GitHub Actions) runs only format/lint,
isolated deterministic unit tests (in-memory fakes) and a test-free build.
The former GitHub CI's Docker-backed suites (Engine matrix, Compose
fixtures, registry/BuildKit, restic/MinIO storage, filesystem security on
Linux, fault injection, deploy smoke), the Playwright browser, proxy and
accessibility specs, fuzzing and the race detector were removed. The
support claims below stay the design target, but the following are **not
verified by automated tests any more**:

- real Docker Engine behaviour and the supported Engine version range;
- the Compose SDK against real Engines and Compose fixtures;
- registry pulls and BuildKit builds against real registries;
- restic against real local, MinIO or S3 repositories;
- the #28 storage layout on real hosts (default/custom data roots, stack roots);
- proxy topologies end to end (Caddy, Traefik, nginx);
- browser behaviour, accessibility and the PWA (installability, service worker);
- crash/kill recovery of manager and agent mid-job;
- the contents of the built images.

Results quoted below from the former GitHub CI are historical evidence and
have not been re-verified. What still runs: unit tests next to the code
(for example the adapters' request mapping, compose-go loading and
validation, storage decisions from the Engine's identity, the job engine
with fake agents) and the build of both executables for linux/amd64 and linux/arm64.

## Summary

| Area | Supported in v1 | Not supported |
| --- | --- | --- |
| Host OS / CPU | Linux on amd64 or arm64 (images for both) | Windows Engines, other architectures |
| Docker Engine | standalone Engine ≥ 25.0 (API ≥ 1.44); 25.0.5, 28.5.2 and 29.8.1 passed the former Engine matrix (not re-verified) | < 25.0 (refused), rootless Engines, Docker Desktop, NAS vendor Engines, Swarm, Kubernetes |
| Containers | both Docker Manager containers run as **root (UID 0)** | running them as a non-root user |
| Deployment | one public HTTPS origin behind an operator's TLS reverse proxy; agents dial out | extra domain names or ports for agents; agents that listen |
| Agents | one agent per Engine; co-located on the internal URL or remote over HTTPS | standby agents / failover (post-v1) |
| Browsers | current Chromium-based browsers, Firefox and Safari (build target below) | older browsers; Firefox cannot install the PWA |
| Manager / agent versions | agent of the same or the previous minor release as the manager | newer agents, older than N-1, other major versions |

## Architectures and the root-only requirement

- **linux/amd64** for both images; **linux/amd64 and linux/arm64** for both
  executables. CI's `Build` job builds static, CGO-free binaries for both
  architectures (`scripts/build-static.sh`, artifact `release-binaries-linux`);
  the arm64 binaries are built, not run or tested. linux/arm64 images are
  blocked until a native arm64 runner exists (no QEMU, no cross-built
  images).
- **Both containers run as root (UID 0)** (#25 decision 16, #28). The agent
  needs the Docker socket and root file access to stack and volume
  directories (owners, modes, backups); the manager image uses the same
  rule for a single support boundary. Running either as another user is
  **unsupported**: the agent refuses to start as non-root
  (`TestRefusesNonRoot`). That the images run as UID 0 follows from the
  Dockerfiles; no automated test starts the images any more.
- The **manager never mounts the Docker socket**; only the agent does, and
  the agent opens no listening socket (`TestAgentNeverListens`). Docker
  socket access is equivalent to root on that host.

## Artifacts

| Artifact | Contents | Published as |
| --- | --- | --- |
| `docker-manager` executable | API, embedded web UI, SQLite store, job engine; CGO-free, static | `ghcr.io/neurekadev/docker-manager:edge` (linux/amd64 and linux/arm64; BuildKit provenance and SBOM attestations) |
| `docker-agent` executable | Docker/Compose adapter, files, backups; CGO-free, static; no web UI, no listener | `ghcr.io/neurekadev/docker-agent:edge` (linux/amd64 and linux/arm64; BuildKit provenance and SBOM attestations) |
| restic | 0.19.1, SHA-256 verified per architecture (`deploy/docker/*.Dockerfile`), in both images | inside the images only |
| smartctl | smartmontools 7.5, a static binary built from the SHA-256 verified source release (`deploy/docker/agent.Dockerfile`, compiled natively per architecture); GPL-2.0, with `COPYING` and the source tarball in `/usr/share/doc/smartmontools/` ([ADR 0005](adr/0005-disk-health.md)) | inside the agent image only |

Only the rolling `:edge` tag is published from `main`; there are no git
tags, releases or semver images in this build (#25). The images on GHCR are
public and need no registry login. Neither image contains a Docker,
Compose or buildx CLI, Node or a shell toolchain; both are based on
`gcr.io/distroless/static-debian12` pinned by digest. This follows from
the Dockerfiles; no automated check inspects the built images any more.

### Pinned integration libraries

| Component | Version | Why pinned |
| --- | --- | --- |
| Moby Engine client (`github.com/moby/moby/client`) | v0.6.0 | the official Engine SDK; API version negotiation, all Engine calls through the agent's adapter |
| Moby API types (`github.com/moby/moby/api`) | v1.56.0 | matching types; the agent talks API 1.44–1.56 |
| Docker Compose SDK (`github.com/docker/compose/v5`) | v5.5.1 | in-process Compose lifecycle (no CLI) |
| compose-go (`github.com/compose-spec/compose-go/v2`) | v2.15.0 | project loading and validation |
| BuildKit client (`github.com/moby/buildkit`) | v0.33.0 | image builds through the Engine's BuildKit |
| restic | 0.19.1 | backup format and CLI behaviour; pinned with a per-architecture SHA-256 in `deploy/docker/*.Dockerfile` |
| smartmontools (smartctl) | 7.5 | the JSON output disk health parses, the `-n standby` exit status and the exit-status bits; pinned with the source tarball's SHA-256 in `deploy/docker/agent.Dockerfile` (bump version and checksum together) |
| Go toolchain | 1.27.1 | `go.mod`, `golang:1.27.1-alpine3.24` build image pinned by digest |

The legacy `github.com/docker/docker` module, hand-written Engine HTTP
clients and the Docker/Compose/buildx CLIs are forbidden by
`scripts/policy-check.sh` and the linters. Bumping the Moby client or the
Compose SDK means re-testing against real Engines by hand: there is no
automated Engine suite any more.

## Deployment topology

One public origin (for example `https://docker.example.com`) behind an
operator-managed TLS-terminating reverse proxy serves the web app,
`/api/v1` and `/agent/v1`. The manager listens on plain HTTP on the
internal network only and honours forwarded headers only from
`DOCKER_MANAGER_TRUSTED_PROXIES`. Remote agents dial the same origin over HTTPS
(certificate validated, redirects refused); an agent on the manager's
Docker network may use the internal URL with the explicit
`DOCKER_AGENT_MANAGER_ALLOW_HTTP` opt-in. The operator brings the proxy;
the repository ships no proxy examples and no proxy is exercised by
automated tests. Requirements and timeouts: [deployment.md](deployment.md).
A highly available manager is out of v1.

## Browsers

The web app is built for Vite 8's `baseline-widely-available` target:
**Chrome and Edge 111+, Firefox 114+, Safari and iOS Safari 16.4+**.

| Browser | Status |
| --- | --- |
| Chromium-based (Chrome, Edge, Brave), desktop and Android | supported, manual check; installable PWA; passkeys |
| Safari (macOS, iOS/iPadOS 16.4+) | supported, manual check; installable via *Add to Home Screen*; passkeys |
| Firefox (desktop) | supported, manual check; no PWA install (Firefox limitation) |
| Anything older than the build target | unsupported |

Passkeys, the service worker and secure cookies need the HTTPS origin
(`http://localhost` only for development). No real browser runs in the
automated tests: UI components are tested with Vitest in jsdom, so browser
behaviour, accessibility and PWA installation are checked by hand.

## Manager/agent protocol compatibility

- The agent protocol is `docker-manager.agent/v1` ([protocol/agent-v1.md](protocol/agent-v1.md)),
  versioned separately from `/api/v1`.
- **Version window:** a manager serves agents of its own minor release and
  of the previous one (N-1). An agent newer than its manager, older than
  N-1, of another major version or with an unparsable version is refused
  (session close `4426`, enrollment `426 version_unsupported`) with a
  message saying what to upgrade. Upgrade the manager first, then agents
  ([operations/upgrades.md](operations/upgrades.md)).
- New protocol fields reach an agent only after it announces the matching
  capability feature, so an N-1 agent never sees a field it predates.
- Rolling `:edge` builds all report `0.0.0-edge` and count as the same
  version: upgrade manager and agents together. A real N-1 image test is a
  manual release check (there are no versioned images yet).

## Docker Engine versions

**Historical evidence, not re-verified.** The table records the results of
the former GitHub CI's Engine matrix, removed on 2026-09-25 with the move to
Forgejo: official `docker:<version>-dind` images on `ubuntu-24.04` (amd64)
and `ubuntu-24.04-arm` (arm64) runners, with the agent's Moby client v0.6.0
(API ≤ 1.56). Each v1 operation was a subtest of `TestEngineOperations`;
Compose and the agent image had their own tests. None of these tests exist
any more, and no Engine version is tested automatically today.

Results were identical on amd64 and arm64 for 25.0.5, 28.5.2 and 29.8.1;
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

### Minimum Engine (recommendation for #25 Q2)

**Docker Engine 25.0 (API 1.44)** was the lowest Engine that passed every
planned v1 operation in the former matrix; 24.0.9 failed BuildKit
base-image pulls from insecure registries (²). The agent enforces it:
Engines below API 1.44 are refused with `unsupported_api_version` and the
agent reports the reason in its capabilities
(`engine.MinSupportedAPIVersion`; the refusal is unit-tested,
`TestConnectRefusesOldEngines`). Docker 24 and 25 are end-of-life upstream;
hosts should run a maintained Engine (28 or 29 today).

## Hosts

| host | v1 | why |
| --- | --- | --- |
| Linux amd64 / arm64, standalone Docker Engine ≥ 25.0, default or custom data root with the identical-path volume mount | supported | the supported configuration (#28 verifies the mount at startup); not verified on real hosts by automated tests |
| Rootless Docker Engine | unsupported | the data root lives in the user's home (`~/.local/share/docker`) and the socket in `$XDG_RUNTIME_DIR`, so the documented identical-path layout does not apply, and the agent's UID 0 is an unprivileged host user (#25 requires root for file access). Detected from the Engine's security options (`Identity.Rootless`); stack operations are refused (#28) |
| Docker Desktop (macOS, Windows, Linux) | unsupported | the Engine runs in a VM: volume paths and bind sources are VM paths, not host paths. Detected (`Identity.DockerDesktop`); stack operations are refused (#28) |
| NAS vendor Engines (Synology Container Manager, QNAP Container Station, Unraid, TrueNAS apps) | unsupported (untested) | vendor-patched Engines, often older than 25.0, custom data roots (e.g. `/volume1/@docker`); not in the matrix. They are refused below API 1.44; above it the #28 identical-path check decides |
| Windows Engines | unsupported | refused at connect (`unsupported`) |
| Docker Swarm | out of the roadmap | |

## Compose features

The agent loads projects with compose-go and runs them with the Compose SDK
v5.5.1 (`internal/agent/compose`). Loading and validation are unit-tested
(`TestLoadProject`, `TestLoadRejectsUnsupportedFeatures`); the runtime
behaviour below was verified against real Engines by the former Compose
fixture and Engine suites, which were removed, and is **not verified by
automated tests any more**.

| feature | status |
| --- | --- |
| services with `image` or `build`, `command`, `environment`, `env_file`, `healthcheck`, `restart`, ports, labels | supported |
| `depends_on` with `service_started`, `service_healthy`, `service_completed_successfully` | supported |
| unhealthy dependency / failed one-shot | the dependent is not started; error `dependency_failed` |
| `required: false` dependencies | supported; a required dependency on a disabled service is `invalid_project` |
| `restart: true` propagation | supported for restart and for recreation of a dependency |
| `.env` / `env_file` interpolation | project files only; the agent's own environment never feeds interpolation |
| profiles | supported (`ProjectSpec.Profiles`) |
| named volumes and networks (local drivers) | supported |
| relative bind mounts (`./data`) | resolved against the project directory; correct Engine paths require the #28 identical-path layout |
| Compose `configs` / `secrets` from files | supported by the SDK (file-based; no Docker Manager secret store, #25) |
| `include` / `extends` with local files | supported |
| remote `include` (Git, OCI) | not loadable (no remote loaders) |
| `post_start` / `pre_stop` hooks | run by the SDK through the Engine's exec API |
| private registry images | per-operation credentials in memory (`TestCredentialsNeverTouchDisk`) |
| top-level `version:` | accepted with an "obsolete" warning |
| `use_api_socket` | **rejected**: would mount the Docker socket and copy registry credentials into the container |
| `provider` services, `models` | **rejected**: they execute external plugins / Docker Model Runner |
| `develop` / watch | ignored by deploy (Docker Manager's own watcher is #23) |

### Stack operations (#7)

| operation | how | evidence |
| --- | --- | --- |
| deploy (`stack.deploy`) | Compose SDK `up` from the on-disk bytes the job reports as the applied revision; missing images pulled/built, `pull: always` / `build: true` on request | unit `internal/agent/stacks`; not verified against a real Engine |
| update (redeploy of a changed definition) | only changed services are recreated | same |
| start / stop / restart (`stack.*`) | the shared lifecycle over the deployed containers: dependencies first with condition waits (2 min per dependency), dependents first on stop, `restart: true` propagation | same; unit `internal/agent/lifecycle` |
| down (`stack.down`, stack deletion) | Compose SDK `down` by project name; volumes and files are kept | same |
| build (`stack.build`, and the deploy's `build_images` step) | the build sections through the Engine's BuildKit (not the Compose SDK's build path): every section on request (`noCache`, `pull`), missing images only in a deploy without `build: true`; streamed, credential-scrubbed progress; cancellation stops BuildKit (the interrupted image keeps its previous version); build timeout (default 1 h, at most 6 h); per-environment build cap | unit `internal/agent/stacks` (`TestStackBuild*`, `TestDeployBuildsMissingImagesThroughTheSamePath`), `internal/manager/stacks` (`TestStackBuild*`); not verified against a real BuildKit |
| unhealthy dependency / failed one-shot | deploy fails with `dependency_failed`; the dependent is not started; the last applied revision is kept | not verified against a real Engine |

### Build keys (#33)

Build sections are built by the agent through the Engine's BuildKit before
the SDK runs. Key validation is unit-tested; builds against a real BuildKit
and registry are not verified by automated tests any more.

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
  shells out to `git` for Git contexts. Docker Manager therefore builds images
  itself with the Moby client's BuildKit API; bake-only features (service
  build contexts, multi-platform, build secrets/SSH, cache export) are not
  available in v1.
- Compose v5.5.1's own go.mod asks for Moby client v0.5.1 / API v1.55.0;
  Docker Manager pins v0.6.0 / v1.56.0 (minimal version selection); the former
  Engine and Compose suites verified the combination (not re-verified since
  their removal). The Moby client is pre-1.0: bump the SDK modules together
  and re-test against real Engines by hand.
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
(`internal/agent/storage`, operator reference in
[deployment.md](deployment.md#host-storage-layout-28)); stack operations are
refused with a diagnostic when it does not hold. The layouts below were
verified on real Engines by the former Compose suite (historical evidence,
tests removed); today only the decision logic is unit-tested
(`internal/agent/storage`), and the layout on real hosts is **not verified
by automated tests**.

| layout | status | evidence |
| --- | --- | --- |
| default data root, `/var/lib/docker/volumes` identical mount, stacks in `docker-manager_stacks` | supported | formerly `TestComposeStacksVolumeDefaultDataRoot` (removed): a stack with `./data`, `env_file` and a local build context deployed from the stacks volume inside the agent image |
| custom data root with the matching identical mount | supported | formerly `TestComposeStacksVolumeCustomDataRoot` (`--data-root /srv/docker-data`) and `TestComposeAgentVerifiesStorageAtStartup` (removed); unit tests (`internal/agent/storage`) |
| default mount on a custom data root / volume directory mounted from another path | refused (`storage_mount_missing` / `storage_path_mismatch`) | formerly `TestComposeMisconfiguredMountRefused` (removed); unit tests (`internal/agent/storage`) |
| extra stack roots (`DOCKER_AGENT_STACK_ROOTS`) at identical paths | supported; a mismatched root is refused on its own (`storage_root_mismatch`) | formerly `TestComposeStackRoots` (removed); unit tests (`internal/agent/storage`) |
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
rules apply) and every volume mounted by Docker Manager's own containers (label
`docker-manager.role`, or its legacy key `dev.neureka.docker-manager.role`), answering `409 volume_files_unsupported`
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
| non-local volume drivers, Docker Manager's own volumes, the stacks volume as a volume | not watched (not served by the file manager, #28) | — |

**Measured latency** (`TestRealFilesystemLatency`, `internal/agent/watch`:
file create, append, rename and delete in a root and a nested directory,
real kernel notifier, 49 changes, time from the change to the
invalidation):

| platform | notifier | p50 | p95 | max |
| --- | --- | --- | --- | --- |
| windows/amd64 (development machine, NTFS) | ReadDirectoryChangesW | 200.6 ms | 200.9 ms | 201.0 ms |
| linux/amd64 (Hyperion CI host, `golang:1.27.1`) | inotify | 200.5 ms | 200.8 ms | 200.9 ms |

The latency is dominated by the 200 ms debounce; the manager's coalescing
window (250 ms, first event immediate) and the live stream add network
time only. The former end-to-end browser check (an external edit reaching
two browser sessions) was removed with the Playwright suite; the path from
the watcher to open views is covered by unit tests only.

**Watch limits.** inotify watches are per user and shared by every root
process on the host (containers included): `fs.inotify.max_user_watches`
defaults to 8 192 on older kernels and scales with memory (up to 1 048 576)
since Linux 5.11. The agent uses at most `DOCKER_AGENT_WATCH_MAX` watches,
default half the kernel limit, clamped to 1 024 – 524 288 (8 192 when the
limit cannot be read). One watch per directory of every watched scope;
nested scopes share watches. A scope that does not fit is polled as a whole
and holds no watches; the kernel's own `ENOSPC` is treated the same. Each
watch costs about 1 KiB of unswappable kernel memory. For large trees raise
the host limit (`sysctl fs.inotify.max_user_watches=524288`) and
`DOCKER_AGENT_WATCH_MAX`.

**Scan budgets.** A reconciliation scan walks at most 200 000 entries per
scope (without following symlinks) and keeps one 64-bit hash per
directory (names, sizes, modification times and modes of its entries), not
per file; the directories past the budget are not compared (the scope
reports `scan_truncated`; inotify still covers them in inotify mode, and
listings are always read live). A manager `rescan` (after a lost
notification sequence) walks at most 200 000 entries. One agent watches at
most 4 096 scopes; one invalidation names at most 256 paths (more become a
whole-scope overflow).

## Disk health (#143)

| Source | Support |
| --- | --- |
| SATA/ATA disks, SAS/SCSI disks, NVMe drives | SMART through smartctl 7.5 (`--scan-open`, then `-a -n standby` per device (ATA also `-l devstat,5 -l devstat,7`), `-n never` once a disk went unread for `DOCKER_AGENT_SMART_WAKE_AFTER`); needs the agent to run privileged ([ADR 0005](adr/0005-disk-health.md)) |
| USB enclosures | only bridges smartctl detects on its own; others report no SMART data (`unsupported`) |
| Virtual disks (virtio, QEMU, VMware, Hyper-V) | no SMART data: reported as such, not as a problem |
| Disks behind hardware RAID controllers | the disks smartctl's scan finds behind the controller (for example MegaRAID, one device per slot: `megaraid,N`) are shown; the controller's own array state is not covered (vendor tools) |
| Linux software RAID (md) | `/proc/mdstat`: every level, members, sync progress |
| ZFS | the pool state from `/proc/spl/kstat/zfs/<pool>/state` only (no vdev errors or scrub progress) |
| btrfs RAID profiles | not covered |

Verified by unit tests against smartctl JSON fixtures and `/proc/mdstat`
samples; not verified against real disks by any automated test. A disk in
standby is not woken (ATA and SCSI; NVMe drives have no such mode for
smartctl) until it went unread for `DOCKER_AGENT_SMART_WAKE_AFTER`
(default 24 h). Disks attached while the agent runs appear after it restarts.
