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

## Local checks (fast path)

Forgejo CI is the gate: it runs the full `lint`, `unit-tests` and `build`
jobs on every push to `main` and publishes `:edge` only when they pass.
Before pushing, run only fast checks on what changed: gofmt, Prettier on
the changed web files, `go vet`/`go test` of the changed Go packages and
the web tests that cover changed web code. Do not run the full
`bash scripts/check.sh`, golangci-lint over the repository or
`scripts/policy-check.sh` locally unless asked (they take many minutes on
the Windows workstation). When a CI run fails, fetch its log and fix
forward. When the user says to skip local tests, skip the package tests
too. Details: `docs/internal/conventions/checks-and-ci.md`.

## Keep the documentation true

**Internal docs** (`docs/internal/`): every change that adds, changes or
removes behavior, a rule, a package, a route, a job kind, a configuration
variable, a protocol field or a convention updates the matching internal
docs in the same commit: the conventions file of its area, the
architecture guide, and the API or protocol contract. Deleting something
deletes its docs. When you notice a doc that no longer matches the code,
fix or remove it. A new area gets a conventions file and a row in the
index; keep `CLAUDE.md` itself short and move detail into the conventions
files.

**User-facing docs** (`docs/public/`, the documentation site) must match the
product at all times. In the same commit, update them whenever a change
alters what users see or do: installation and the deploy examples,
configuration variables and defaults, requirements and limits, UI flows,
and messages users must act on. Maintain them with care:

- Write for people who run Docker but are not developers: plain words,
  short sentences, one clear example per task, copy-ready commands.
- Keep pages short and task-oriented; no internals, architecture, API
  details for contributors or development setup.
- Add a page or section only for a real, recurring user need (a task users
  must do or a problem they will hit). Prefer extending an existing page;
  never add docs for the sake of having docs.
- Remove or correct anything that is no longer true.
- Build the site locally only when you change its code or structure
  (`npm --prefix docs/public ci && npm --prefix docs/public run build`).
