# Deployment topology

How DockYard runs behind an operator-managed reverse proxy on **one public
origin** (#27), how agents connect, and a complete two-environment example.
Configuration reference: [configuration.md](configuration.md). The
ready-to-run examples live in [`deploy/`](../deploy/README.md).

## One origin

```
                          https://docker.example.com
 browsers (PWA) ─┐   ┌──────────────────────────────┐         ┌──────────────────────┐
 API clients ────┼──▶│ reverse proxy (TLS, HTTP/2)  │──http──▶│ dockyard-manager     │
 remote agents ──┘   │ Caddy / Traefik / nginx      │  :8080  │ /        PWA         │
   (dial out)        └──────────────────────────────┘         │ /api/v1  API, SSE    │
                                                     ┌──http─▶│ /agent/v1 agents (WS)│
 co-located agent (same Docker network) ─────────────┘        └──────────────────────┘
```

- The manager serves plain HTTP on one listener (`:8080`). It never
  terminates TLS; your proxy does, with one route that forwards everything.
- `DOCKYARD_PUBLIC_URL` is that origin (`https://…`, no path). It defines
  the passkey RP ID, cookie scope, Origin checks, the PWA scope and the URL in
  agent install commands. Changing the host name later invalidates passkeys
  (#16).
- `/api/v1` and `/agent/v1` are both on this origin. No extra host names or
  ports; agents never listen.

## Reverse proxy requirements

Every example in `deploy/` implements these; if you bring your own proxy,
check each row.

| Requirement | Why | Caddy (`deploy/caddy`) | Traefik (`deploy/traefik`) | nginx (`deploy/nginx`) |
| --- | --- | --- | --- | --- |
| HTTPS for the public origin | passkeys, service worker, `Secure` cookies need a secure context; first-run setup refuses plain HTTP | automatic (`tls internal`, ACME, or your files) | Let's Encrypt resolver or your files | your certificate files |
| HTTP/2 to browsers | several SSE/WebSocket streams across tabs share one connection instead of exhausting the HTTP/1.1 per-origin limit | default | default on TLS entry points | `http2 on;` |
| WebSocket upgrades | agent sessions, container exec | automatic | automatic | `Upgrade`/`Connection` headers |
| No response buffering for streams | SSE must arrive as written | `flush_interval -1` (and automatic for `text/event-stream`) | automatic | the manager sends `X-Accel-Buffering: no` on every stream; keep `proxy_buffering` on for the rest |
| Idle/read timeout above the heartbeat | quiet streams must not be cut | `read_timeout 60s` | entry point `readTimeout=0s` (see below) | `proxy_read_timeout 60s` |
| Pass the `Host` header (with port) | Origin/WebSocket checks against `DOCKYARD_PUBLIC_URL` | default | `passHostHeader: true` | `Host $http_host` |
| Set `X-Forwarded-For/Proto/Host`, overwrite client values | client IP, https detection | default (client values ignored) | default (untrusted client values replaced) | `$remote_addr` (replaced, not appended), `$scheme`, `$http_host` |
| Body size ≥ the manager's maximum upload/archive size (#15) | uploads, archives, restores | `request_body max_size` (`DOCKYARD_MAX_BODY_SIZE`, 1GB) | no limit by default | `client_max_body_size` (`DOCKYARD_MAX_BODY_SIZE`, 1024m); `proxy_request_buffering off` streams uploads |
| Proxy address trusted by the manager | forwarded headers are honored only from `DOCKYARD_TRUSTED_PROXIES` | Docker's default address pools (below) | same | same |

### Trusted proxies

The manager honors `X-Forwarded-For`, `X-Forwarded-Proto`,
`X-Forwarded-Host` and an inbound `X-Request-ID` only when the TCP peer is
listed in `DOCKYARD_TRUSTED_PROXIES`; from anyone else they are ignored,
and they are removed from every request before a handler sees it. The
client IP is the rightmost `X-Forwarded-For` entry that is not itself a
trusted proxy, so addresses a client prepends are never used.

The examples put the proxy and the manager on the `dockyard` network
without a fixed address or subnet and default `DOCKYARD_TRUSTED_PROXIES` to
Docker's default address pools, `172.16.0.0/12,192.168.0.0/16`: whatever
address Docker gives the proxy lies in them. Override it in `.env`:

- **Trade-off:** any container on a network the manager is attached to
  can then set forwarded headers (fake its client IP for rate limits and
  audit, claim https), and so can processes on the host itself (they reach
  the container through the network's gateway address). In the examples
  the manager publishes no port and its only network, `dockyard`, holds
  DockYard and its proxy alone. If other, untrusted containers join it, or
  you attach the manager to a network shared with other applications (an
  existing proxy's network, for example), narrow the value to the proxy's
  network or address, e.g. the `dockyard` subnet (`docker network inspect
  dockyard -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}'`).
- If your Engine allocates networks from other `default-address-pools`
  (`daemon.json`), list those ranges instead.
- Clients whose own address lies in these ranges (a LAN in
  `192.168.0.0/16`) are still resolved correctly because every example
  proxy replaces `X-Forwarded-For` with the client's address instead of
  appending to it (nginx: `$remote_addr`). If you put another proxy (CDN,
  load balancer) in front, forward its chain
  (`$proxy_add_x_forwarded_for`) and trust its addresses as well.

Without the right `DOCKYARD_TRUSTED_PROXIES` the manager sees every request
as plain HTTP from the proxy's address: rate limits then apply to all
clients together, audit entries show the proxy, and first-run setup is
refused (below).

### Timeouts and heartbeats

The manager sends an SSE `: heartbeat` comment and a WebSocket ping every
`DOCKYARD_STREAM_HEARTBEAT` (default 15 s, at most 55 s). Proxy idle/read
timeouts must be comfortably longer; 60 s (nginx's default) is fine.

- **nginx:** `proxy_read_timeout`/`proxy_send_timeout` (`DOCKYARD_PROXY_READ_TIMEOUT`,
  60 s). They also bound idle WebSockets; pings keep them alive.
- **Caddy:** `transport http { read_timeout }` (same variable, 60 s). Caddy
  has no default; the example sets one so a dead manager is noticed.
- **Traefik:** v3's entry point `respondingTimeouts.readTimeout` defaults
  to 60 s and ends long-lived responses (SSE, WebSocket) after a minute,
  heartbeats or not. The example sets it to `0s`. Trade-off: Traefik then
  no longer bounds how slowly a client may send a request; the manager still
  bounds headers (10 s) and unauthenticated `/agent/v1` bodies (10 s), and
  Traefik's `idleTimeout` (180 s) still closes idle keep-alive connections.
- Load balancers in front of the proxy (cloud LBs, CDNs) need an idle
  timeout above the heartbeat as well.

These settings are not verified end to end by automated tests any more:
the former Playwright proxy specs and `TestTLSProxyAgentSessions` (SSE,
WebSocket, terminal and agent sessions idle for 70 s through each example
proxy, reconnects, `Last-Event-ID` resumes) were removed on 2026-09-25.
`test/deploy` still checks the example files statically. After changing a
proxy configuration, check an idle log stream and terminal by hand.

### Optional: restrict `/agent/v1` by IP

`/agent/v1` is publicly reachable on the shared origin. The manager already
rate-limits it per client IP, answers failures generically, bounds request
bodies and frames, and times out unauthenticated requests. If your agents
connect from known networks you can also allow only those at the proxy:

- Caddy: uncomment the `@agent_blocked` matcher in `deploy/caddy/Caddyfile`.
- Traefik: uncomment the `dockyard-agent-endpoint` router and the
  `agent-allowlist` `ipAllowList` middleware in `deploy/traefik/dynamic/dockyard.yml`.
- nginx: add a `location /agent/v1/` block with `allow …; deny all;` and the
  same proxy settings as `location /`.

Co-located agents use the internal URL and are unaffected.

## Upgrades, removal and diagnostics

Upgrade the manager first, then the agents; the manager serves agents of
its own and the previous minor release. The procedure per deploy method,
the pre-migration snapshot and the rollback are in
[`docs/operations/upgrades.md`](operations/upgrades.md). Removing agents
and hosts (preview, archive, re-attach) is in
[`docs/operations/removing-hosts.md`](operations/removing-hosts.md);
diagnostics (logs and request IDs, health, metrics, support bundle) in
[`docs/operations/diagnostics.md`](operations/diagnostics.md).

## Agents

| Agent | `DOCKYARD_MANAGER_URL` | Notes |
| --- | --- | --- |
| co-located (same Docker network as the manager) | `http://dockyard-manager:8080` | requires `DOCKYARD_MANAGER_ALLOW_HTTP=true`; skips the proxy; reported as a plain-HTTP connection and flagged on the host page |
| remote (any other host) | the public origin, `https://docker.example.com` | certificate validated against the system roots plus `DOCKYARD_MANAGER_CA_FILE` (private PKI); never use plain HTTP across networks |

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

- Stacks live in the named volume `dockyard_stacks` (one directory per
  stack; `DOCKYARD_STACKS_VOLUME` selects another local volume).
- The agent mounts Docker's volume directory at its identical path:
  `/var/lib/docker/volumes:/var/lib/docker/volumes`, plus the stacks volume
  at its own mountpoint (every example in `deploy/` does this). No other
  host paths are needed; DockYard's own state lives in named volumes.
- Extra host directories with stacks (e.g. `/opt/stacks`) can be registered
  with `DOCKYARD_STACK_ROOTS=/opt/stacks` and must be bind-mounted at the
  identical path (`/opt/stacks:/opt/stacks`).

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
| `storage_stacks_volume_missing` / `storage_stacks_volume_not_local` | the stacks volume does not exist / is not a local volume | declare it with the local driver (the examples' `stacks` volume in the project `dockyard` is `dockyard_stacks`) |
| `storage_root_mismatch` | a `DOCKYARD_STACK_ROOTS` entry is not mounted at its identical path (only that root is refused) | bind-mount it at the same path |
| `storage_self_unknown` | the agent cannot identify its own container | do not override the agent's `hostname` |
| `storage_rootless_engine` / `storage_docker_desktop` | unsupported Engines ([support matrix](support-matrix.md)) | use a rootful Linux Engine |

**Custom data root.** If `docker info -f '{{.DockerRootDir}}'` is not
`/var/lib/docker`, replace `/var/lib/docker` in the agent's volume lines
(both sides of the directory mount and the stacks volume's mount path) with
your data root.

**SELinux.** The agent needs the Docker socket and every volume's files,
which the default container policy denies. On enforcing hosts run the agent
with `security_opt: ["label=disable"]` instead of relabeling: do **not** add
`:z`/`:Z` to `/var/lib/docker/volumes` (it would relabel every volume on the
host) or to the socket. Stack roots from `DOCKYARD_STACK_ROOTS` that stack
containers also bind-mount can use the shared label (`/opt/stacks:/opt/stacks:z`)
so both the agent and the stack containers may read them; never use the
private `:Z` label there.

**AppArmor.** The agent works with Docker's default `docker-default` profile
and the default capability set; it needs neither `--privileged` nor
`apparmor=unconfined`. A custom host profile must allow the socket and the
mounts above.

**Non-local volumes.** Volumes of other drivers (plugins) and local
volumes backed by NFS/CIFS mount options are not under Docker's volume
directory (or only while mounted); v1 lists them read-only with the reason
and excludes them from file browsing, watching and backup
([support matrix](support-matrix.md)).

## DockYard's own containers (#32)

DockYard protects itself: through its UI, API, API tokens, policies and
jobs it never stops, pauses, updates or removes the connected agent, never
stops or removes the manager (a restart needs an explicit confirmation),
never removes the manager data, agent state or stacks volumes or the images
DockYard runs, and leaves them out of prune, update, backup-shutdown and
bulk selections. The instance owner cannot override this; use Docker on the
host if you really must.

The agent finds its own container by itself. The co-located manager is
found by its container ID, which the manager reports to its agents; keep the
`dev.neureka.dockyard.role: manager` / `agent` labels of the deploy
examples on your containers too, so both are also recognized when that
detection is not possible (custom setups, other installations on the same
host). Every other container of DockYard's own Compose project (for example
the reverse proxy of the examples) is protected with them. Details:
[architecture/self-protection.md](architecture/self-protection.md).

## First-run setup over HTTPS

Creating the owner account (#16) needs a secure context. The manager refuses
to complete setup unless the request reached `DOCKYARD_PUBLIC_URL` over
HTTPS: directly over TLS or, behind a proxy, with `X-Forwarded-Proto: https`
from a trusted proxy, and addressed to the public host. The error explains
what is wrong, for example:

- you opened the manager's internal address (`http://server:8080`) instead
  of the public origin;
- the proxy is not in `DOCKYARD_TRUSTED_PROXIES`, so the manager cannot see
  that the browser used HTTPS;
- `DOCKYARD_PUBLIC_URL` is not https.

The only exception is local development: with
`DOCKYARD_PUBLIC_URL=http://localhost:<port>` (or `127.0.0.1`/`[::1]`) the
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
- **Sign-in policy** (owner, *Settings → Security*): strict passwords
  (default on: at least 15 characters, common and breached passwords
  refused, no composition rules or forced rotation) and the required
  factors: `none`, `totp`, `passkey`, `either` or `both`. Changing the
  required factors signs everyone out; users then get a limited enrollment
  session with a grace period (default 72 h) to add the factors. After the
  grace period only an owner factor or password reset helps. The owner has
  no deadline and is never locked out.
- **Sessions** end after 1 h of inactivity and 24 h at most
  (`DOCKYARD_SESSION_IDLE_TIMEOUT`, `DOCKYARD_SESSION_LIFETIME`); disabling
  a user, a factor or password reset, and "sign out everywhere" end the
  user's sessions and open live streams immediately.
- **Lost factors:** a user completes a password sign-in with one of their
  ten one-time recovery codes, or asks the owner for a factor reset (TOTP,
  passkeys and recovery codes removed; sign in with the password and enroll
  again) or a password reset link.
- **Passkeys** are bound to the host name of `DOCKYARD_PUBLIC_URL`. Moving
  DockYard to another host name makes existing passkeys unusable: users
  sign in with password (+ TOTP or a recovery code) and register new
  passkeys, or the owner resets their factors.

### Owner lockout (break-glass)

If the owner lost their password or factors, run on the manager's host:

```sh
docker exec dockyard-manager dockyard-manager owner-recovery
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

- **host A** runs the manager, the proxy and a co-located agent;
- **host B** runs only an agent that connects through the public origin.

Everything DockYard stores lives in named volumes: `dockyard_data`
(manager database, snapshots, secret key), `dockyard_agent` (agent
credential and job journal) and `dockyard_stacks` (stack project
directories), declared as `data`, `agent` and `stacks` in the Compose
project `dockyard` of every example (remote agents included). The only
host paths are the Docker socket and Docker's volume directory, mounted at
the **identical path** (`/var/lib/docker/volumes`) so stack and volume
paths mean the same inside the agent and on the Engine (#28).

**Host A** (public name `docker.example.com`, ports 80/443 reachable):

```bash
cd deploy/caddy                 # or deploy/traefik, deploy/nginx
cp .env.example .env
# .env: DOCKYARD_HOST=docker.example.com
#       DOCKYARD_TLS=admin@example.com       (Caddy ACME; see the example for other modes)
docker compose up -d
docker compose ps               # manager, agent and proxy healthy/running
```

Enroll the co-located agent (#3). Create a one-use token (in the UI once
#16/#22 ship, or now on the command line inside the manager container) and
hand it to the running agent on stdin — it never appears in a URL, a
process list or the container configuration:

```bash
docker compose exec -T dockyard-manager dockyard-manager enrollment create -name host-a
# prints the token (shown once) and the install commands
printf '%s\n' "$TOKEN" | docker compose exec -T dockyard-agent dockyard-agent enroll
# enrolled: agent …, environment …; the environment is online
```

(Putting the token into `.env` as `DOCKYARD_ENROLLMENT_TOKEN` and running
`docker compose up -d` works as well; remove it again afterwards.)

**Host B:**

```bash
cd deploy/remote-agent
cp .env.example .env
# .env: DOCKYARD_MANAGER_URL=https://docker.example.com
#       DOCKYARD_ENROLLMENT_TOKEN=<token created on host A>
#       DOCKYARD_ENVIRONMENT_NAME=host-b
docker compose up -d
```

One agent per Docker Engine: enrolling a second agent for an enrolled
Engine is refused with `engine_already_enrolled` unless the token's intent
is `replace:<agentId>`, which revokes the old agent. Cloned VMs share the
Engine ID; the manager refuses them with `engine_identity_conflict` (see
`docs/protocol/agent-v1.md`).

**Docker socket access is host-level authority.** The agent needs the
Docker socket (and the volume directory, #28) to manage the host, and
anyone who controls the agent — or its credential and the manager — can
run anything on that host. Protect the manager like root on every enrolled
host: restrict who can reach `/agent/v1` (optionally allowlist agent IPs at
the proxy), keep `DOCKYARD_MANAGER_ALLOW_HTTP` to the internal network,
remove agents you no longer use (their credential stops working at once)
and rotate agent credentials if you suspect exposure. See Docker's
[daemon attack surface](https://docs.docker.com/engine/security/#docker-daemon-attack-surface)
and [protect daemon access](https://docs.docker.com/engine/security/protect-access/).

For a private PKI, copy the CA bundle into the `dockyard_agent_ca` volume
and set `DOCKYARD_MANAGER_CA_FILE=/etc/dockyard/ca/ca.pem` (see the comment
in `deploy/remote-agent/compose.yaml`).

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
  `DOCKYARD_STREAM_HEARTBEAT`.
- Agent side: `internal/agent/transport` builds the HTTP/WebSocket client
  (TLS roots, CA bundle, no redirects) and `protocol.TransportInfo`, which
  the session reports in the capabilities (`internal/agent/session`).
