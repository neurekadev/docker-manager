# Diagnostics (#34)

## Logs

Both executables log structured JSON lines to stderr (`log/slog`;
`DOCKER_MANAGER_LOG_FORMAT=text` / `DOCKER_AGENT_LOG_FORMAT=text` for humans) at `DOCKER_MANAGER_LOG_LEVEL` / `DOCKER_AGENT_LOG_LEVEL` (`debug`,
`info`, `warn`, `error`). Collect them with `docker compose logs` or any
Docker log driver. Secrets never reach the log: values that might be
sensitive are wrapped as `[REDACTED]`, and the secret-canary tests (#29)
fail when a password, token, key or credential shows up in a log line.

**Request IDs.** Every `/api/v1` request gets an ID (`X-Request-ID` on the
response; kept from a trusted proxy's `X-Request-ID`, generated
otherwise). The manager logs it as `request_id` and passes it to the agent:
request, `stream_open` and job `command` frames carry `requestId` (jobs keep
the ID of the request that created them), and the agent logs everything it
does for that frame with the same `request_id` (and `job_id`). To follow
one operation, grep both logs for the ID:

```bash
docker compose logs docker-manager docker-agent | grep '"request_id":"4f0c…"'
```

Scheduled work carries no request ID; follow it by `job_id`. Agents older
than the field (they do not announce `frame.request_id`) get no request
IDs.

## Health

| image | `HEALTHCHECK` | what it checks |
| --- | --- | --- |
| manager | `docker-manager healthcheck` → `GET /api/v1/health` | the process serves HTTP |
| agent | `docker-agent healthcheck` | `<state dir>/health.json` updated within 60 s (its `status` is the connection state) |

`GET /api/v1/health/ready` answers 503 `not_ready` while the database is
unreachable or migrations are pending (use it for load balancers).

## Metrics of Docker Manager itself

Off by default. With `DOCKER_MANAGER_METRICS_ENABLED=true` the manager serves
`GET /api/v1/system/metrics` in the Prometheus text format 0.0.4: job queue
depth and unfinished jobs by state and kind, connected agent sessions,
environments by status and connection, agents by version compatibility,
open event streams, event bus subscribers, database sizes, the audit chain
length and Go runtime basics (`docs/internal/api/streams.md` lists every family).
Labels carry only enumerations.

It needs `system.metrics.read` (instance scope, delegable). Create a
dedicated API token with only that grant (the owner, or a user whose group
grants it) and scrape with it:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: docker-manager
    scheme: https
    metrics_path: /api/v1/system/metrics
    authorization:
      credentials_file: /etc/prometheus/docker-manager-token   # dy_… token with system.metrics.read
    static_configs:
      - targets: [docker.example.com]
```

Host and container metrics are the JSON routes of #5, not this endpoint.

## Support bundle

`GET /api/v1/support-bundle` (owner only, a signed-in browser session; API
tokens are refused; audited as `system.support_bundle`) downloads a zip:

| file | contents |
| --- | --- |
| `versions.json` | manager version/commit/Go, API and agent protocol versions, every agent's version and compatibility |
| `configuration.json` | the effective `DOCKER_MANAGER_*` settings (secret files by path only) |
| `support-matrix.json` | per environment: agent version window, Engine API ≥ 1.44, linux/amd64 or arm64, rootless, Docker Desktop, plain-HTTP transport, agent diagnostics |
| `agents.json` | environments (status, online, archived) and agents (connection, last seen, rotation pending) |
| `audit-chain.json` | the audit hash chain verification (`ok`, records checked, problems) |
| `jobs.json` | job counts per kind and state, unfinished jobs (no inputs) |
| `database.json` | migrations applied/pending, pre-migration snapshots, database sizes |
| `logs.ndjson` | the manager's most recent log lines (in memory, up to 2 000), redacted again |

The bundle is built from allowlisted fields and never contains passwords,
tokens, keys, registry/Git/S3 credentials, the Recovery Key, TOTP seeds,
Compose or `.env` contents, file contents or job inputs
(`internal/manager/app` `TestSupportBundleHasNoSecrets` seeds a canary of
every secret kind and scans the unpacked bundle). Review it before sharing
anyway: it contains host names, environment names and IP addresses from the
logs.
