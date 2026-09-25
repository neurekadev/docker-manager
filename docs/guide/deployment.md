# Deployment

DockYard runs as two containers on your first Docker host, the manager and
a co-located agent, behind a TLS-terminating reverse proxy that serves one
public origin (for example `https://docker.example.com`). Browsers, API
clients and the agents on other hosts all use that origin. The examples in
[`deploy/`](../../deploy/README.md) contain all three services.

## Requirements

- A Linux amd64 host (the images are published for linux/amd64 only) with
  Docker Engine 25.0 or newer and the
  Compose plugin, using a local data root (default `/var/lib/docker`).
  Rootless Engines, Docker Desktop and NAS vendor Engines are not
  supported ([support matrix](../support-matrix.md#hosts)).
- A DNS name for the origin and ports 80/443 on the host (or your own
  proxy in front).
- Access to the images. The registry at `code.neureka.dev` is private
  while DockYard is in development; log in with your Forgejo username and
  a Forgejo access token that has the `read:package` scope:

  ```bash
  echo "$FORGEJO_TOKEN" | docker login code.neureka.dev -u <forgejo-user> --password-stdin
  ```

## Start it

Pick the proxy you prefer: `deploy/caddy` (automatic certificates),
`deploy/traefik` or `deploy/nginx` (your certificate files). All three run
the same manager and agent services.

```bash
cd deploy/caddy
cp .env.example .env        # set DOCKYARD_HOST to your DNS name
docker compose up -d
docker compose ps           # dockyard-manager, dockyard-agent and caddy running, healthy
```

With `DOCKYARD_TLS=internal` Caddy uses its own CA; trust its root on your
clients (`docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt .`)
or set `DOCKYARD_TLS` to an email address for a public ACME certificate.
Open `https://<DOCKYARD_HOST>` and continue with [First run](first-run.md)
right away: until the owner exists, anyone who reaches the origin could
claim the instance.

What the example creates:

| Service | Image | Notes |
| --- | --- | --- |
| `dockyard-manager` | `code.neureka.dev/dockyard/dockyard-manager:edge` | volume `dockyard_data` (database, snapshots, `secret.key`); publishes no port; honors forwarded headers only from `DOCKYARD_TRUSTED_PROXIES` (default: Docker's address pools, where the proxy's network lies) |
| `dockyard-agent` | `code.neureka.dev/dockyard/dockyard-agent:edge` | the Docker socket, Docker's volume directory at the identical path, volumes `dockyard_agent` and `dockyard_stacks`; talks to the manager over the internal network (`DOCKYARD_MANAGER_ALLOW_HTTP=true`) |
| proxy | pinned by digest | the only published ports; on the `dockyard` network |

The Compose project is always `dockyard` (`name: dockyard`), which makes
the volumes (`data`, `agent`, `stacks` in `compose.yaml`) `dockyard_data`,
`dockyard_agent` and `dockyard_stacks` on the host.

## Settings you are likely to change

All in `.env` (every variable: [configuration.md](../configuration.md)):

| Variable | Default | Change when |
| --- | --- | --- |
| `DOCKYARD_HOST` | `localhost` | always: the public DNS name |
| `DOCKYARD_TLS` (Caddy) | `internal` | an email address for ACME, or your certificate files |
| `DOCKYARD_TRUSTED_PROXIES` | `172.16.0.0/12,192.168.0.0/16` | other, untrusted containers share the `dockyard` network (narrow it to that network's subnet), or your Engine uses other `default-address-pools` ([trusted proxies](../deployment.md#trusted-proxies)) |
| `DOCKYARD_MAX_BODY_SIZE` | 1 GB | you upload larger archives or restore larger files |
| `DOCKYARD_LOG_LEVEL` | `info` | you need `debug` logs for a support case |

Keep the identical-path mount `/var/lib/docker/volumes:/var/lib/docker/volumes`
unchanged. With a custom Docker data root, replace `/var/lib/docker` on both
sides; the agent checks the layout at startup and refuses stack operations
with a clear diagnostic when it does not hold
([host storage layout](../deployment.md#host-storage-layout-28)).

## Your own reverse proxy

Any proxy works if it terminates TLS for one origin, forwards everything
to `http://dockyard-manager:8080`, keeps WebSockets and Server-Sent Events
unbuffered with idle timeouts above 15 s, and overwrites
`X-Forwarded-For/Proto/Host`. The full checklist and a comparison of the
three examples: [deployment.md](../deployment.md#reverse-proxy-requirements).

## Back up DockYard's own data

Everything DockYard stores is in named volumes. `dockyard_data` holds the
database **and `secret.key`**; without the key the encrypted settings
cannot be read. DockYard's own backups cover this for you once configured
([Backup and restore](backup-restore.md)); a plain copy of the volume while
the manager is stopped works too.

## Next

- [First run](first-run.md): owner account, first agent, first stack.
- [Multi-host operation](multi-host.md): agents on other Docker hosts.
