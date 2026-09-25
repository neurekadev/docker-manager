# Troubleshooting

Reference: [operations/diagnostics.md](../operations/diagnostics.md).

## First checks

```bash
cd deploy/caddy
docker compose ps                         # manager, agent and proxy healthy?
docker compose logs --tail 200 dockyard-manager dockyard-agent
curl -fsS https://docker.example.com/api/v1/health/ready
```

- `health/ready` answers 503 `not_ready` while the database is unreachable
  or migrations are pending; the manager log says which.
- Both logs are JSON lines (set `DOCKYARD_LOG_FORMAT=text` for reading,
  `DOCKYARD_LOG_LEVEL=debug` for detail). Secrets never appear in them.
- Every API response carries `X-Request-ID`; the agent logs the same
  `request_id` for the work it does for that request. Grep both logs for
  it to follow one operation end to end.

## Common problems

| Symptom | Cause and fix |
| --- | --- |
| Setup says the request did not arrive over HTTPS | you used the internal address, or the proxy is not in `DOCKYARD_TRUSTED_PROXIES`, or `DOCKYARD_PUBLIC_URL` is not https ([details](../deployment.md#first-run-setup-over-https)) |
| Rate limits hit everyone at once; audit shows one IP | the manager does not trust the proxy: `DOCKYARD_TRUSTED_PROXIES` must contain the proxy's address (`docker network inspect dockyard`); the default covers Docker's default address pools only |
| Live views stop updating after about a minute | a proxy or load balancer idle timeout below the 15 s heartbeat, or response buffering; see [timeouts](../deployment.md#timeouts-and-heartbeats) |
| Agent stays offline | `docker compose logs dockyard-agent`: wrong `DOCKYARD_MANAGER_URL`, an untrusted certificate (set `DOCKYARD_MANAGER_CA_FILE`), a used or expired enrollment token (create a new one), or `version_unsupported` (upgrade the manager first, then the agent) |
| Enrollment refused with `engine_already_enrolled` | this Engine already has an agent: create the token with the intent to replace it, or remove the old agent |
| Stack operations refused with `storage_mount_missing` / `storage_path_mismatch` | the agent's identical-path mount of Docker's volume directory is missing or points elsewhere (custom data root, rootless Engine, Docker Desktop); the host page shows the diagnostic ([layout](../deployment.md#host-storage-layout-28)) |
| An action on a DockYard container is refused | self-protection: DockYard never stops or removes its own agent, manager data or images, for anyone |
| A job is `interrupted` | the manager or agent restarted during a step whose outcome is unknown; DockYard does not guess: check the target and run it again |
| Update run refused with `update_source_drift` | the Compose files changed on disk and were not deployed: deploy or revert them first |
| Backup import: `backup_import_key_rejected` | wrong Recovery Key, or the key was rotated after that set: enter the previous key too |
| Passkeys fail after moving to another host name | passkeys are bound to the host name: sign in with password (plus TOTP or a recovery code) and register new ones |

## Diagnostics in the UI

**Settings → Diagnostics** (owner) shows versions, each environment's
support-matrix checks (Engine API, architecture, rootless, Docker Desktop,
plain-HTTP transport), job queue state, database and migration state, and
the audit chain verification.

## Support bundle

**Settings → Diagnostics → Download support bundle** (owner, browser
session only; audited) produces a zip with versions, the effective
configuration (secret files by path only), per-environment checks, agent
and job summaries, database and migration state, the audit chain check and
the most recent manager log lines. It is built from allowlisted fields and
never contains passwords, tokens, keys, credentials, the Recovery Key,
Compose or `.env` contents or file contents. It does contain host names,
environment names and IP addresses: review it before sharing.

## Metrics for monitoring

Set `DOCKYARD_METRICS_ENABLED=true` and scrape `/api/v1/system/metrics`
(Prometheus text) with an API token that has only `system.metrics.read`
([example](../operations/diagnostics.md#metrics-of-dockyard-itself)).

## Owner locked out

If the owner lost their password or all factors, run on the manager's host:

```bash
docker compose exec dockyard-manager dockyard-manager owner-recovery
```

It prints a one-time recovery link (valid one hour), signs the owner out
everywhere and records an audit event. Open the link and set a new
password; TOTP, passkeys and recovery codes are removed, so enroll them
again. Anyone who can run this command already controls the manager's data
volume. Other users with lost factors: one of their recovery codes, or a
factor or password reset by the owner (**Access → Users**).

## Rolling back

A failed upgrade and its pre-migration snapshot:
[Upgrades and migrations](upgrades.md#roll-back).
