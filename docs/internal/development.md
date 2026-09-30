# Development

## Prerequisites

| Tool | Version | Notes |
| --- | --- | --- |
| Go | 1.27.1 (`go.mod` toolchain) | `CGO_ENABLED=0` works everywhere; no C compiler needed. |
| Node.js / npm | 26 (`.nvmrc`) / 11 | Only for the web UI and client generation. |
| golangci-lint | v2.13.2 | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2` |
| bash | Git Bash on Windows, any bash on Linux/macOS | All gates are bash scripts; there is no Makefile. |

Docker is **not** required locally, and no automated check needs it: the
tests are isolated unit tests with in-memory fakes. Only the image builds
need Docker; they run in CI.

The code lives at `https://github.com/neurekadev/docker-manager`; its GitHub
issues #1–#35 stay the written record of the roadmap and decisions.

## Local checks and CI

GitHub Actions CI is the gate for `main`: its `Lint`, `Unit Tests` and `Build`
jobs run on every push and images are published only when they pass. Tests
run in CI, not locally: before pushing, run only the fast non-test checks
for what you changed (gofmt, Prettier, `go build`/`go vet` of the changed
packages, `bash scripts/generate.sh` for generated artifacts).
`bash scripts/check.sh` mirrors the three CI jobs for an explicitly
requested full local run; it
fails fast with a summary. Run one or more classes with
`bash scripts/check.sh lint|unit-tests|build`:

| class | what runs |
| --- | --- |
| `lint` | gofmt on the tracked Go files; `npm --prefix web run format:check` (Prettier); golangci-lint v2.13.2, also with `GOOS=linux` on non-Linux hosts (govet runs inside golangci-lint); `scripts/policy-check.sh` (legacy Docker imports, CLI execution, CRLF, LICENSE, mockup); `npm --prefix web run lint` (ESLint) |
| `unit-tests` | `go test ./...` (`CGO_ENABLED=0`); `npm --prefix web run test` (Vitest: `*.spec.ts` in Node, `*.test.ts` in jsdom) |
| `build` | `npm --prefix web run build` + `node web/scripts/verify-build.mjs`; `go build ./...`; `bash scripts/build-static.sh` (static linux/amd64 and linux/arm64 binaries into `DIST_DIR`, default `dist/`) |

CI (GitHub Actions) runs on pushes to `main` and on
manual dispatch only; there is no pull-request trigger. Besides the three
classes above it builds the linux/amd64 and linux/arm64 manager and agent
images on native runners with
BuildKit and, on `main`, publishes them as `:edge` with BuildKit provenance
and SBOM attestations (`deploy/docker/*.Dockerfile`); the
`Build` job uploads the release
binaries as the artifact `release-binaries-linux`.

Not part of the gate or CI any more (run them by hand when relevant):

```bash
npm --prefix web run check     # svelte-check / TypeScript
bash scripts/generate.sh       # regenerate api/openapi.json, schema.d.ts, the job-engine lock table
bash scripts/generate.sh --check
bash scripts/license-check.sh  # dependency license allowlist (Go + npm production)
govulncheck ./...              # when adding or bumping dependencies
```

Race detection, fuzzing, coverage, the API breaking-change check and all
Docker-, browser- or proxy-backed suites were removed on 2026-09-25 with
the move to Forgejo (and stay removed after the move back to GitHub).

## Running locally

```bash
# terminal 1: manager on http://localhost:8080 (plain http allowed for localhost)
DOCKER_MANAGER_PUBLIC_URL=http://localhost:8080 DOCKER_MANAGER_DATA_DIR=./data \
  DOCKER_MANAGER_LOG_FORMAT=text go run ./cmd/docker-manager

# terminal 2 (optional): Vite dev server with HMR, proxies /api to :8080
npm --prefix web run dev
```

`go run` serves whatever UI is embedded: the committed placeholder page on a
clean checkout, or the real app after `npm --prefix web run build` (the build
writes `web/build/app`, which `web/embed.go` picks up automatically).

The agent refuses to run as non-root and targets Linux; run it in its
container (see the user documentation's Quickstart) rather than on a Windows host.

Without an agent the manager has no environments: setup, sign-in, users,
groups, settings, API tokens and the empty states work, but every Docker
screen needs an agent connected to a real Docker Engine. For the full stack
run the images with the Quickstart's compose file on a Linux host with
Docker ([deployment.md](deployment.md)); the Docker-free development stack
with fake Engines (`test/devstack`) was removed on 2026-09-25. Design
review rules: [design/README.md](design/README.md#tests-and-screenshot-review).

## Generated artifacts

`api/openapi.json` and `web/src/lib/api/schema.d.ts` are generated and
committed. After changing any Huma operation or DTO run
`bash scripts/generate.sh` and commit both files. `TestOpenAPISnapshot`
(part of `go test ./...`) fails when `api/openapi.json` is stale;
`generate.sh --check` also compares `schema.d.ts` and the job-engine lock
table but no longer runs in the gate or CI, so run it by hand. Breaking
API changes are reviewed in the `api/openapi.json` diff
([api/versioning.md](api/versioning.md)).

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

The standard suite is format/lint plus isolated unit tests (owner decision,
2026-09-25).

- Unit tests live next to the code (`foo_test.go`) and must be Docker-free,
  deterministic and fast; `go test ./...` runs all of them. They may use
  in-process fakes (`enginefake`, `restictest`, `regclient/regtest`,
  `streammux/muxtest`, `migrationtest`, `containerio/ciotest`), `httptest`
  servers, temporary directories and SQLite files under `t.TempDir()`; they
  never start containers, a Docker Engine, browsers, real registries,
  restic or smartctl (the restic and smartctl runners' tests re-execute
  the test binary as a fake).
- Use `internal/clock` (`clock.NewFake`, `testutil.FakeClock()`) instead of
  sleeping; `testutil.Logger(t)` and `testutil.CaptureLogger()` for logs.
- Shared test infrastructure: `internal/testutil/canary` (secret canaries),
  `internal/testutil/fscorpus` (hostile path and archive corpus, generated
  in memory).
- Web: Vitest, `*.spec.ts` for Node logic and `*.test.ts` for jsdom
  component tests ([web.md](web.md#tests)).
- There are no build-tagged (`integration`, `e2e`, `faultinject`), fuzz,
  race, benchmark, smoke or browser suites any more. What that leaves
  unverified is listed in [support-matrix.md](support-matrix.md#verification-status).

## Line endings

The repository is LF-only (`.gitattributes`); `policy-check.sh` rejects CRLF.
With `core.autocrlf=true` on Windows this is handled automatically for new
checkouts; run `git add --renormalize .` if an old checkout shows CRLF files.
