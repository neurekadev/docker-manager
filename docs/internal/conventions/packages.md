# Package boundaries

Binding conventions (split out of AGENTS.md). Read this file when your change touches this area.

- `cmd/*` only parse args/env and call into `internal/...`.
- Manager code lives under `internal/manager/...`; agent code under
  `internal/agent/...`. The agent never imports `internal/manager/...` and
  never listens on a socket (enforced by `internal/agent/nolisten_test.go`).
  Code both sides run lives in neutral packages (`internal/protocol`,
  `internal/streammux`, `internal/transfer`, `internal/fsroot`, ...) that
  import neither `internal/agent/...` nor `internal/manager/...`; manager
  production code never imports `internal/agent/...` (it would pull in the Docker
  SDK adapters).
- Keep three kinds of types separate and convert explicitly:
  - transport DTOs in `internal/manager/api` (JSON/Huma tags),
  - database models in `internal/manager/store` (Bun tags, unexported rows),
  - domain types in `internal/domain` (no tags, no HTTP/DB/Docker imports).
- Docker Engine access only through the agent's Moby adapter
  (`internal/agent/engine`, interface `engine.Engine`) and Compose through
  `internal/agent/compose` (#21); SDK types never leave those packages and
  errors carry stable `engine.Code`s. Never import `github.com/docker/docker`,
  never exec the docker/compose/buildx CLI, never dial the Docker socket by
  hand (`scripts/policy-check.sh`, depguard `sdk-boundary`, forbidigo).
  Registry credentials are per operation and in memory only (#19).
  Guide: `docs/internal/architecture/engine-integration.md`.
- Prefer small focused packages over a shared `util` package.
