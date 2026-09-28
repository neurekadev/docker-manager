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
index; keep `CLAUDE.md` itself short and move detail into the conventions
files.

**User-facing docs** (`docs/public/`, the documentation site) must match the
product at all times. In the same commit, update them whenever a change
alters what users see or do: installation and the compose files,
configuration variables and defaults, requirements and limits, UI flows,
and messages users must act on. The site is the only install guide: the
`compose.yaml` and `.env` in Quickstart and "Add more servers" are the
deployment (no proxy examples, no `deploy/` examples); keep them in sync
with the install command text (`internal/manager/agents/install.go`) and
`docs/internal/deployment.md`. Maintain them with care:

- **Verify every statement against the code** (UI labels in `web/src`,
  defaults and limits in the Go code) before writing it. Never copy a claim
  from older docs without checking it.
- Write for people who run Docker but are not developers: friendly, plain
  words, short sentences, active voice, "you". Explain a term once where
  it first matters (an *environment* is one server with its agent).
- Lead with what the feature does for the user, then numbered steps, then
  a short "Good to know" list for limits and edge cases. Short and simple
  beats complete; skip anything a user never needs to decide or do.
- Name UI elements exactly as the app shows them, in bold, in the order
  the user clicks them (**Stacks → Create stack**).
- One clear example per task; copy-ready commands and complete YAML
  snippets (mark kept lines with `# ...keep the existing lines...`).
- No internals: no API paths, job kinds, package names, issue numbers or
  architecture. Error codes appear only in Troubleshooting, next to the
  words users see.
- Use callouts sparingly: only for data loss, security or a step users
  must not skip. Link between pages with absolute `/docs/<page>` links.
- Add a page or section only for a real, recurring user need (a task users
  must do or a problem they will hit). Prefer extending an existing page;
  never add docs for the sake of having docs.
- Remove or correct anything that is no longer true.
- Build the site locally only when you change its code or structure
  (`npm --prefix docs/public ci && npm --prefix docs/public run build`).
