# Development

## Prerequisites

| Tool | Version | Notes |
| --- | --- | --- |
| Go | 1.27.1 (`go.mod` toolchain) | `CGO_ENABLED=0` works everywhere; no C compiler needed. |
| Node.js / npm | 26 (`.nvmrc`) / 11 | Only for the web UI and client generation. |
| golangci-lint | v2.13.2 | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2` |
| bash | Git Bash on Windows, any bash on Linux/macOS | All gates are bash scripts; there is no Makefile. |

Docker is **not** required locally. Everything that needs a Docker Engine
(image builds, Engine/Compose integration tests, E2E) runs in GitHub Actions.
Keep such tests behind the `integration` or `e2e` build tags (or under
`test/`) so `go test ./...` stays Docker-free.

## Everyday commands

```bash
bash scripts/check.sh          # the full local gate (run before every push)
bash scripts/go-check.sh       # gofmt, go vet, golangci-lint, go test
bash scripts/web-check.sh      # npm ci (if needed), lint, svelte-check, vitest, build
bash scripts/policy-check.sh   # repository policy (legacy Docker imports, CRLF, LICENSE, mockup)
bash scripts/generate.sh       # regenerate api/openapi.json + web/src/lib/api/schema.d.ts
bash scripts/generate.sh --check
bash scripts/build-static.sh   # linux amd64/arm64 static binaries into dist/
bash scripts/license-check.sh  # dependency license allowlist (Go + npm production)
```

## Running locally

```bash
# terminal 1: manager on http://localhost:8080 (plain http allowed for localhost)
DOCKYARD_PUBLIC_URL=http://localhost:8080 DOCKYARD_DATA_DIR=./data \
  DOCKYARD_LOG_FORMAT=text go run ./cmd/dockyard-manager

# terminal 2 (optional): Vite dev server with HMR, proxies /api to :8080
npm --prefix web run dev
```

`go run` serves whatever UI is embedded: the committed placeholder page on a
clean checkout, or the real app after `npm --prefix web run build` (the build
writes `web/build/app`, which `web/embed.go` picks up automatically).

The agent refuses to run as non-root and targets Linux; run it in its
container (see `deploy/`) rather than on a Windows host.

## UI devstack (no Docker)

`test/devstack` runs a real manager on localhost plus in-process agents over
in-memory fake Docker Engines (`internal/agent/engine/enginefake`), seeded
with a small homelab, so UI work and Playwright need no Docker Engine:

```bash
npm --prefix web run build            # the devstack serves web/build/app from disk
go run ./test/devstack                # http://localhost:8080, seeded, owner signed up
go run ./test/devstack -setup         # first-run setup still open (no accounts, no jobs)
go run ./test/devstack -addr 127.0.0.1:8090 -keep -log-level info
```

It prints the environments and credentials:

| account | password | access |
| --- | --- | --- |
| `admin` (owner) | `dockyard-devstack-owner` | everything |
| `guest` | `dockyard-devstack-guest` | Restricted (the denied state) |

Seeded data:

- **homelab** (online, service address `192.168.1.10`): the stack **Silo**
  (services `silo-web`, `silo-api`, `silo-db`, `silo-redis`, `silo-worker`
  with the #22 display metadata; descriptions and icons are DockYard
  metadata, not images), the stack **Media** (`jellyfin`), standalone
  `homeassistant`, `pihole` and an exited `backup-runner`, volumes,
  networks and images.
- **nas** (online): `syncthing`, `samba` and stopped leftovers for prune.
- **edge** (arm64): enrolled, then disconnected, so it is **offline**.
- Metrics: a 30-minute history and a live 10 s sampler per host and
  container (smooth, deterministic curves); `engine.info` inventories.
  Each agent's history is collected (through the production collector)
  before the devstack reports ready, so the dashboard has usage from the
  first load and **edge** keeps its last known values; its offline time
  shows as a growing gap in its charts.
- Jobs: a container restart (succeeded), a container start that fails, and
  a prune run with one failed removal (**partial**).
- Schedules: the prune policy (disabled) and an update policy for Silo whose
  check runs Sundays at 02:30 Europe/Berlin (enabled; its fifth next run
  falls on the repeated hour of the October DST change).
- Container logs: a few lines per service, followed every 4 s.
- Files (#15, #23): each host has a real "Docker volume directory" at
  `<data>/hosts/<host>/volumes` (default data: `<tmp>/dockyard-devstack`)
  with the volumes' `_data` folders and the stacks volume
  (`dockyard_stacks/_data/<stack>`: Silo's project with `config/`,
  `data/thumbnails/` of 1 200 files, `README.md`, …). The agents serve them
  with the production file service and watcher: the file manager, uploads,
  archives and jobs work, and editing a file there with any editor shows up
  in an open listing (and as an editor conflict) within seconds.
- Automation (#20, #14): the update policy **Silo images** with the result
  of an earlier digest check (an update on a `latest` tag, a quarantined
  digest with history, an up-to-date, an excluded and a failed service; the
  devstack has no registry, so a new check reports its errors) and the
  maintenance policy **Weekly cleanup** on homelab.
- Backups (#10): an in-memory restic (`restictest`, persisted to
  `<-backups>/restic-state.json`) behind local repositories below
  `-backups` (default `<tmp>/dockyard-devstack-backups`): **Manager disk**
  (Recovery Key generated, confirmed and printed), **Homelab disk** and
  **NAS disk** (awaiting its key confirmation); the policies **Manager
  state** (a complete set) and **Nightly** (partial: the devstack agents run
  no restic, so the Silo member fails).
- Import (#24): a seeded run starts `-backups` fresh; a later `-setup` run
  keeps it, so setup's **Import from backup** can restore it (directory
  `<-backups>/manager`, the printed Recovery Key).

What is simulated: the Docker Engines, host metrics, Compose reads
(`compose.read` from the project directories on disk, `compose.services`
from the fake Engine) and container logs. Everything between the public
API and the Engine adapter is production code. Not available: exec
terminals, Compose deploys and builds (they need a real Engine; use the CI
suites). The
devstack is a test tool under `test/`: it is never part of the images or
`scripts/build-static.sh`, refuses non-loopback addresses and prints
credentials, so never expose it.

Rebuild the UI and restart the devstack to see UI changes (the manager
reads the UI files at start). For hot reload, run `npm --prefix web run dev`
and point its proxy at the devstack (`vite.config.ts` proxies `/api` to
`127.0.0.1:8080`).

### Playwright against the devstack

```bash
cd e2e && npm ci && npx playwright install chromium
go run ./test/devstack -setup               # fresh manager for the setup flow (other terminal)
E2E_BASE_URL=http://localhost:8080 npx playwright test tests/ui.spec.ts
# a seeded devstack (setup done): tell the spec who the owner is
E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin E2E_UI_PASSWORD=dockyard-devstack-owner \
  npx playwright test tests/ui.spec.ts
