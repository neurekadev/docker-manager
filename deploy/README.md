# Deploying Docker Manager

Docker Manager ships two images, published from `main` as the rolling `edge` tag:

- `code.neureka.dev/docker-manager/docker-manager:edge` — UI, API, agent endpoint, SQLite
- `code.neureka.dev/docker-manager/docker-agent:edge` — one per Docker Engine, outbound only

Both are published for linux/amd64 only (arm64 images are blocked until a
native arm64 build runner exists) and carry BuildKit provenance and SBOM
attestations. Both run as root (UID 0); running them as a non-root user is
not supported. There are no semver releases yet.

Docker Manager runs behind your TLS-terminating reverse proxy on one public origin
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
[`docs/configuration.md`](../docs/configuration.md). `test/deploy` checks
these files statically (topology, pinning, volumes, proxy settings, known
variables); the proxies themselves are not exercised by automated tests.

## Quick start: manager + local agent behind Caddy

Requirements: a Linux amd64 host with Docker Engine 25.0 or
later and the Compose plugin, using the default data root
(`/var/lib/docker`). Rootless Engines, Docker Desktop and NAS vendor Engines
are not supported; see [../docs/support-matrix.md](../docs/support-matrix.md).

The registry is private, so log in to `code.neureka.dev` first with your
Forgejo username and a Forgejo access token that has the `read:package`
scope:

```bash
echo "$FORGEJO_TOKEN" | docker login code.neureka.dev -u <forgejo-user> --password-stdin

cd deploy/caddy
cp .env.example .env        # set DOCKER_MANAGER_HOST to your DNS name (default: localhost)
docker compose pull
docker compose up -d
docker compose ps           # all services should become healthy
```

Open `https://<DOCKER_MANAGER_HOST>`. With the default `DOCKER_MANAGER_TLS=internal`,
Caddy issues a certificate from its local CA; trust it on your clients
(`docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt .`) or
set `DOCKER_MANAGER_TLS` to an email address to use a public ACME certificate for a
publicly reachable host.

What the example sets up (`caddy/compose.yaml`):

- **docker-manager** with the volume `data` (`docker-manager_data` on the
  host) at `/var/lib/docker-manager` (database, snapshots, secret key),
  `DOCKER_MANAGER_PUBLIC_URL=https://${DOCKER_MANAGER_HOST}` and
  `DOCKER_MANAGER_TRUSTED_PROXIES` defaulting to Docker's default address pools
  `172.16.0.0/12,192.168.0.0/16`, which contain the proxy's address on the
  `docker-manager` network whatever Docker assigns. It publishes no ports; only
  the proxy reaches it.
- **docker-agent** on the same network, using the internal URL
  `http://docker-manager:8080` with the explicit
  `DOCKER_AGENT_MANAGER_ALLOW_HTTP=true` opt-in, the Docker socket, Docker's volume
  directory at the identical path (`/var/lib/docker/volumes`), the volume
  `agent` (`docker-manager_agent`) and the stacks volume `stacks`
  (`docker-manager_stacks`).
- **caddy** terminating TLS for one origin with one route to the manager,
  streaming responses unbuffered, on the `docker-manager` network.

Every example is the Compose project `docker-manager` (`name: docker-manager`), so the
volumes are always `docker-manager_data`, `docker-manager_agent` and `docker-manager_stacks`;
`docker-manager_stacks` is the agent's default `DOCKER_AGENT_STACKS_VOLUME`. Keep the
project name. Trusting whole address ranges means any container on the
manager's networks could set `X-Forwarded-*`; if untrusted containers share
one with it, narrow `DOCKER_MANAGER_TRUSTED_PROXIES` in `.env` to the proxy's
network or address (see `.env.example`).

Create the owner account on the setup screen right away, then enroll the
co-located agent (#3): create a one-use token in the UI (**Environments →
Add environment**) and hand it to the running agent on stdin:

```bash
printf '%s\n' "$TOKEN" | docker compose exec -T docker-agent docker-agent enroll
```

Without the UI (automation), the manager creates the token itself:
`docker compose exec -T docker-manager docker-manager enrollment create -name "$(hostname)"`.

(or put the token in `.env` as `DOCKER_AGENT_ENROLLMENT_TOKEN`, run `docker
compose up -d`, and remove it again after the agent has enrolled). Agents on
other hosts use `remote-agent/`. See `docs/deployment.md` and the step-by-step
[guide](../docs/guide/README.md).

## Backups of Docker Manager itself

Back up the `docker-manager_data` volume **including `secret.key`**. Without the key,
encrypted settings cannot be decrypted. To keep the key outside the volume,
mount it as a secret and set `DOCKER_MANAGER_SECRET_KEY_FILE` (see
`docs/configuration.md`). Before each schema upgrade the manager writes a
snapshot to `snapshots/` in the data volume (the newest 3 are kept).

## Updating

```bash
docker compose pull && docker compose up -d
```

Migrations run automatically at manager start; a failing migration stops the
manager with the database unchanged.
