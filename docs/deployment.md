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
| Set `X-Forwarded-For/Proto/Host`, overwrite client values | client IP, https detection | default (client values ignored) | default (untrusted client values replaced) | `$proxy_add_x_forwarded_for`, `$scheme`, `$http_host` |
| Body size ≥ the manager's maximum upload/archive size (#15) | uploads, archives, restores | `request_body max_size` (`DOCKYARD_MAX_BODY_SIZE`, 1GB) | no limit by default | `client_max_body_size` (`DOCKYARD_MAX_BODY_SIZE`, 1024m); `proxy_request_buffering off` streams uploads |
| Fixed proxy address trusted by the manager | forwarded headers are honored only from `DOCKYARD_TRUSTED_PROXIES` | `ipv4_address` | `ipv4_address` | `ipv4_address` |

### Trusted proxies

The manager honors `X-Forwarded-For`, `X-Forwarded-Proto`,
`X-Forwarded-Host` and an inbound `X-Request-ID` only when the TCP peer is
listed in `DOCKYARD_TRUSTED_PROXIES`; from anyone else they are ignored,
and they are removed from every request before a handler sees it. The
client IP is the rightmost `X-Forwarded-For` entry that is not itself a
trusted proxy, so addresses a client prepends are never used. The examples
put the proxy on a fixed address (`DOCKYARD_PROXY_IP`, default
`10.227.27.10` in `DOCKYARD_SUBNET=10.227.27.0/24`) and trust exactly that
address. If the subnet overlaps one of your networks, change both values in
`.env`.

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

The Playwright suite verifies this through each example proxy: an SSE
stream and a WebSocket stay open through 70 s of silence (heartbeats and
pings only), and heartbeats arrive as they are sent (no buffering).

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

## Two-environment example

Two Docker hosts, each one Environment:

- **host A** runs the manager, the proxy and a co-located agent;
- **host B** runs only an agent that connects through the public origin.

Everything DockYard stores lives in named volumes: `dockyard_data`
(manager database, snapshots, secret key), `dockyard_agent_state` (agent
credential and job journal) and `dockyard_stacks` (stack project
directories). The only host paths are the Docker socket and Docker's volume
directory, mounted at the **identical path** (`/var/lib/docker/volumes`) so
stack and volume paths mean the same inside the agent and on the Engine
(#28).

**Host A** (public name `docker.example.com`, ports 80/443 reachable):

```bash
cd deploy/caddy                 # or deploy/traefik, deploy/nginx
cp .env.example .env
# .env: DOCKYARD_HOST=docker.example.com
#       DOCKYARD_TLS=admin@example.com       (Caddy ACME; see the example for other modes)
docker compose up -d
docker compose ps               # manager, agent and proxy healthy/running
```

Open `https://docker.example.com` and create the owner account (#16). Then
enroll the co-located agent (#3): create an enrollment token in the UI, put
it into `.env` as `DOCKYARD_ENROLLMENT_TOKEN`, `docker compose up -d`, and
remove it again once the agent is enrolled.

**Host B:**

```bash
cd deploy/remote-agent
cp .env.example .env
# .env: DOCKYARD_MANAGER_URL=https://docker.example.com
#       DOCKYARD_ENROLLMENT_TOKEN=<token from the UI>
#       DOCKYARD_ENVIRONMENT_NAME=host-b
docker compose up -d
```

For a private PKI, copy the CA bundle into the `dockyard_agent_ca` volume
and set `DOCKYARD_MANAGER_CA_FILE=/etc/dockyard/ca/ca.pem` (see the comment
in `deploy/remote-agent/compose.yaml`).

Both environments then appear in the UI; host A's agent is marked as using
the internal plain-HTTP URL. Until enrollment ships (#3) both agents start,
log that they are not enrolled, and stay healthy.

## For contributors

- Client IP, scheme and host of a request: `requestinfo.From(ctx)` /
  `requestinfo.ClientIP(ctx)` (`internal/manager/requestinfo`). Never read
  `X-Forwarded-*` or `RemoteAddr` in features (rate limits #16, audit #30).
- Secure-context check for setup and other credential-creating flows:
  `requestinfo.CheckSecureOrigin(publicURL, localDevelopment, info)`; map
  failures to 403 `insecure_origin` and show the error's `Explanation`.
- `/agent/v1` handlers (#3) are passed as `server.Options.Agent` and run
  behind the agent guard (`server.AgentLimits`: per-IP token bucket, 64 KiB
  bodies, 10 s pre-auth read deadline) and the credential separation
  (`internal/manager/authsep`: mint credentials with
  `authsep.NewAgentCredential`/`NewEnrollmentToken`). Reject with
  `server.AgentFailure`; call `server.EndPreAuth` after authenticating a
  request that keeps streaming a body.
- Streams: `internal/manager/server/sse` (headers, flushing, heartbeats) for
  every SSE endpoint; `internal/manager/server/ws` (`Accept`, bounded read
  limit, `KeepAlive` pings) for every WebSocket. Both use
  `DOCKYARD_STREAM_HEARTBEAT`.
- Agent side: `internal/agent/transport` builds the HTTP/WebSocket client
  (TLS roots, CA bundle, no redirects) and `protocol.TransportInfo`, which
  #3 reports in the capabilities.
