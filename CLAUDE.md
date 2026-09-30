# Docker Manager

A self-hosted web UI and API for Docker hosts: the manager (`cmd/docker-manager`,
`internal/manager`, `web/`) and the Docker Agent on each host
(`cmd/docker-agent`, `internal/agent`). Code: https://code.neureka.dev/docker-manager/docker-manager.
Specs are the GitHub issues of `neurekadev/dockyard`, kept as the written
record (#1 roadmap, #25 decisions, #4 API catalog).

## Read only what you need

The binding rules live in `docs/internal/conventions/`, one file per area.
Before changing code, open `docs/internal/conventions/README.md` (the index),
find the rows for the code you will touch and read only those files. Do not
read the whole `docs/` tree; open an architecture guide, the API or protocol
contract only when a conventions file points you there or you need its
detail. User-facing documentation lives in `docs/public/`.

## Always

- Never log, audit, return, persist or put in job inputs a secret: tokens,
  passwords, keys, credentials, the Recovery Key, file contents, Compose
  or `.env` values, environment values, request bodies or query strings.
- Docker Engine and Compose only through the agent's adapters
  (`internal/agent/engine`, `internal/agent/compose`); never the docker CLI,
  `github.com/docker/docker` or the Docker socket by hand.
- Keep transport DTOs (`internal/manager/api`), database models
  (`internal/manager/store`) and domain types (`internal/domain`) separate.
- Tests are isolated unit tests only; never add integration, end-to-end,
  fuzz, race, benchmark or other extended suites unless explicitly asked.
- LF line endings, no LICENSE file, never commit the UI mockup;
  conventional commits, branches `feat/<issue>-<slug>`, squash merges; no
  git tags or releases (`main` publishes `:edge`).

## Local checks (tests run in CI only)

Forgejo CI is the gate: it runs the full `lint`, `unit-tests` and `build`
jobs on every push to `main` and publishes `:edge` only when they pass.
**Never run tests locally**: no `go test`, vitest (`npm --prefix web run
test`), `npm --prefix web run check`, `bash scripts/check.sh`,
golangci-lint or `scripts/policy-check.sh`, unless the user explicitly asks
for a local run. Write and update the unit tests; CI runs them. Before
pushing, run only fast non-test checks on what changed: gofmt, Prettier on
the changed web files, `go build`/`go vet` of the changed Go packages and
`bash scripts/generate.sh` when generated artifacts change. Subagents
follow the same rule. When a CI run fails, fetch its log and fix forward.
Details: `docs/internal/conventions/checks-and-ci.md`.

## Keep the documentation true

**Internal docs** (`docs/internal/`): every change that adds, changes or
removes behavior, a rule, a package, a route, a job kind, a configuration
variable, a protocol field or a convention updates the matching internal
docs in the same commit: the conventions file of its area, the
architecture guide, and the API or protocol contract. Deleting something
deletes its docs. When you notice a doc that no longer matches the code,
fix or remove it. A new area gets a conventions file and a row in the
index; keep this file short and move detail into the conventions files.

**User docs** (`docs/public/`, the documentation site) are part of the
product. A change that alters what users see or do is **not done** until
the user docs match it, in the same commit: UI labels and flows, defaults
and limits, configuration variables (the Configuration page lists every
one), requirements, install and upgrade steps, and messages users act on.
Read `docs/internal/conventions/user-docs.md` before you touch them; its
rules are binding:

- **Verify every statement against the code** before you write or keep it:
  labels in `web/src`, numbers in the Go/TS code, behavior in the code
  path. Unverifiable means it stays out. Never copy older docs unchecked.
- **Lean and simple**: what it does for the user, numbered steps, a short
  "Good to know". No internals (API paths, job kinds, packages, issue
  numbers, architecture), no over-explaining, no bloat, no new pages
  except for a new major feature. The sidebar structure is fixed there.
- **Single pane of glass**: describe the app's way of doing things; show a
  shell step only where the app has none.
- `scripts/policy-check.sh` (CI) fails on a variable missing from or
  stale in the docs, a broken docs link or anchor, or `meta.json` out of
  step with the pages. Labels and behavior are on you.
