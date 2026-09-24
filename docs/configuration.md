# Configuration

Both executables are configured only through environment variables (and
mounted files for secrets). Configuration is validated at startup; all
problems are reported together and the process exits with status 2.

Secret values accept a `<NAME>_FILE` variant holding a path to a file with
the value (Docker/Compose secrets). Setting both `NAME` and `NAME_FILE` is an
error. Trailing newlines in secret files are ignored.

Sources: `internal/manager/config`, `internal/agent/config`,
`internal/envconfig`. Keep this page in sync with them.

## Manager (`dockyard-manager`)

| Variable | Default | Description |
| --- | --- | --- |
| `DOCKYARD_PUBLIC_URL` | — (required) | The single public origin, e.g. `https://docker.example.com`. Must be `https`; plain `http` is accepted only for `localhost`, `127.0.0.1` or `[::1]` (local development, logged as a warning). No path, query or credentials; sub-path deployments are not supported. Defines the WebAuthn RP ID/origin, cookie scope, CSRF/Origin checks and PWA scope (#27). |
| `DOCKYARD_LISTEN_ADDR` | `:8080` | HTTP listen address (`host:port`). The manager serves plain HTTP; TLS terminates at your reverse proxy (#27). |
| `DOCKYARD_DATA_DIR` | `/var/lib/dockyard` | Persistent data directory (mount a named volume). Holds `dockyard.db` (+ `-wal`/`-shm`), `snapshots/` and, by default, `secret.key`. Created with mode 0700. |
| `DOCKYARD_SECRET_KEY_FILE` | `<data dir>/secret.key` | Application secret-protection key (32 random bytes, base64). See below. |
| `DOCKYARD_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `DOCKYARD_LOG_FORMAT` | `json` | `json` (structured, one object per line) or `text`. |
| `DOCKYARD_TRUSTED_PROXIES` | empty | Comma/space-separated CIDRs or IPs of the reverse proxies in front of the manager. Only from these peers are `X-Forwarded-For` (client IP for rate limits and audit, read right to left), `X-Forwarded-Proto` (https detection, e.g. for first-run setup), `X-Forwarded-Host` and an inbound `X-Request-ID` honored; from anyone else they are ignored, and the server strips them before any handler runs. Set it to your proxy's address (the `deploy/` examples give the proxy a fixed IP). See `docs/deployment.md`. |
| `DOCKYARD_STREAM_HEARTBEAT` | `15s` | Interval of SSE heartbeat comments and WebSocket pings (1s..55s). Must stay below your reverse proxy's idle/read timeout (nginx `proxy_read_timeout` defaults to 60 s). |
| `DOCKYARD_JOB_HISTORY_RETENTION` | `720h` | Finished jobs and their event logs older than this are deleted (Go duration, `1h`..`87600h`). Independent of audit retention (#30). See `docs/architecture/job-engine.md`. |
| `DOCKYARD_JOB_HISTORY_MAX` | `10000` | Keep at most this many finished jobs (100..10000000); the oldest are deleted first. Unfinished jobs are never deleted. |
| `DOCKYARD_JOB_EVENTS_MAX` | `500` | Progress/event log entries kept per job (10..100000); older entries are trimmed. |
| `DOCKYARD_JOB_MAX_CONCURRENT_PULLS` | `2` | Concurrent pull-class jobs (`image.pull`, `stack.update`, `update.run`) per environment (1..64). |
| `DOCKYARD_JOB_MAX_CONCURRENT_BUILDS` | `1` | Concurrent build-class jobs (`image.build`, `stack.build`) per environment (1..64). |

### Secret-protection key

The manager encrypts sensitive settings at rest (registry credentials, S3
keys, TOTP seeds, …) with XChaCha20-Poly1305 under this key
(`internal/manager/secrets`). On the **first start of a fresh installation**
the manager generates it with mode 0600 at `DOCKYARD_SECRET_KEY_FILE`.

- Back it up together with the data volume; without it encrypted settings are
  unrecoverable. Portable recovery is designed in #24.
- Operators may keep it outside the data volume, for example as a Docker
  secret: create it with `openssl rand -base64 32 > secret.key`, mount it and
  set `DOCKYARD_SECRET_KEY_FILE=/run/secrets/dockyard_secret_key`.
- If the database already belongs to an installation but the key file is
  missing, the manager **refuses to start** instead of generating a new key.

### Database

SQLite (pure Go, `modernc.org/sqlite` via Bun's `sqliteshim`) in WAL mode with
`busy_timeout=5000`, `foreign_keys=on`, `synchronous=NORMAL` and
`BEGIN IMMEDIATE` transactions, through a single-connection writer pool (see
`internal/manager/store`). On every start, before any listener or worker:

1. open the database;
2. if migrations are pending and the database already holds data, write a
   consistent `VACUUM INTO` snapshot to `<data dir>/snapshots/`
   (`dockyard-<UTC time>-<seq>-pre-<migration>.db`, the newest 3 are kept);
3. apply the pending migrations, each in its own transaction;
4. on failure, exit non-zero with the database unchanged — restore from the
   snapshot only if you need to roll back an earlier successful migration.

A database migrated by a newer DockYard build (unknown migrations) is refused.
Sampled metrics will live in a separate database file (#5).

## Agent (`dockyard-agent`)

| Variable | Default | Description |
| --- | --- | --- |
| `DOCKYARD_MANAGER_URL` | — (required) | Manager origin the agent dials, e.g. `https://docker.example.com`. Remote agents use the public HTTPS origin; an agent on the manager's Docker network may use `http://dockyard-manager:8080` with the opt-in below. |
| `DOCKYARD_MANAGER_ALLOW_HTTP` | `false` | Must be `true` to accept an `http://` manager URL. Only for an internal network; tokens and credentials otherwise require HTTPS (#27). A plain-HTTP agent is reported as flagged in its capabilities and shown as a warning on its host page. |
| `DOCKYARD_MANAGER_CA_FILE` | empty | Optional PEM bundle of extra CA certificates trusted for the manager's HTTPS origin (private PKI), in addition to the system roots. Validated at startup (certificates only). Certificate verification is never disabled; redirects from the manager are never followed. |
| `DOCKYARD_ENROLLMENT_TOKEN` / `_FILE` | empty | One-use enrollment token created in the UI (#3). Never logged. Remove it after enrollment. |
| `DOCKYARD_AGENT_STATE_DIR` | `/var/lib/dockyard-agent` | Agent state (credentials after #3, health file, job journal `jobs/journal.json` with the fencing high-water mark, #26). Mount a named volume; losing it makes in-flight jobs end as `journal_lost`. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker Engine endpoint (`unix://` or `tcp://`). |
| `DOCKYARD_ENVIRONMENT_NAME` | empty | Optional initial display name of this Environment (≤ 63 characters). |
| `DOCKYARD_LOG_LEVEL` | `info` | As for the manager. |
| `DOCKYARD_LOG_FORMAT` | `json` | As for the manager. |

The agent must run as root (UID 0) and refuses to start otherwise (#28). It
opens no listening socket. Its container health check verifies that
`<state dir>/health.json` was updated within the last 60 seconds.

The agent needs Docker's volume directory mounted at the identical path
(`/var/lib/docker/volumes:/var/lib/docker/volumes`) so stack and volume paths
resolve the same inside the agent and on the Engine (#28).
