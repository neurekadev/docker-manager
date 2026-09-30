# Configuration

Both executables are configured only through environment variables (and
mounted files for secrets). Configuration is validated at startup; all
problems are reported together and the process exits with status 2.

Secret values accept a `<NAME>_FILE` variant holding a path to a file with
the value (Docker/Compose secrets). Setting both `NAME` and `NAME_FILE` is an
error. Trailing newlines in secret files are ignored.

Sources: `internal/manager/config`, `internal/agent/config`,
`internal/envconfig`. Keep this page in sync with them.

## Manager (`docker-manager`)

| Variable | Default | Description |
| --- | --- | --- |
| `DOCKER_MANAGER_PUBLIC_URL` | — (required) | The single public origin, e.g. `https://docker.example.com`. Must be `https`; plain `http` is accepted only for `localhost`, `127.0.0.1` or `[::1]` (local development, logged as a warning). No path, query or credentials; sub-path deployments are not supported. Defines the WebAuthn RP ID/origin, cookie scope, CSRF/Origin checks and PWA scope (#27). |
| `DOCKER_MANAGER_LISTEN_ADDR` | `:8080` | HTTP listen address (`host:port`). The manager serves plain HTTP; TLS terminates at your reverse proxy (#27). |
| `DOCKER_MANAGER_DATA_DIR` | `/var/lib/docker-manager` | Persistent data directory (mount a named volume). Holds `docker-manager.db` (+ `-wal`/`-shm`), `snapshots/` and, by default, `secret.key`. Created with mode 0700. |
| `DOCKER_MANAGER_SECRET_KEY_FILE` | `<data dir>/secret.key` | Application secret-protection key (32 random bytes, base64). See below. |
| `DOCKER_MANAGER_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `DOCKER_MANAGER_LOG_FORMAT` | `json` | `json` (structured, one object per line) or `text`. |
| `DOCKER_MANAGER_TRUSTED_PROXIES` | empty | Comma/space-separated CIDRs or IPs of the reverse proxies in front of the manager. Only from these peers are `X-Forwarded-For` (client IP for rate limits and audit, read right to left), `X-Forwarded-Proto` (https detection, e.g. for first-run setup), `X-Forwarded-Host` and an inbound `X-Request-ID` honored; from anyone else they are ignored, and the server strips them before any handler runs. Set it to your proxy's address or network. The user documentation's `.env` sets it to `172.16.0.0/12`, Docker's first default address pool (a proxy container on the host, or a host proxy that connects through `localhost`; every container in that range is then trusted: narrow it if untrusted containers run on the host). See `docs/internal/deployment.md`. |
| `DOCKER_MANAGER_STREAM_HEARTBEAT` | `15s` | Interval of SSE heartbeat comments and WebSocket pings (1s..55s). Must stay below your reverse proxy's idle/read timeout (nginx `proxy_read_timeout` defaults to 60 s). |
| `DOCKER_MANAGER_SESSION_IDLE_TIMEOUT` | `8h` | A browser session ends after this much inactivity (Go duration, `5m`..`168h`; at most the lifetime). |
| `DOCKER_MANAGER_SESSION_LIFETIME` | `24h` | A browser session ends this long after sign-in, whatever the activity (`15m`..`720h`). |
| `DOCKER_MANAGER_SESSION_STAY_IDLE_TIMEOUT` | `720h` | Idle timeout of a session signed in with **Stay signed in** (`1h`..`8760h`; at least the normal idle timeout, at most the stay lifetime). |
| `DOCKER_MANAGER_SESSION_STAY_LIFETIME` | `8760h` | Lifetime of a session signed in with **Stay signed in** (`24h`..`17520h`; at least the normal lifetime). |
| `DOCKER_MANAGER_JOB_HISTORY_RETENTION` | `720h` | Finished jobs and their event logs older than this are deleted (Go duration, `1h`..`87600h`). Independent of audit retention (#30). See `docs/internal/architecture/job-engine.md`. |
| `DOCKER_MANAGER_JOB_HISTORY_MAX` | `10000` | Keep at most this many finished jobs (100..10000000); the oldest are deleted first. Unfinished jobs are never deleted. |
| `DOCKER_MANAGER_JOB_EVENTS_MAX` | `500` | Progress/event log entries kept per job (10..100000); older entries are trimmed. |
| `DOCKER_MANAGER_JOB_MAX_CONCURRENT_PULLS` | `2` | Concurrent pull-class jobs (`image.pull`, `stack.update`, `update.run`) per environment (1..64). |
| `DOCKER_MANAGER_JOB_MAX_CONCURRENT_BUILDS` | `1` | Concurrent build-class jobs (`image.build`, `stack.build`) per environment (1..64). |
| `DOCKER_MANAGER_AUDIT_RETENTION_DAYS` | `365` | Audit records older than this many days are deleted (1..36500). The purge is itself audited and keeps the hash chain verifiable. See `docs/internal/architecture/audit.md`. |
| `DOCKER_MANAGER_AUDIT_MAX_SIZE_MB` | `1024` | Size cap of the retained audit records in MiB (16..1048576); above it the oldest records are purged down to 90% of the cap. |
| `DOCKER_MANAGER_AUDIT_LOG_MIRROR` | `false` | Also write every audit record (redacted, as stored) to the structured log as `msg="audit"` lines for external collection. |
| `DOCKER_MANAGER_METRICS_RETENTION_RAW` | `24h` | Keep 10 s metric samples this long (`1h`..`168h`). Metrics live in `<data dir>/metrics.db`, a separate database (#5, `docs/internal/architecture/metrics.md`). |
| `DOCKER_MANAGER_METRICS_RETENTION_1M` | `168h` | Keep 1 min rollups this long (`24h`..`2160h`, at least the raw retention). |
| `DOCKER_MANAGER_METRICS_RETENTION_15M` | `2160h` | Keep 15 min rollups this long (`168h`..`43800h`, at least the 1 min retention). |
| `DOCKER_MANAGER_METRICS_MAX_SIZE_MB` | `2048` | Size cap of the metrics database in MiB (64..1048576). Above it every level's retention is shortened (oldest data first); at 5% of the retention new series are refused until space is free. The full scale budget (25 environments, 1 000 containers) needs about 1.2 GiB. |
| `DOCKER_MANAGER_METRICS_MAX_SERIES` | `5000` | Maximum number of metric series (one per environment host, filesystem and container; 100..1000000). Samples of new containers beyond it are dropped (hosts are always kept). |
| `DOCKER_MANAGER_FILES_MAX_EDIT_KB` | `512` | Largest file the file manager's editor opens for editing, in KiB (64..16384): the most one content read returns and one save accepts; larger files open read-only (their first bytes). Holds for every agent: above 512 KiB the manager reads and saves through the file streams. Saves send the content as JSON (the manager accepts bodies up to 4x the limit, at least 4 MiB), so the reverse proxy's body limit must allow somewhat more than this (#15). |
| `DOCKER_MANAGER_FILES_MAX_UPLOAD_MB` | `2048` | Largest file-manager upload in MiB (1..1048576). The reverse proxy's request body limit must allow it (#27); larger data goes in as an archive to extract (#15). Agents older than `files.limits` accept at most 2 GiB. |
| `DOCKER_MANAGER_FILES_MAX_DOWNLOAD_MB` | `10240` | Largest file-manager download (one file or an archive of several) and largest archive created with **Create archive**, in MiB (1..1048576). |
| `DOCKER_MANAGER_FILES_MAX_EXTRACT_MB` | `10240` | Most bytes one extraction writes, in MiB (1..1048576). |
| `DOCKER_MANAGER_FILES_MAX_EXTRACT_RATIO` | `100` | An extraction writes at most this many times the archive's size (at least 1 MiB; 10..10000): the decompression-bomb guard. |
| `DOCKER_MANAGER_FILES_MAX_ARCHIVE_ENTRIES` | `100000` | Most entries of an archive extracted, previewed or created (100..1000000). |
| `DOCKER_MANAGER_TEMPLATE_MAX_SIZE_MB` | `32` | Largest stack template in MiB (1 to 1024): the files of a template's draft and of each published version (at most 5000 files and directories). Drafts live in `<data dir>/templates/`. |
| `DOCKER_MANAGER_TEMPLATE_REGISTRY_ENABLED` | `true` | Serve this instance's public template registry (`/api/v1/template-registry` and the `/registry` page) without sign-in: public templates only. `false` answers 404 there. |
| `DOCKER_MANAGER_TEMPLATE_REGISTRY_SYNC_INTERVAL` | `30m` | How often the template registries of other instances added here are read again (at least `5m`; `0` turns periodic syncs off, **Sync now** still works). Failing registries back off up to 6 h. |
| `DOCKER_MANAGER_MIGRATION_BANDWIDTH_LIMIT` | `0` | Bandwidth cap of environment migrations (#35) through the manager, in bytes per second: `0` (unlimited), a number of bytes or a number with a unit (`KB`, `MB`, `GB`, `KiB`, `MiB`, `GiB`, optionally `/s`), e.g. `50MB`; at least 1 KiB/s. One cap shared by all running migrations. |
| `DOCKER_MANAGER_BACKUP_LOCAL_ROOTS` | empty | Comma-separated absolute directories (mounted into the manager) that local backup repositories on the manager may live in (#10). A local manager repository must be below one of them and outside the data directory. Empty: only S3 repositories can hold the manager state. |
| `DOCKER_MANAGER_RESTIC_BINARY` | `/usr/local/bin/restic` | The pinned, checksum-verified restic of the image (#10). Restic's cache and temporary files live in `<data dir>/restic-cache` and `<data dir>/tmp`. |
| `DOCKER_MANAGER_METRICS_ENABLED` | `false` | Serve Docker Manager's own metrics (job queue, agent sessions, event streams, database sizes, audit chain length) in the Prometheus text format at `GET /api/v1/system/metrics` (#34). Off: the route answers 404. Scrape it with an API token holding only `system.metrics.read`. Unrelated to the host and container metrics of #5, which are always collected. See `docs/internal/operations/diagnostics.md`. |
| `DOCKER_MANAGER_MOVE_FROM` | empty | Moving Docker Manager to this server: the old manager's address, `http://<old server>:<port>` (or https; no path). Set together with `DOCKER_MANAGER_MOVE_CODE` (both or neither, else startup fails). On an empty data directory (no owner, no environment) the manager starts in **waiting mode**: it serves only `GET /api/v1/move/status`, health and the web app, refuses agents with 503, asks the old manager for the handoff every 10 s and restarts as the moved instance (`docs/internal/architecture/manager-move.md`). With an owner or environments both are ignored with a warning. The old manager's "Move to a new server" writes both into the generated `.env`; remove them after the move. |
| `DOCKER_MANAGER_MOVE_CODE` | empty | The move code (`dmm_…`), a secret: never logged (the support bundle lists it as `(set)`). It signs the requests to the old manager and decrypts the handoff; it never travels itself. |

### File manager limits

The `DOCKER_MANAGER_FILES_*` limits are set once, on the manager, for every
environment (#15). The manager enforces the edit and upload limits itself
and sends the others with each download, upload, extract preview and
archive or extract job to agents that announce the `files.limits` feature
(`protocol.FeatureFileLimits`); agents cap what they receive at 1 TiB,
1 000 000 entries and a ratio of 10 000. Older agents keep their built-in
limits (the defaults above; uploads at most 2 GiB). Every directory
listing reports the limits in effect for its root (`limits`), and the web
client uses them for its checks and messages; template drafts keep the
template size limits (`DOCKER_MANAGER_TEMPLATE_MAX_SIZE_MB`), with the
edit limit and at most the upload limit. Contract: `docs/internal/api/files.md`.

### Secret-protection key

The manager encrypts sensitive settings at rest (registry credentials, S3
keys, TOTP seeds, …) with XChaCha20-Poly1305 under this key
(`internal/manager/secrets`). On the **first start of a fresh installation**
the manager generates it with mode 0600 at `DOCKER_MANAGER_SECRET_KEY_FILE`.

- Back it up together with the data volume; without it encrypted settings are
  unrecoverable. Docker Manager's manager-state backups (#10, #24) carry it sealed
  under a key derived from the Recovery Key, so the Recovery Key alone
  recovers it on a fresh manager (`docs/internal/architecture/backups.md`).
- Operators may keep it outside the data volume, for example as a Docker
  secret: create it with `openssl rand -base64 32 > secret.key`, mount it and
  set `DOCKER_MANAGER_SECRET_KEY_FILE=/run/secrets/docker_manager_secret_key`.
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
   (`docker-manager-<UTC time>-<seq>-pre-<migration>.db`, the newest 3 are kept);
3. apply the pending migrations, each in its own transaction;
4. on failure, exit non-zero: the failing migration left nothing behind, but
   migrations before it in the same upgrade stay applied, so the previous
   image cannot open the database any more. Roll back by restoring the
   snapshot (`docker-manager snapshots restore <name>`, manager stopped)
   and starting the previous image (`docs/internal/operations/upgrades.md`).

A database migrated by a newer Docker Manager build (unknown migrations) is refused:
downgrades are unsupported.

Sampled metrics live in a separate database file, `<data dir>/metrics.db`
(#5), with its own migrations. It is migrated at startup without a
snapshot (its data is expendable) and refused, like the main database, when
a newer build migrated it; moving the file away starts an empty one.
Manager-state backups (#10) leave it out by default.

## Agent (`docker-agent`)

| Variable | Default | Description |
| --- | --- | --- |
| `DOCKER_AGENT_MANAGER_URL` | — (required) | Manager origin the agent dials, e.g. `https://docker.example.com`. Remote agents use the public HTTPS origin; an agent on the manager's Docker network may use `http://docker-manager:8080` with the opt-in below. After a manager move the address the manager sent replaces it (see "Manager address after a move" below) until this variable is changed. |
| `DOCKER_AGENT_MANAGER_ALLOW_HTTP` | `false` | Must be `true` to accept an `http://` manager URL. Only for an internal network; tokens and credentials otherwise require HTTPS (#27). A plain-HTTP agent is reported as flagged in its capabilities and shown as a warning on its host page. Not needed for a plain-HTTP address a moving manager sent (below). |
| `DOCKER_AGENT_MANAGER_CA_FILE` | empty | Optional PEM bundle of extra CA certificates trusted for the manager's HTTPS origin (private PKI), in addition to the system roots. Validated at startup (certificates only). Certificate verification is never disabled; redirects from the manager are never followed. |
| `DOCKER_AGENT_ENROLLMENT_TOKEN` / `_FILE` | empty | One-use enrollment token (`dye_…`, #3) used while the agent is not enrolled. Never logged; a used or refused token is remembered and never sent again, so leaving it configured is harmless, but remove it after enrollment. Alternatively hand a token to the running agent with `docker-agent enroll` (stdin). |
| `DOCKER_AGENT_STATE_DIR` | `/var/lib/docker-agent` | Agent state: `install-id`, the agent credential `credential.json` (0600), `manager.json` (0600: the highest manager generation seen, so an older manager is refused after a move, and the manager address a move gave), handed-over tokens and enrollment status, the health file and the job journal `jobs/journal.json` with the fencing high-water mark (#26). Mount a named volume; losing it means enrolling again (intent `replace`) and in-flight jobs end as `journal_lost`. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker Engine endpoint (`unix://`, or plain `tcp://`; TLS to a remote Engine is not supported because one agent runs next to each Engine). See "Docker Engine" below. |
| `DOCKER_AGENT_ENVIRONMENT_NAME` | empty | Optional initial display name of this Environment (≤ 63 characters). |
| `DOCKER_AGENT_STACKS_VOLUME` | `docker-manager_stacks` | Local named volume holding one directory per stack (#28). Must be mounted into the agent at its identical path (see "Host storage layout" in `docs/internal/deployment.md`). |
| `DOCKER_AGENT_STACK_ROOTS` | empty | Comma-separated extra host directories with stacks (absolute, non-overlapping, at most 16), each bind-mounted into the agent at the identical path. Verified at startup like the stacks volume; a root that fails is refused on its own. |
| `DOCKER_AGENT_HOST_PROC` | `/proc` | procfs the host telemetry is read from (#5). CPU, memory, load and uptime are host-wide in any procfs; network rates come from `<proc>/1/net/dev`, i.e. the host's interfaces only when the agent shares the host's PID namespace (`pid: host`) or network (`network_mode: host`). See "Host telemetry" in `docs/internal/architecture/metrics.md`. Disk health reads the RAID state (`mdstat`, `spl/kstat/zfs`) and the block devices (`partitions`) from it too (#143). |
| `DOCKER_AGENT_BACKUP_LOCAL_ROOTS` | empty | Comma-separated absolute directories (mounted into the agent) that local backup repositories of this environment may live in (#10). A location outside them, or overlapping a stack root or Docker's data root (backup sources), is refused (`path_not_allowed`, `repository_inside_source`). |
| `DOCKER_AGENT_BACKUP_EXTERNAL_ALLOWLIST` | empty | Comma-separated host paths outside stack project directories that backup policies may opt into (for example bind sources like `../data` or `/srv/shared`, #10). A path is backed up only when the policy opts in (its **Back up allowed folders outside stacks** switch, `externalBinds`) **and** it lies below an entry here; it must be mounted into the agent at the same path. System paths (`/`, `/proc`, `/sys`, `/dev`, `/run`, `/boot`) and Docker's data root are never allowed. |
| `DOCKER_AGENT_RESTIC_BINARY` | `/usr/local/bin/restic` | As for the manager; restic's cache and temporary files live in `<state dir>/restic-cache` and `<state dir>/tmp`. |
| `DOCKER_AGENT_SMART_ENABLED` | `true` | Reads every disk's SMART data for disk health (#143) with the image's smartctl; needs the agent to run privileged (see "Disk health" in `docs/internal/deployment.md`). `false` turns it off: the System tab says so and RAID state is still read. |
| `DOCKER_AGENT_SMART_INTERVAL` | `30m` (5m – 24h) | How often the agent reads every disk's SMART data (a Go duration). A disk in standby is not woken; the device list is scanned again every 6 h and on "Check disks now". |
| `DOCKER_AGENT_SMARTCTL_BINARY` | `/usr/local/bin/smartctl` | Advanced: the smartctl executable (the image ships a pinned one, ADR 0005). Must be an absolute path. |
| `DOCKER_AGENT_WATCH_MAX` | half of `fs.inotify.max_user_watches` (1 024 – 524 288) | Kernel watch budget of the file watcher (#23): one watch per directory of every watched stack project and open volume view. Scopes beyond it are polled every 30 s instead. The kernel limit is per user and shared with every root process on the host; raise `fs.inotify.max_user_watches` on the host for large trees. See "File watching" in `docs/internal/support-matrix.md`. |
| `DOCKER_AGENT_LOG_LEVEL` | `info` | As for the manager. |
| `DOCKER_AGENT_LOG_FORMAT` | `json` | As for the manager. |

The agent must run as root (UID 0) and refuses to start otherwise (#28). It
opens no listening socket. Its container health check verifies that
`<state dir>/health.json` was updated within the last 60 seconds; the file's
`status` is the connection state (`not_enrolled`, `enrolling`,
`enrollment_failed`, `connecting`, `connected`, `online`, `disconnected`,
`unauthorized`, `replaced`, `version_unsupported`); `managerUrl` and
`managerUrlSource` (`config` or `move`) show the manager address it dials.

### Manager address after a move

When Docker Manager moves to a new server, the old manager tells each
agent it can place the new address (`manager.redirect`,
`docs/internal/architecture/manager-move.md`, "Agents follow"). The agent
keeps it in `<state dir>/manager.json` together with the
`DOCKER_AGENT_MANAGER_URL` value it replaces, and from then on dials it
instead of `DOCKER_AGENT_MANAGER_URL`, also after restarts and for
enrollment (the new credential records it). A plain-HTTP address is
accepted without `DOCKER_AGENT_MANAGER_ALLOW_HTTP` (the authenticated
manager sent it) and flagged like any plain-HTTP agent; an HTTPS address
keeps `DOCKER_AGENT_MANAGER_CA_FILE`. The startup log names the address
and `manager_url_source: move`.

To stop using it, set `DOCKER_AGENT_MANAGER_URL` to the address you want
(for example the public HTTPS origin once DNS points at the new server)
and recreate the agent container: a value other than the one the redirect
replaced wins, and the agent forgets the redirect (it keeps the manager
generation, so the old manager stays refused).

### Commands

| Command | Purpose |
| --- | --- |
| `docker-agent enroll [-token-file F] [-wait 90s]` | Hand an enrollment token (stdin) to the running agent through its state directory and wait until it enrolled and its environment is online. Exit 0 online, 1 refused, 2 usage, 3 timeout. |
| `docker-manager enrollment create [-name N] [-intent new\|replace:<agentId>\|reattach:<environmentId>] [-ttl 1h] [-allow-duplicate-engine-id] [-json]` | Create a one-use enrollment token in the manager's database and print it with the install commands (run inside the manager container; for installations without the UI/owner account yet, #16). |

### Docker Engine

At startup the agent connects to `DOCKER_HOST` through the official Moby Go
SDK, negotiates the API version and logs the Engine identity (ID, version,
negotiated API version, OS/arch, `DockerRootDir`, rootless / Docker Desktop
detection). Engines older than API 1.44 (Docker Engine 25.0) are refused
with `unsupported_api_version`; see `docs/internal/support-matrix.md` for the tested
and recommended versions. An unreachable or unsupported Engine does not stop
the agent: it records the error code in `health.json` (`engine` field) and in
its capabilities, and retries with backoff (2 s up to 60 s).
`DOCKER_AUTH_CONFIG` and any Docker config directory are ignored: registry
credentials come from the manager for each operation and stay in memory
(#19). Details: `docs/internal/architecture/engine-integration.md`.

The agent needs Docker's volume directory mounted at the identical path
(`/var/lib/docker/volumes:/var/lib/docker/volumes`, or your custom data
root's) so stack and volume paths resolve the same inside the agent and on
the Engine (#28). It verifies the layout at startup and refuses stack
operations with a diagnostic when it does not hold (`health.json` field
`storage`); see "Host storage layout" in `docs/internal/deployment.md`.
