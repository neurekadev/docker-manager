# Upgrades and migrations

DockYard has no in-app self-update (#25): you upgrade the images where they
run. There are no versioned releases yet; `main` publishes the rolling
`:edge` tag, so "upgrading" means pulling a newer `:edge` digest. The full
procedure, including the rollback, is
[operations/upgrades.md](../operations/upgrades.md); this page is the
short version.

## Before you upgrade

1. Note the image digests you run, so you can go back:

   ```bash
   docker image inspect --format '{{index .RepoDigests 0}}' \
     ghcr.io/neurekadev/dockyard-manager:edge ghcr.io/neurekadev/dockyard-agent:edge
   ```

2. Recommended: run the **Manager state** backup (Backups → policies →
   *Run now*), or copy the `dockyard_data` volume.

## Upgrade: manager first, then the agents

On host A (manager, co-located agent, proxy):

```bash
cd deploy/caddy
docker compose pull dockyard-manager
docker compose up -d dockyard-manager
docker compose ps                     # dockyard-manager healthy
docker compose pull dockyard-agent && docker compose up -d dockyard-agent
```

On every other host (`deploy/remote-agent`):

```bash
docker compose pull dockyard-agent && docker compose up -d dockyard-agent
```

Rolling `:edge` builds all report version `0.0.0-edge`, so upgrade the
manager and every agent together. For versioned releases the rule is: a
manager serves agents of its own and the previous minor release; an older
or newer agent is refused with "upgrade the agent" / "upgrade the manager
first", and the host page lists each agent's compatibility with upgrade
instructions.

## What happens on the manager's first start

1. If database migrations are pending, the manager writes a consistent
   snapshot of the database to `snapshots/` in the data volume (the newest
   three are kept).
2. It applies the migrations, each in its own transaction.
3. Only then does it listen; `/api/v1/health/ready` turns green.

A failing migration stops the manager (the container keeps restarting and
logs `migration failed (pre-migration snapshot: "…")`); the database is
unchanged by the failed migration. Downgrades are refused: an older
manager does not start on a database a newer one migrated.

Agents reconnect after the manager restarts; every environment is offline
until its agent has reconciled its jobs, then online again. Jobs that were
running continue on the agent and report their result when the session is
back.

## Roll back

Stop the manager, restore the pre-migration snapshot with the manager
image's own command, pin the previous digest and start it:

```bash
docker compose stop dockyard-manager
docker compose run --rm --no-deps dockyard-manager snapshots list
docker compose run --rm --no-deps dockyard-manager snapshots restore <snapshot file>
# compose.yaml: image: ghcr.io/neurekadev/dockyard-manager@sha256:<previous digest>
docker compose up -d dockyard-manager
```

Everything done after the snapshot is lost; the replaced database is kept
in `replaced-<time>/` until you delete it.

## Changing the deployment

- **New host name**: set `DOCKYARD_HOST` (the public URL), update DNS and
  the proxy certificate, restart. Existing passkeys stop working (they are
  bound to the host name); users sign in with password plus TOTP or a
  recovery code and register new passkeys. Update `DOCKYARD_MANAGER_URL` on
  every remote agent.
- **Moving the manager to another machine**: use a manager-state backup
  and the fresh-manager import ([Backup and restore](backup-restore.md#restore-a-lost-manager)),
  or move the `dockyard_data` volume (including `secret.key`) as a whole.
- **Moving stacks between hosts**: environment migration
  ([Multi-host operation](multi-host.md#move-stacks-and-volumes-between-hosts)).
