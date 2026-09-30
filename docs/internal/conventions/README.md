# Conventions index

The binding rules for code changes, one file per area. Find the rows that
match the code you are about to touch and read only those files (plus
the guide a file points to, when you need its detail). Every change also
follows [checks-and-ci.md](checks-and-ci.md) and the "Always" rules in
`CLAUDE.md`.

| Area | You touch | Read |
| --- | --- | --- |
| Local checks, CI, generated artifacts, commits, branches | any change | [checks-and-ci.md](checks-and-ci.md) |
| User documentation | any change users see or do (UI labels, flows, defaults, limits, configuration variables, install), `docs/public` | [user-docs.md](user-docs.md) |
| Package layout, DTO/store/domain split, Engine/Compose boundary | new packages, cross-package imports, `internal/agent/engine`, `internal/agent/compose` | [packages.md](packages.md) |
| HTTP API operations, errors, paging, ETags, idempotency, jobs | `internal/manager/api`, `api/route-inventory.yaml` | [api.md](api.md) |
| Database migrations | `internal/db/migrations`, `internal/db/metricsmigrations` | [database-migrations.md](database-migrations.md) |
| Tests | any `*_test.go`, `*.spec.ts`, `*.test.ts` | [tests.md](tests.md) |
| Logging, secrets, security defaults, dependencies | logging, secrets at rest, proxies, SSE/WebSockets, auth, `go.mod`, `web/package.json` | [logging-and-security.md](logging-and-security.md) |
| Audit trail | `internal/manager/audit`, audited operations and events | [audit.md](audit.md) |
| Authorization and permissions | `internal/manager/authz`, `permissions`, capabilities, shaping | [authorization.md](authorization.md) |
| API tokens | `internal/manager/auth` tokens, session-only routes | [api-tokens.md](api-tokens.md) |
| Registry connections and credentials | `internal/manager/registries`, `regclient`, `regauth` | [registries.md](registries.md) |
| Notification channels, alerts, Shoutrrr | `internal/manager/notify`, `store/notification_channels.go`, `api/notifications.go`, `web/src/lib/features/notifications` | [alerts-and-notifications.md](alerts-and-notifications.md) |
| Image builds | `internal/manager/builds`, `gitcreds`, `internal/agent/buildrun` | [builds.md](builds.md) |
| Agent sessions, requests, events | `internal/manager/agents`, `internal/agent/session`, `runtime`, `internal/protocol` | [agent-transport.md](agent-transport.md) |
| Metrics, inventory and disk health | `internal/agent/observe`, `internal/agent/health`, `internal/agent/smartctl`, `internal/manager/observe`, `metrics` | [observation.md](observation.md) |
| Containers, images, volumes, networks | `internal/manager/resources`, `internal/agent/resources`, `internal/agent/volumelabels`, `protocol/docker.go` | [docker-resources.md](docker-resources.md) |
| Byte streams and file manager | `internal/streammux`, `internal/fsroot`, `internal/agent/files`, `internal/manager/files` | [files-and-streams.md](files-and-streams.md) |
| Compose stacks, import by copy | `internal/manager/stacks`, `internal/agent/stacks`, `lifecycle`, `compose` | [stacks.md](stacks.md) |
| Stack templates (template registry) | `internal/manager/templates`, `api/templates.go`, template file scope | [templates.md](templates.md) |
| Docker Manager's own containers | `internal/protection`, `internal/agent/protect`, destructive or bulk features | [self-protection.md](self-protection.md) |
| Container logs and terminals | `containerio` (agent and manager), `api/container_io.go` | [logs-and-terminals.md](logs-and-terminals.md) |
| Schedules | `internal/cron`, `internal/manager/scheduler`, policy owners | [scheduler.md](scheduler.md) |
| Prune | `internal/manager/maintenance`, `internal/agent/prune` | [maintenance.md](maintenance.md) |
| Moving stacks between environments | `internal/manager/migrations`, `internal/agent/migration`, `internal/transfer` | [environment-migration.md](environment-migration.md) |
| Image updates | `internal/manager/updates`, `internal/agent/stacks/update.go` | [updates.md](updates.md) |
| Backups and restores | `internal/restic`, `internal/backup`, `internal/manager/backups`, `internal/agent/backups` | [backups.md](backups.md) |
| Moving the manager to a new server | `internal/manager/managermove`, `internal/manager/movelock`, the move lock in `api`, `jobs`, `scheduler`, `agents`, `web/src/lib/features/managermove` | [manager-move.md](manager-move.md) |
| Version window, host removal, diagnostics | `protocol` versions, `internal/manager/removal`, `diagnostics` | [operability.md](operability.md) |
| Live updates to the browser | `internal/manager/live`, `internal/manager/events`, `web/src/lib/live` | [live-sync.md](live-sync.md) |
| Web UI | `web/` | [web-ui.md](web-ui.md) |

Other internal documentation (open only when needed):

- `docs/internal/architecture/`: the design guide of each subsystem (the
  conventions files link to them).
- `docs/internal/api/`: the HTTP API contract (conventions, errors,
  streams, files, versioning).
- `docs/internal/protocol/agent-v1.md`: the manager-agent protocol.
- `docs/internal/design/README.md` and `docs/internal/web.md`: UI design
  system and web client.
- `docs/internal/development.md`, `deployment.md`, `configuration.md`,
  `support-matrix.md`, `operations/`, `security/`, `adr/`: contributor
  setup, operator reference and decisions. The only user guide is the
  public site (`docs/public`).
