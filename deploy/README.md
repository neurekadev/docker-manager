# Deploying DockYard

DockYard ships two images, published from `main` as the rolling `edge` tag:

- `ghcr.io/neurekadev/dockyard-manager:edge` — UI, API, agent endpoint, SQLite
- `ghcr.io/neurekadev/dockyard-agent:edge` — one per Docker Engine, outbound only

Both run as root (UID 0); running them as a non-root user is not supported.
There are no semver releases yet.

## Quick start: manager + local agent behind Caddy

Requirements: a Linux host with Docker Engine and the Compose plugin, using
the default data root (`/var/lib/docker`).

The packages are private, so log in to GHCR first with a GitHub personal
access token that has `read:packages`:

```bash
echo "$GITHUB_TOKEN" | docker login ghcr.io -u <github-user> --password-stdin

cd deploy/compose
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

What the example sets up (`compose.yaml`):

- **dockyard-manager** with the named volume `dockyard_data` at
  `/var/lib/dockyard` (database, snapshots, secret key) and
  `DOCKYARD_PUBLIC_URL=https://${DOCKYARD_HOST}`. It publishes no ports; only
  Caddy reaches it.
- **dockyard-agent** on the same network, using the internal URL
  `http://dockyard-manager:8080` with the explicit
  `DOCKYARD_MANAGER_ALLOW_HTTP=true` opt-in, the Docker socket, Docker's volume
  directory at the identical path (`/var/lib/docker/volumes`), the named
  volume `dockyard_agent_state`, and the stacks volume `dockyard_stacks`.
- **caddy** terminating TLS for one origin with one route to the manager and
  `flush_interval -1` for streaming responses.

Enrollment (#3) is not implemented yet: the agent logs that it is not
enrolled and stays healthy. When enrollment lands, create a token in the UI,
put it in `.env` as `DOCKYARD_ENROLLMENT_TOKEN`, run `docker compose up -d`,
and remove it again after the agent has enrolled.

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

## More

Traefik and nginx examples, remote agents on the public origin and a
two-environment setup follow in #27. Configuration reference:
`docs/configuration.md`.
