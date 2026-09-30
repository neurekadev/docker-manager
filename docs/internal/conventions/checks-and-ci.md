# Local checks, CI and repository rules

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

## Local checks and CI

GitHub Actions CI is the gate: it runs the full `lint`, `unit-tests` and `build`
jobs on every push to `main`, and publishes `:edge` only when they pass.
Tests run in CI only: never run `go test`, vitest, `npm --prefix web run
check`, `bash scripts/check.sh`, golangci-lint or `scripts/policy-check.sh`
locally unless the user explicitly asks for a local run (they take many
minutes on the Windows workstation). Write and update the unit tests; CI
runs them. Before pushing, run only fast non-test checks on what changed:
gofmt, Prettier on the changed web files, `go build`/`go vet` of the
changed Go packages and `bash scripts/generate.sh` when generated
artifacts change. When a CI run fails, fetch its log and fix forward.
`scripts/check.sh` mirrors the CI jobs for an explicitly requested local
run (`bash scripts/check.sh lint|unit-tests|build`):

- **lint:** gofmt, `npm --prefix web run format:check` (Prettier),
  golangci-lint v2.13.2 (also `GOOS=linux` on other hosts; govet runs
  inside it), `scripts/policy-check.sh`, `npm --prefix web run lint` (ESLint).
- **unit-tests:** `go test ./...` and `npm --prefix web run test` (vitest).
- **build:** web build + `web/scripts/verify-build.mjs`, `go build ./...`,
  `scripts/build-static.sh` (static linux/amd64 + linux/arm64 binaries).

CI (GitHub Actions) runs on pushes to `main` and manual
dispatch only (no pull-request trigger); it also builds the amd64 images and,
from `main`, publishes `ghcr.io/neurekadev/docker-{manager,agent}:edge`.
Images are amd64 only for now; arm64 would use the native `ubuntu-24.04-arm`
runner (never QEMU).
The only tests are isolated unit tests (see [tests.md](tests.md)); nothing starts
Docker, containers, browsers, real registries or restic.

## Generated / pinned artifacts

- `api/openapi.json`, `web/src/lib/api/schema.d.ts`: `bash scripts/generate.sh`.
- `web/package-lock.json`: commit with any `web/package.json` change (npm 11).
- Image digests, restic version/SHA-256: in `deploy/docker/*.Dockerfile`.

## Repository rules

- LF line endings only; no LICENSE file; never commit the UI mockup (#22).
- Branches `feat/<issue>-<slug>`, conventional commits, squash merges.
- No git tags, GitHub Releases or semver images; `main` publishes `:edge`.

## User documentation site

`docs/public` is the user documentation site. Its structure, content and
update rules, the site build and the screenshots: [user-docs.md](user-docs.md).
`scripts/policy-check.sh` (lint) checks that the Configuration page lists
every configuration variable, that no page names a removed one, and that
every docs link, anchor and `meta.json` entry resolves.
