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
| `DOCKYARD_SESSION_IDLE_TIMEOUT` | `1h` | A browser session ends after this much inactivity (Go duration, `5m`..`168h`; at most the lifetime). The default follows NIST SP 800-63B AAL2. |
| `DOCKYARD_SESSION_LIFETIME` | `24h` | A browser session ends this long after sign-in, whatever the activity (`15m`..`720h`). |
| `DOCKYARD_JOB_HISTORY_RETENTION` | `720h` | Finished jobs and their event logs older than this are deleted (Go duration, `1h`..`87600h`). Independent of audit retention (#30). See `docs/architecture/job-engine.md`. |
| `DOCKYARD_JOB_HISTORY_MAX` | `10000` | Keep at most this many finished jobs (100..10000000); the oldest are deleted first. Unfinished jobs are never deleted. |
| `DOCKYARD_JOB_EVENTS_MAX` | `500` | Progress/event log entries kept per job (10..100000); older entries are trimmed. |
| `DOCKYARD_JOB_MAX_CONCURRENT_PULLS` | `2` | Concurrent pull-class jobs (`image.pull`, `stack.update`, `update.run`) per environment (1..64). |
| `DOCKYARD_JOB_MAX_CONCURRENT_BUILDS` | `1` | Concurrent build-class jobs (`image.build`, `stack.build`) per environment (1..64). |
| `DOCKYARD_AUDIT_RETENTION_DAYS` | `365` | Audit records older than this many days are deleted (1..36500). The purge is itself audited and keeps the hash chain verifiable. See `docs/architecture/audit.md`. |
| `DOCKYARD_AUDIT_MAX_SIZE_MB` | `1024` | Size cap of the retained audit records in MiB (16..1048576); above it the oldest records are purged down to 90% of the cap. |
| `DOCKYARD_AUDIT_LOG_MIRROR` | `false` | Also write every audit record (redacted, as stored) to the structured log as `msg="audit"` lines for external collection. |
| `DOCKYARD_METRICS_RETENTION_RAW` | `24h` | Keep 10 s metric samples this long (`1h`..`168h`). Metrics live in `<data dir>/metrics.db`, a separate database (#5, `docs/architecture/metrics.md`). |
| `DOCKYARD_METRICS_RETENTION_1M` | `168h` | Keep 1 min rollups this long (`24h`..`2160h`, at least the raw retention). |
| `DOCKYARD_METRICS_RETENTION_15M` | `2160h` | Keep 15 min rollups this long (`168h`..`43800h`, at least the 1 min retention). |
| `DOCKYARD_METRICS_MAX_SIZE_MB` | `2048` | Size cap of the metrics database in MiB (64..1048576). Above it every level's retention is shortened (oldest data first); at 5% of the retention new series are refused until space is free. The full scale budget (25 environments, 1 000 containers) needs about 1.2 GiB. |
| `DOCKYARD_METRICS_MAX_SERIES` | `5000` | Maximum number of metric series (one per environment host, filesystem and container; 100..1000000). Samples of new containers beyond it are dropped (hosts are always kept). |
| `DOCKYARD_FILES_MAX_UPLOAD_MB` | `2048` | Largest file-manager upload in MiB (1 to 2048; agents never accept more than 2 GiB). The reverse proxy's request body limit must allow it (#27); larger data goes in as an archive to extract (#15). |
| `DOCKYARD_MIGRATION_BANDWIDTH_LIMIT` | `0` | Bandwidth cap of environment migrations (#35) through the manager, in bytes per second: `0` (unlimited), a number of bytes or a number with a unit (`KB`, `MB`, `GB`, `KiB`, `MiB`, `GiB`, optionally `/s`), e.g. `50MB`; at least 1 KiB/s. One cap shared by all running migrations. |

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

Sampled metrics live in a separate database file, `<data dir>/metrics.db`
(#5), with its own migrations. It is migrated at startup without a
snapshot (its data is expendable) and refused, like the main database, when
a newer build migrated it; moving the file away starts an empty one.
Manager-state backups (#10) leave it out by default.

## Agent (`dockyard-agent`)

| Variable | Default | Description |
| --- | --- | --- |
| `DOCKYARD_MANAGER_URL` | — (required) | Manager origin the agent dials, e.g. `https://docker.example.com`. Remote agents use the public HTTPS origin; an agent on the manager's Docker network may use `http://dockyard-manager:8080` with the opt-in below. |
| `DOCKYARD_MANAGER_ALLOW_HTTP` | `false` | Must be `true` to accept an `http://` manager URL. Only for an internal network; tokens and credentials otherwise require HTTPS (#27). A plain-HTTP agent is reported as flagged in its capabilities and shown as a warning on its host page. |
| `DOCKYARD_MANAGER_CA_FILE` | empty | Optional PEM bundle of extra CA certificates trusted for the manager's HTTPS origin (private PKI), in addition to the system roots. Validated at startup (certificates only). Certificate verification is never disabled; redirects from the manager are never followed. |
| `DOCKYARD_ENROLLMENT_TOKEN` / `_FILE` | empty | One-use enrollment token (`dye_…`, #3) used while the agent is not enrolled. Never logged; a used or refused token is remembered and never sent again, so leaving it configured is harmless, but remove it after enrollment. Alternatively hand a token to the running agent with `dockyard-agent enroll` (stdin). |
| `DOCKYARD_AGENT_STATE_DIR` | `/var/lib/dockyard-agent` | Agent state: `install-id`, the agent credential `credential.json` (0600), handed-over tokens and enrollment status, the health file and the job journal `jobs/journal.json` with the fencing high-water mark (#26). Mount a named volume; losing it means enrolling again (intent `replace`) and in-flight jobs end as `journal_lost`. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker Engine endpoint (`unix://`, or plain `tcp://`; TLS to a remote Engine is not supported because one agent runs next to each Engine). See "Docker Engine" below. |
| `DOCKYARD_ENVIRONMENT_NAME` | empty | Optional initial display name of this Environment (≤ 63 characters). |
| `DOCKYARD_STACKS_VOLUME` | `dockyard_stacks` | Local named volume holding one directory per stack (#28). Must be mounted into the agent at its identical path (see "Host storage layout" in `docs/deployment.md`). |
| `DOCKYARD_STACK_ROOTS` | empty | Comma-separated extra host directories with stacks (absolute, non-overlapping, at most 16), each bind-mounted into the agent at the identical path. Verified at startup like the stacks volume; a root that fails is refused on its own. |
| `DOCKYARD_HOST_PROC` | `/proc` | procfs the host telemetry is read from (#5). CPU, memory, load and uptime are host-wide in any procfs; network rates come from `<proc>/1/net/dev`, i.e. the host's interfaces only when the agent shares the host's PID namespace (`pid: host`) or network (`network_mode: host`). See "Host telemetry" in `docs/architecture/metrics.md`. |
| `DOCKYARD_LOG_LEVEL` | `info` | As for the manager. |
| `DOCKYARD_LOG_FORMAT` | `json` | As for the manager. |

The agent must run as root (UID 0) and refuses to start otherwise (#28). It
opens no listening socket. Its container health check verifies that
`<state dir>/health.json` was updated within the last 60 seconds; the file's
`status` is the connection state (`not_enrolled`, `enrolling`,
`enrollment_failed`, `connecting`, `connected`, `online`, `disconnected`,
`unauthorized`, `replaced`, `version_unsupported`).

### Commands

| Command | Purpose |
| --- | --- |
| `dockyard-agent enroll [-token-file F] [-wait 90s]` | Hand an enrollment token (stdin) to the running agent through its state directory and wait until it enrolled and its environment is online. Exit 0 online, 1 refused, 2 usage, 3 timeout. |
| `dockyard-manager enrollment create [-name N] [-intent new\|replace:<agentId>\|reattach:<environmentId>] [-ttl 1h] [-allow-duplicate-engine-id] [-json]` | Create a one-use enrollment token in the manager's database and print it with the install commands (run inside the manager container; for installations without the UI/owner account yet, #16). |

### Docker Engine

At startup the agent connects to `DOCKER_HOST` through the official Moby Go
SDK, negotiates the API version and logs the Engine identity (ID, version,
negotiated API version, OS/arch, `DockerRootDir`, rootless / Docker Desktop
detection). Engines older than API 1.44 (Docker Engine 25.0) are refused
with `unsupported_api_version`; see `docs/support-matrix.md` for the tested
and recommended versions. An unreachable or unsupported Engine does not stop
the agent: it records the error code in `health.json` (`engine` field) and in
its capabilities, and retries with backoff (2 s up to 60 s).
`DOCKER_AUTH_CONFIG` and any Docker config directory are ignored: registry
credentials come from the manager for each operation and stay in memory
(#19). Details: `docs/architecture/engine-integration.md`.

The agent needs Docker's volume directory mounted at the identical path
(`/var/lib/docker/volumes:/var/lib/docker/volumes`, or your custom data
root's) so stack and volume paths resolve the same inside the agent and on
the Engine (#28). It verifies the layout at startup and refuses stack
operations with a diagnostic when it does not hold (`health.json` field
`storage`); see "Host storage layout" in `docs/deployment.md`.
