# Deploying DockYard

DockYard ships two images, published from `main` as the rolling `edge` tag:

- `code.neureka.dev/dockyard/dockyard-manager:edge` — UI, API, agent endpoint, SQLite
- `code.neureka.dev/dockyard/dockyard-agent:edge` — one per Docker Engine, outbound only

Both run as root (UID 0); running them as a non-root user is not supported.
There are no semver releases yet.

DockYard runs behind your TLS-terminating reverse proxy on one public origin
(#27). Pick an example; each runs the manager, a co-located agent on the
internal URL and the proxy. Agents on other hosts use `remote-agent/`.

| Directory | Proxy | TLS |
| --- | --- | --- |
| [`caddy/`](caddy/) | Caddy 2.11 | Caddy's local CA (default), ACME, or your files |
| [`traefik/`](traefik/) | Traefik 3.7 (file provider, no Docker socket) | Let's Encrypt or your files |
| [`nginx/`](nginx/) | nginx 1.30 | your files (e.g. from certbot) |
| [`remote-agent/`](remote-agent/) | — | agent on another host, dials the public HTTPS origin |

Proxy requirements, timeouts, body sizes, trusted proxies, the optional IP
allowlist for `/agent/v1`, first-run HTTPS and a complete two-environment
walkthrough: [`docs/deployment.md`](../docs/deployment.md). Every variable:
[`docs/configuration.md`](../docs/configuration.md). The Playwright suite
runs these proxy configurations unchanged (`e2e/compose.yaml`).

## Quick start: manager + local agent behind Caddy

Requirements: a Linux host (amd64 or arm64) with Docker Engine 25.0 or
later and the Compose plugin, using the default data root
(`/var/lib/docker`). Rootless Engines, Docker Desktop and NAS vendor Engines
are not supported; see [../docs/support-matrix.md](../docs/support-matrix.md).

The packages are private, so log in to the Forgejo registry first with a
Forgejo access token that has the `read:package` scope:

```bash
echo "$FORGEJO_TOKEN" | docker login code.neureka.dev -u <forgejo-user> --password-stdin

cd deploy/caddy
cp .env.example .env        # set DOCKYARD_HOST to your DNS name (default: localhost)
docker compose pull
docker compose up -d
docker compose ps           # all services should become healthy
```

Open `https://<DOCKYARD_HOST>`. With the default `DOCKYARD_TLS=internal`,
Caddy issues a certificate from its local CA; trust it on your clients
(`docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt .`) or
set `DOCKYARD_TLS` to an email address to use a public ACME certificate for a
publicly reachable host.

What the example sets up (`caddy/compose.yaml`):

- **dockyard-manager** with the named volume `dockyard_data` at
  `/var/lib/dockyard` (database, snapshots, secret key),
  `DOCKYARD_PUBLIC_URL=https://${DOCKYARD_HOST}` and
  `DOCKYARD_TRUSTED_PROXIES` set to the proxy's fixed address. It publishes
  no ports; only the proxy reaches it.
- **dockyard-agent** on the same network, using the internal URL
  `http://dockyard-manager:8080` with the explicit
  `DOCKYARD_MANAGER_ALLOW_HTTP=true` opt-in, the Docker socket, Docker's volume
  directory at the identical path (`/var/lib/docker/volumes`), the named
  volume `dockyard_agent_state`, and the stacks volume `dockyard_stacks`.
- **caddy** terminating TLS for one origin with one route to the manager,
  streaming responses unbuffered, on the fixed address `DOCKYARD_PROXY_IP`
  of the `dockyard` network (`DOCKYARD_SUBNET`; change both if the subnet
  overlaps one of your networks).

Create the owner account on the setup screen right away, then enroll the
co-located agent (#3): create a one-use token in the UI (**Environments →
Add environment**) and hand it to the running agent on stdin:

```bash
printf '%s\n' "$TOKEN" | docker compose exec -T dockyard-agent dockyard-agent enroll
```

Without the UI (automation), the manager creates the token itself:
`docker compose exec -T dockyard-manager dockyard-manager enrollment create -name "$(hostname)"`.

(or put the token in `.env` as `DOCKYARD_ENROLLMENT_TOKEN`, run `docker
compose up -d`, and remove it again after the agent has enrolled). Agents on
other hosts use `remote-agent/`. See `docs/deployment.md` and the step-by-step
[guide](../docs/guide/README.md).

## Backups of DockYard itself

Back up the `dockyard_data` volume **including `secret.key`**. Without the key,
encrypted settings cannot be decrypted. To keep the key outside the volume,
mount it as a secret and set `DOCKYARD_SECRET_KEY_FILE` (see
`docs/configuration.md`). Before each schema upgrade the manager writes a
snapshot to `snapshots/` in the data volume (the newest 3 are kept).

## Updating

```bash
docker compose pull && docker compose up -d
```

Migrations run automatically at manager start; a failing migration stops the
manager with the database unchanged.