# review screenshots at 1440x900 and 390x844 (keep them outside the repo)
E2E_SCREENSHOTS_DIR=/tmp/dockyard-shots E2E_BASE_URL=http://localhost:8080 npx playwright test tests/ui.spec.ts
```

The file manager and log viewer specs use the seeded Silo stack; the
host-side edit test writes into its project directory:

```bash
E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin E2E_UI_PASSWORD=dockyard-devstack-owner \
  E2E_FILES_STACK_DIR=/tmp/dockyard-devstack/hosts/homelab/volumes/dockyard_stacks/_data/silo \
  npx playwright test tests/ui-files.spec.ts tests/ui-logs.spec.ts
```

`tests/ui-terminal.spec.ts` needs exec on a real Engine (CI, `E2E_TERMINAL_*`).
`tests/pwa.spec.ts` and `tests/smoke.spec.ts` also run against the devstack;
only their final HTTPS assertions fail on plain `http://localhost` (CI runs
them behind the TLS proxies of `e2e/compose.yaml`). Design review rules:
[design/README.md](design/README.md#tests-and-screenshot-review).

## Generated artifacts

`api/openapi.json` and `web/src/lib/api/schema.d.ts` are generated and
committed. After changing any Huma operation or DTO run
`bash scripts/generate.sh` and commit both files.
`TestOpenAPISnapshot` and `generate.sh --check` (CI `policy` job) fail when
they are stale.

## Web UI embedding

```
web/build/fallback/   committed placeholder page (never deleted)
web/build/app/        `npm run build` output (git-ignored)
```

`web/embed.go` embeds `web/build` and serves `build/app` when it contains an
`index.html`, otherwise `build/fallback`. SvelteKit's static adapter only
writes `build/app`, so `go build ./...` and `go test ./...` always work on a
clean checkout without Node, and the manager image (which runs the npm build
first) always embeds the real UI.

Web UI structure, the generated-client workflow and PWA behaviour:
[web.md](web.md).

## Tests

- Unit tests live next to the code (`foo_test.go`) and must be Docker-free,
  deterministic and fast. Use `internal/clock` (`clock.NewFake`,
  `testutil.FakeClock()`) instead of sleeping; `testutil.Logger(t)` and
  `testutil.CaptureLogger()` for logs.
- Build tags: `integration` (needs a Docker Engine; CI extended workflow),
  `e2e` (browser/proxy end-to-end). Tagged tests go in `test/` or next to the
  code with `//go:build integration`.
- Fuzz targets (`FuzzXxx`) run their seed corpus in `go test`; the extended
  workflow runs real fuzzing.
- `extended.yaml` runs the slower suites (race, fuzz, Engine matrix,
  Compose fixtures, storage, filesystem security, fault injection, secret
  canaries, Playwright E2E, deploy smoke) on `main`, nightly and on demand:
  `gh workflow run extended.yaml --ref <branch> -f suites=race,fuzz`.
- Shared test infrastructure: `internal/testharness` (DinD Engines from
  `test/matrix/engines.json`, registry with fault proxy, Git server, MinIO,
  restic, TLS proxy), `internal/testutil/canary` (secret canaries),
  `internal/testutil/fscorpus` (traversal/archive/TOCTOU corpora), `e2e/`
  (Playwright). See [docs/testing/harness.md](testing/harness.md) and the
  release map [docs/testing/verification-matrix.md](testing/verification-matrix.md).

## Line endings

The repository is LF-only (`.gitattributes`); `policy-check.sh` rejects CRLF.
With `core.autocrlf=true` on Windows this is handled automatically for new
checkouts; run `git add --renormalize .` if an old checkout shows CRLF files.
