# Deployment topology

How Docker Manager runs behind an operator-managed reverse proxy on **one public
origin** (#27), how agents connect, and a complete two-environment example.
Configuration reference: [configuration.md](configuration.md).

The Compose setup users run is the one in the user documentation
([Quickstart](../public/content/docs/quickstart.mdx), with its "Add more
servers" section): the manager and a co-located agent in the Compose
project `docker-manager`, with the manager's port 8080 published for the
operator's own HTTPS reverse proxy, and an agent-only project for other
hosts. The repository ships no proxy examples; keep that page and this
guide in sync.

## One origin

```
                          https://docker.example.com
 browsers (PWA) ─┐   ┌──────────────────────────────┐         ┌──────────────────────┐
 API clients ────┼──▶│ reverse proxy (TLS, HTTP/2)  │──http──▶│ docker-manager     │
 remote agents ──┘   │ the operator's own proxy     │  :8080  │ /        PWA         │
   (dial out)        └──────────────────────────────┘         │ /api/v1  API, SSE    │
                                                     ┌──http─▶│ /agent/v1 agents (WS)│
 co-located agent (same Docker network) ─────────────┘        └──────────────────────┘
```

- The manager serves plain HTTP on one listener (`:8080`). It never
  terminates TLS; your proxy does, with one route that forwards everything.
- `DOCKER_MANAGER_PUBLIC_URL` is that origin (`https://…`, no path). It defines
  the passkey RP ID, cookie scope, Origin checks, the PWA scope and the URL in
  agent install commands. Changing the host name later invalidates passkeys
  (#16).
- `/api/v1` and `/agent/v1` are both on this origin. No extra host names or
  ports; agents never listen.

## Reverse proxy requirements

The operator's proxy must meet every row. The user documentation states
them in plain words (Quickstart, "Put HTTPS in front of it").

| Requirement | Why | Notes for common proxies |
| --- | --- | --- |
| HTTPS for the public origin | passkeys, service worker, `Secure` cookies need a secure context; first-run setup refuses plain HTTP | any certificate the browsers trust |
| HTTP/2 to browsers | several SSE/WebSocket streams across tabs share one connection instead of exhausting the HTTP/1.1 per-origin limit | Caddy and Traefik: default; nginx: `http2 on;` |
| WebSocket upgrades | agent sessions, container exec | nginx: `Upgrade`/`Connection` headers |
| No response buffering for streams | SSE must arrive as written | Caddy: `flush_interval -1`; nginx: the manager sends `X-Accel-Buffering: no` on every stream |
| Idle/read timeout above the heartbeat | quiet streams must not be cut | see below |
| Pass the `Host` header (with port) | Origin/WebSocket checks against `DOCKER_MANAGER_PUBLIC_URL` | nginx: `Host $http_host`; Traefik: `passHostHeader: true` |
| Set `X-Forwarded-For/Proto/Host`, overwrite client values | client IP, https detection | nginx: `$remote_addr` (replaced, not appended), `$scheme`, `$http_host` |
| Body size ≥ the manager's maximum upload/archive size (`DOCKER_MANAGER_FILES_MAX_UPLOAD_MB`, #15) | uploads, archives, restores, editor saves | Caddy: `request_body max_size`; nginx: `client_max_body_size`, `proxy_request_buffering off` |
| Proxy address trusted by the manager | forwarded headers are honored only from `DOCKER_MANAGER_TRUSTED_PROXIES` | below |

### Trusted proxies

The manager honors `X-Forwarded-For`, `X-Forwarded-Proto`,
`X-Forwarded-Host` and an inbound `X-Request-ID` only when the TCP peer is
listed in `DOCKER_MANAGER_TRUSTED_PROXIES`; from anyone else they are ignored,
and they are removed from every request before a handler sees it. The
client IP is the rightmost `X-Forwarded-For` entry that is not itself a
trusted proxy, so addresses a client prepends are never used.

The documented `.env` sets `DOCKER_MANAGER_TRUSTED_PROXIES=172.16.0.0/12`,
Docker's first default address pool. It covers a proxy container on the
`docker-manager` network (or another bridge network on the host) and a
proxy installed on the host that connects to the published port through
`localhost` (Docker's port forwarding shows the bridge gateway as the
peer). A proxy on another machine needs its own address instead; the user
documentation says so next to the variable.

- **Trade-off:** any container on the host in that range can then set
  forwarded headers (fake its client IP for rate limits and audit, claim
  https). Narrow the value to the proxy's address or network if untrusted
  containers run on the host. Machines on the LAN are not trusted by
  default, even though the manager's port is published.
- If your Engine allocates networks from other `default-address-pools`
  (`daemon.json`), list those ranges instead.
- The proxy must replace `X-Forwarded-For` with the client's address
  instead of appending to it (nginx: `$remote_addr`). If you put another
  proxy (CDN, load balancer) in front, forward its chain
  (`$proxy_add_x_forwarded_for`) and trust its addresses as well.

Without the right `DOCKER_MANAGER_TRUSTED_PROXIES` the manager sees every request
as plain HTTP from the proxy's address: rate limits then apply to all
clients together, audit entries show the proxy, and first-run setup is
refused (below).

### Timeouts and heartbeats

The manager sends an SSE `: heartbeat` comment and a WebSocket ping every
`DOCKER_MANAGER_STREAM_HEARTBEAT` (default 15 s, at most 55 s). Proxy idle/read
timeouts must be comfortably longer; 60 s (nginx's default) is fine.

- **nginx:** `proxy_read_timeout`/`proxy_send_timeout` (default 60 s).
  They also bound idle WebSockets; pings keep them alive.
- **Caddy:** `transport http { read_timeout }`. Caddy has no default; set
  one (60 s) so a dead manager is noticed.
- **Traefik:** v3's entry point `respondingTimeouts.readTimeout` defaults
  to 60 s and ends long-lived responses (SSE, WebSocket) after a minute,
  heartbeats or not. Set it to `0s`. Trade-off: Traefik then
  no longer bounds how slowly a client may send a request; the manager still
  bounds headers (10 s) and unauthenticated `/agent/v1` bodies (10 s), and
  Traefik's `idleTimeout` (180 s) still closes idle keep-alive connections.
- Load balancers in front of the proxy (cloud LBs, CDNs) need an idle
  timeout above the heartbeat as well.

These settings are not verified end to end by automated tests any more:
the former Playwright proxy specs and `TestTLSProxyAgentSessions` (SSE,
WebSocket, terminal and agent sessions idle for 70 s through each example
proxy, reconnects, `Last-Event-ID` resumes) were removed on 2026-09-25, and
the proxy examples themselves on 2026-09-27. After changing a proxy
configuration, check an idle log stream and terminal by hand.

### Optional: restrict `/agent/v1` by IP

`/agent/v1` is publicly reachable on the shared origin. The manager already
rate-limits it per client IP, answers failures generically, bounds request
bodies and frames, and times out unauthenticated requests. If your agents
connect from known networks you can also allow only those at the proxy:

- Caddy: a `path /agent/v1/*` matcher with `not remote_ip …` that responds
  403.
- Traefik: a router for the `/agent/v1` path prefix with an `ipAllowList`
  middleware.
- nginx: a `location /agent/v1/` block with `allow …; deny all;` and the
  same proxy settings as `location /`.

Co-located agents use the internal URL and are unaffected.

## Upgrades, removal and diagnostics

Upgrade the manager first, then the agents; the manager serves agents of
its own and the previous minor release. The procedure per deploy method,
the pre-migration snapshot and the rollback are in
[`docs/internal/operations/upgrades.md`](operations/upgrades.md). Removing agents
and hosts (preview, archive, re-attach) is in
[`docs/internal/operations/removing-hosts.md`](operations/removing-hosts.md);
diagnostics (logs and request IDs, health, metrics, support bundle) in
[`docs/internal/operations/diagnostics.md`](operations/diagnostics.md).

## Agents

| Agent | `DOCKER_AGENT_MANAGER_URL` | Notes |
| --- | --- | --- |
| co-located (same Docker network as the manager) | `http://docker-manager:8080` | requires `DOCKER_AGENT_MANAGER_ALLOW_HTTP=true`; skips the proxy; reported as a plain-HTTP connection and flagged on the host page |
| remote (any other host) | the public origin, `https://docker.example.com` | certificate validated against the system roots plus `DOCKER_AGENT_MANAGER_CA_FILE` (private PKI); never use plain HTTP across networks |

The agent always dials out; it opens no port. It never follows redirects
from the manager (so tokens cannot be forwarded to another origin or
downgraded to http) and never disables certificate verification. Without
the opt-in an `http://` manager URL is refused at startup.

Credentials are separated by route: the agent's bearer credential (and its
one-use enrollment token) authenticate only `/agent/v1`; the manager refuses
them on `/api/v1`. Browser cookies never authenticate `/agent/v1`: the
manager removes the `Cookie` header from every agent request.

## Host storage layout (#28)

Docker Engine resolves a stack's relative bind mounts (`./data`),
`env_file` entries and build contexts on the **host**. The agent runs in a
container, so it must see those files at the same paths:

- Stacks live in the named volume `docker-manager_stacks` (one directory per
  stack; `DOCKER_AGENT_STACKS_VOLUME` selects another local volume).
- The agent mounts Docker's volume directory at its identical path:
  `/var/lib/docker/volumes:/var/lib/docker/volumes`, plus the stacks volume
  at its own mountpoint (the documented compose files do this). The
  documented compose files also mount their own folder read-only at
  `/import/docker-manager` (`.:/import/docker-manager:ro`,
  `agents.ImportOwnProject`), so **Import project** can copy Docker
  Manager's own project and it is upgraded from the app afterwards. No
  other host paths are needed; Docker Manager's own state lives in named
  volumes.
- Extra host directories with stacks (e.g. `/opt/stacks`) can be registered
  with `DOCKER_AGENT_STACK_ROOTS=/opt/stacks` and must be bind-mounted at the
  identical path (`/opt/stacks:/opt/stacks`).
- Optional, only to import existing Compose projects that live elsewhere
  (e.g. `/opt/stacks` of another tool): mount that directory into the agent
  below `/import`, read-only is enough (`/opt/stacks:/import/stacks:ro`, or
  several such as `/srv/apps:/import/apps:ro`). **Import project** then
  moves a project into the stacks volume: it stops the project, copies its
  whole directory (Compose files and the data folders next to them, with
  owners, permissions, times, links and extended attributes), verifies the
  copy, recreates the containers from it under the same project name and
  starts what ran before. The original directory is left untouched; remove
  it and the mount once your imports are done.

At startup (and whenever the Engine comes back) the agent **verifies**
this: it reads the Engine's `DockerRootDir`, inspects the stacks volume's
`Mountpoint`, finds its own container (from `/proc/self/mountinfo`,
`/proc/self/cgroup` or its default hostname) and checks that each path is
mounted from the same host path, visible and writable. It logs
`storage layout verified` or one error per problem, records the result in
`health.json` (`storage`) and reports it in its capabilities. On a
mismatch, **stack operations are refused** with a diagnostic; everything
else (containers, images, logs, …) keeps working.

| code | cause | fix |
| --- | --- | --- |
| `storage_mount_missing` | the stacks volume's directory is not mounted into the agent (e.g. a custom data root with the default mount) | mount `<DockerRootDir>/volumes:<DockerRootDir>/volumes` |
| `storage_path_mismatch` | mounted, but from a different host path | use the identical path on both sides |
| `storage_read_only` / `storage_not_writable` | read-only mount, or the agent is not root | read-write mount; the agent runs as UID 0 |
| `storage_path_not_visible` | the directory does not exist inside the agent | check the mount |
| `storage_stacks_volume_missing` / `storage_stacks_volume_not_local` | the stacks volume does not exist / is not a local volume | declare it with the local driver (the documented `stacks` volume in the project `docker-manager` is `docker-manager_stacks`) |
| `storage_root_mismatch` | a `DOCKER_AGENT_STACK_ROOTS` entry is not mounted at its identical path (only that root is refused) | bind-mount it at the same path |
| `storage_self_unknown` | the agent cannot identify its own container | do not override the agent's `hostname` |
| `storage_rootless_engine` / `storage_docker_desktop` | unsupported Engines ([support matrix](support-matrix.md)) | use a rootful Linux Engine |

**Custom data root.** If `docker info -f '{{.DockerRootDir}}'` is not
`/var/lib/docker`, replace `/var/lib/docker` in the agent's volume lines
(both sides of the directory mount and the stacks volume's mount path) with
your data root.

**Disk health: the agent runs privileged (#143).** The documented compose
files, the install commands and the move files run the agent with
`privileged: true` (`docker run --privileged`), so the image's smartctl
can read the disks' SMART data: raw ATA/SCSI commands need
`CAP_SYS_RAWIO`, NVMe admin commands `CAP_SYS_ADMIN`, and both need the
host's device nodes, which a privileged container gets without a
host-specific `devices:` list. This adds no authority beyond the Docker
socket the agent already mounts (root-equivalent: whoever controls the
agent can start a privileged container anyway); see
[ADR 0005](adr/0005-disk-health.md). The container's `/dev` is populated
when it starts: disks attached later appear after the agent restarts.
Without `privileged: true` everything else works; the System tab says the
agent can't read the disks (`no_access`). `DOCKER_AGENT_SMART_ENABLED=false`
turns SMART off. RAID state (md, ZFS) comes from procfs and needs no
privilege.

**SELinux.** The agent needs the Docker socket and every volume's files,
which the default container policy denies. On enforcing hosts run the agent
with `security_opt: ["label=disable"]` instead of relabeling: do **not** add
`:z`/`:Z` to `/var/lib/docker/volumes` (it would relabel every volume on the
host) or to the socket. Stack roots from `DOCKER_AGENT_STACK_ROOTS` that stack
containers also bind-mount can use the shared label (`/opt/stacks:/opt/stacks:z`)
so both the agent and the stack containers may read them; never use the
private `:Z` label there. A privileged container already runs without the
SELinux confinement label (`label=disable` is then implied), so the
documented privileged agent needs no `security_opt`; it matters only for an
agent run without `privileged: true`.

**AppArmor.** A privileged container runs unconfined by AppArmor. An agent
run without `privileged: true` works with Docker's default `docker-default`
profile and the default capability set (everything but disk health) and
needs no `apparmor=unconfined`; a custom host profile must allow the socket
and the mounts above.

**Non-local volumes.** Volumes of other drivers (plugins) and local
volumes backed by NFS/CIFS mount options are not under Docker's volume
directory (or only while mounted); v1 lists them read-only with the reason
and excludes them from file browsing, watching and backup
([support matrix](support-matrix.md)).

## Docker Manager's own containers (#32)

Docker Manager protects itself: through its UI, API, API tokens, policies and
jobs it never stops, pauses or removes the connected agent, never stops or
removes the manager (a restart needs an explicit confirmation), never takes
down or deletes its own Compose project, never removes the manager data,
agent state or stacks volumes or the images Docker Manager runs, and leaves
them out of prune, backup-shutdown and bulk selections. The instance owner
cannot override this; use Docker on the host if you really must.

It does manage itself: its own Compose project can be imported as a stack,
redeployed (also with pull or force recreate) and updated by a digest update
policy. The agent converges every other service itself and hands its own
container to a short-lived helper container (`docker-agent self-update`,
started from the agent's image with the agent's mounts) right after the job;
the environment reconnects within a minute. In the UI the stack's Restart,
Stop, Migrate, Rename and Delete stay visible but disabled.

The documented compose files let the agent read their own folder (the
`/import/docker-manager` line), so no mount has to be added first. To import the deployment **in place** (so a redeploy uses the same
`compose.yaml`, `.env` and relative files), keep its directory inside a
registered stack root: for example put it in `/opt/stacks/docker-manager`,
set `DOCKER_AGENT_STACK_ROOTS=/opt/stacks` on the agent and bind-mount
`/opt/stacks:/opt/stacks`. Otherwise **Import project** copies the whole
directory into the stacks volume while it keeps running, and Docker Manager
moves onto the copy at its next deploy.

The agent finds its own container by itself. The co-located manager is
found by its container ID, which the manager reports to its agents; keep the
`docker-manager.role: manager` / `agent` labels of the
documented compose files on your containers too (files written before
2026-09-28 use `dev.neureka.docker-manager.role`, which still works), so
both are also recognized when that detection is not possible (custom setups, other installations on
the same host). Every other container of Docker Manager's own Compose project
(for example a reverse proxy added to it) is protected with them. Details:
[architecture/self-protection.md](architecture/self-protection.md).

## First-run setup over HTTPS

Creating the owner account (#16) needs a secure context. The manager refuses
to complete setup unless the request reached `DOCKER_MANAGER_PUBLIC_URL` over
HTTPS: directly over TLS or, behind a proxy, with `X-Forwarded-Proto: https`
from a trusted proxy, and addressed to the public host. The error explains
what is wrong, for example:

- you opened the manager's internal address (`http://server:8080`) instead
  of the public origin;
- the proxy is not in `DOCKER_MANAGER_TRUSTED_PROXIES`, so the manager cannot see
  that the browser used HTTPS;
- `DOCKER_MANAGER_PUBLIC_URL` is not https.

The only exception is local development: with
`DOCKER_MANAGER_PUBLIC_URL=http://localhost:<port>` (or `127.0.0.1`/`[::1]`) the
manager runs in its explicit localhost development mode and accepts plain
HTTP requests addressed to that host. Never expose such an instance to a
network. (The check is `requestinfo.CheckSecureOrigin`.)

Setup is single use: the first successful request creates the owner and
every later one answers `409 setup_complete`. Complete it right after the
first start; until then anyone who reaches the public origin could claim the
instance (restrict access at the proxy if the host is exposed before you
finish).

## Accounts, sign-in policy and recovery (#16)

- **No self-registration.** The owner invites users (`POST
  /api/v1/invitations`); the one-time link is shown once and expires
  (default 72 h). New users join the default group, initially
  **Restricted** with no access, until the owner grants permissions (#17).
- **Sign-in policy** (owner, *Settings → Sign-in policy*): strict passwords
  (default on: at least 15 characters, common and breached passwords
  refused, no composition rules or forced rotation) and the required
  factors: `none`, `totp`, `passkey`, `either` or `both`. Changing the
  required factors signs everyone out; users then get a limited enrollment
  session with a grace period (default 72 h) to add the factors. After the
  grace period only an owner factor or password reset helps. The owner has
  no deadline and is never locked out.
- **Sessions** end after 8 h of inactivity and 24 h at most
  (`DOCKER_MANAGER_SESSION_IDLE_TIMEOUT`, `DOCKER_MANAGER_SESSION_LIFETIME`), and
  their cookie ends with the browser. **Stay signed in** at sign-in (the
  owner can turn the option off in the sign-in policy) keeps a device
  signed in for 30 days of inactivity and a year at most
  (`DOCKER_MANAGER_SESSION_STAY_IDLE_TIMEOUT`,
  `DOCKER_MANAGER_SESSION_STAY_LIFETIME`) with a persistent cookie. Users
  see their signed-in devices (browser, IP, last activity) under
  **Profile → Sessions** and sign them out one by one; the owner does the
  same on a user's page. Disabling a user, a factor or password reset, and
  "sign out everywhere" end the user's sessions and open live streams
  immediately.
- **Lost factors:** a user completes a password sign-in with one of their
  ten one-time recovery codes, or asks the owner for a factor reset (TOTP,
  passkeys and recovery codes removed; sign in with the password and enroll
  again) or a password reset link.
- **Passkeys** are bound to the host name of `DOCKER_MANAGER_PUBLIC_URL`. Moving
  Docker Manager to another host name makes existing passkeys unusable: users
  sign in with password (+ TOTP or a recovery code) and register new
  passkeys, or the owner resets their factors.

### Owner lockout (break-glass)

If the owner lost their password or factors, run on the manager's host:

```sh
docker exec docker-manager docker-manager owner-recovery
```

It prints a one-time owner-recovery code and link (valid 1 hour), signs the
owner out everywhere and records an audit event. Open the link (or `POST
/api/v1/auth/password-resets/redemptions` with the code and a new password):
the owner gets the new password and their TOTP, passkeys and recovery codes
are removed; sign in and enroll them again. Anyone who can run the command
already controls the data volume. Ownership cannot be transferred in v1; the
owner account is permanent and cannot be disabled or deleted.

## Two-environment example

Two Docker hosts, each one Environment:

- **host A** runs the manager and a co-located agent, behind the
  operator's HTTPS reverse proxy;
- **host B** runs only an agent that connects through the public origin.

Everything Docker Manager stores lives in named volumes: `docker-manager_data`
(manager database, snapshots, secret key), `docker-manager_agent` (agent
credential and job journal) and `docker-manager_stacks` (stack project
directories), declared as `data`, `agent` and `stacks` in the Compose
project `docker-manager` of both documented compose files. The only
host paths are the Docker socket and Docker's volume directory, mounted at
the **identical path** (`/var/lib/docker/volumes`) so stack and volume
paths mean the same inside the agent and on the Engine (#28).

**Host A** (public name `docker.example.com`): the Quickstart's
`compose.yaml` and `.env` (`DOCKER_MANAGER_PUBLIC_URL=https://docker.example.com`),
then `docker compose up -d`. The proxy forwards the origin to port 8080.

Enroll the co-located agent (#3). Create a one-use token (in the UI once
#16/#22 ship, or now on the command line inside the manager container) and
hand it to the running agent on stdin — it never appears in a URL, a
process list or the container configuration:

```bash
docker compose exec -T docker-manager docker-manager enrollment create -name host-a
# prints the token (shown once) and the install commands
printf '%s\n' "$TOKEN" | docker compose exec -T docker-agent docker-agent enroll
# enrolled: agent …, environment …; the environment is online
```

(Putting the token into `.env` as `DOCKER_AGENT_ENROLLMENT_TOKEN` and running
`docker compose up -d` works as well; remove it again afterwards.)

**Host B:** the agent `compose.yaml` of the user documentation's
"Add more servers" page, with a `.env` holding
`DOCKER_AGENT_MANAGER_URL=https://docker.example.com` and
`DOCKER_AGENT_ENROLLMENT_TOKEN=<token created on host A>`, then
`docker compose up -d`.

One agent per Docker Engine: enrolling a second agent for an enrolled
Engine is refused with `engine_already_enrolled` unless the token's intent
is `replace:<agentId>`, which revokes the old agent. Cloned VMs share the
Engine ID; the manager refuses them with `engine_identity_conflict` (see
`docs/internal/protocol/agent-v1.md`).

**Docker socket access is host-level authority.** The agent needs the
Docker socket (and the volume directory, #28) to manage the host, and
anyone who controls the agent — or its credential and the manager — can
run anything on that host (running it privileged for disk health, #143,
adds nothing to that). Protect the manager like root on every enrolled
host: restrict who can reach `/agent/v1` (optionally allowlist agent IPs at
the proxy), keep `DOCKER_AGENT_MANAGER_ALLOW_HTTP` to the internal network,
remove agents you no longer use (their credential stops working at once)
and rotate agent credentials if you suspect exposure. See Docker's
[daemon attack surface](https://docs.docker.com/engine/security/#docker-daemon-attack-surface)
and [protect daemon access](https://docs.docker.com/engine/security/protect-access/).

For a private PKI, mount the CA bundle into the agent read-only (for
example `./ca.pem:/etc/docker-manager/ca.pem:ro`) and set
`DOCKER_AGENT_MANAGER_CA_FILE=/etc/docker-manager/ca.pem`.

Both environments then appear (`GET /api/v1/environments`, and in the UI
with #22); host A's agent is marked as using the internal plain-HTTP URL
(`transport.plainHttp` in `GET /api/v1/environments/{id}/system`).

## For contributors

- Client IP, scheme and host of a request: `requestinfo.From(ctx)` /
  `requestinfo.ClientIP(ctx)` (`internal/manager/requestinfo`). Never read
  `X-Forwarded-*` or `RemoteAddr` in features (rate limits #16, audit #30).
- Secure-context check for setup and other credential-creating flows:
  `requestinfo.CheckSecureOrigin(publicURL, localDevelopment, info)`; map
  failures to 403 `insecure_origin` and show the error's `Explanation`.
- `/agent/v1` is served by `internal/manager/agents` (`Service.Handler()`,
  passed as `server.Options.Agent`) behind the agent guard
  (`server.AgentLimits`: per-IP token bucket, 64 KiB bodies, 10 s pre-auth
  read deadline) and the credential separation (`internal/manager/authsep`:
  `MintAgentCredential`/`MintEnrollmentToken` embed the record ID, only
  `Verifier`s are stored). Reject with `server.AgentFailure`; call
  `server.EndPreAuth` after authenticating a request that keeps streaming a
  body.
- Streams: `internal/manager/server/sse` (headers, flushing, heartbeats) is
  the one SSE implementation; Huma operations use its adapter
  `api.StartSSE`; `internal/manager/server/ws` (`Accept`, bounded read
  limit, `KeepAlive` pings) for every WebSocket. Both use
  `DOCKER_MANAGER_STREAM_HEARTBEAT`.
- Agent side: `internal/agent/transport` builds the HTTP/WebSocket client
  (TLS roots, CA bundle, no redirects) and `protocol.TransportInfo`, which
  the session reports in the capabilities (`internal/agent/session`).
