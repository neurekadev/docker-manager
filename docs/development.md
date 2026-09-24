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
