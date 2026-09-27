# Local checks, CI and repository rules

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

## Local checks and CI

Forgejo CI is the gate: it runs the full `lint`, `unit-tests` and `build`
jobs on every push to `main`, and publishes `:edge` only when they pass.
Before pushing, run only fast checks on what changed: gofmt, Prettier on
the changed web files, and `go vet`/`go test` of the changed Go packages
(plus the web tests touching changed web code). Do not run the full
`bash scripts/check.sh`, golangci-lint over the repository or
`scripts/policy-check.sh` locally unless asked (they take many minutes on
the Windows workstation). When a CI run fails, fetch its log and fix
forward. `scripts/check.sh` mirrors the CI jobs for anyone who wants the
whole suite (`bash scripts/check.sh lint|unit-tests|build`):

- **lint:** gofmt, `npm --prefix web run format:check` (Prettier),
  golangci-lint v2.13.2 (also `GOOS=linux` on other hosts; govet runs
  inside it), `scripts/policy-check.sh`, `npm --prefix web run lint` (ESLint).
- **unit-tests:** `go test ./...` and `npm --prefix web run test` (vitest).
- **build:** web build + `web/scripts/verify-build.mjs`, `go build ./...`,
  `scripts/build-static.sh` (static linux/amd64 + linux/arm64 binaries).

CI (Forgejo Actions on code.neureka.dev) runs on pushes to `main` and manual
dispatch only (no pull-request trigger); it also builds the amd64 images and,
from `main`, publishes `code.neureka.dev/docker-manager/docker-{manager,agent}:edge`.
arm64 images are blocked until a native arm64 runner exists (never QEMU).
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

`docs/public` is the user documentation (Fumadocs, static Next.js export,
dark only, local search): pages in `docs/public/content/docs/*.mdx`, order
in `meta.json`. `.github/workflows/Docs.yaml` builds `docs/public/Dockerfile`
(nginx on port 3000) and publishes
`code.neureka.dev/docker-manager/docker-manager-docs:edge` on pushes to
`main` that change `docs/public/**`. Content rules: `CLAUDE.md`, "Keep the
documentation true". Build locally (`npm --prefix docs/public ci && npm
--prefix docs/public run build`) only after changing the site's code or
structure, not for text edits.
