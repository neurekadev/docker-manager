# DockYard code conventions

Binding for every change. Specs: GitHub issues (#1 roadmap, #25 decisions,
#4 API catalog). Background: `docs/architecture/overview.md`,
`docs/adr/0001-foundation.md`.

## Local gate

`bash scripts/check.sh` must pass before every push (policy, generated
artifacts, gofmt/vet/golangci-lint v2.13.2/tests, web lint/check/test/build).
No Docker locally: Engine-dependent tests run in CI only.

## Package boundaries

- `cmd/*` only parse args/env and call into `internal/...`.
- Manager code lives under `internal/manager/...`; agent code under
  `internal/agent/...`. The agent never imports `internal/manager/...` and
  never listens on a socket (enforced by `internal/agent/nolisten_test.go`).
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
  Guide: `docs/architecture/engine-integration.md`.
- Prefer small focused packages over a shared `util` package.

## Adding an API operation

1. Put it in `internal/manager/api` (one file per resource, e.g. `stacks.go`).
2. Register with `api.Register` (never `huma.Register`/`huma.Get`):
   ```go
   api.Register(a, api.Operation{
       Operation: huma.Operation{
           OperationID: "get-stack", Method: http.MethodGet,
           Path: api.BasePath + "/stacks/{stackId}", Summary: "Get a stack",
           Tags: []string{"Stacks"},
       },
       Capability: "stack.read",      // or api.CapabilityPublic / Authenticated / Owner, or "stack.{action}" + CapabilityValues
       Scope:      api.ScopeResource, // none|instance|environment|resource
   }, handler)
   ```
   Operation IDs are kebab-case and stable; take them (and capability/scope)
   from `api/route-inventory.yaml` and flip the entry to
   `status: implemented` in the same PR (`TestRouteInventory`). Capability
   keys come from #17.
3. Errors: return `api.NotFound(...)`, `api.Invalid(msg, api.Field("body.name", "..."))`,
   `api.Conflict("stack_name_taken", ...)`, `api.PreconditionFailed`,
   `api.Unavailable`, `api.Internal(err)`. Plain errors become a 500 with the
   cause logged, never returned. Codes are stable snake_case; add every new
   code to `ErrorCodes()` (`errorcodes.go`) and `docs/api/errors.md`.
4. Lists return `api.Page[T]` and embed `api.PageParams` (+ `api.SortParam`);
   page with `api.ScanPage` + `api.CursorFor`. Revisioned GETs embed
   `api.ETagHeader`; edits embed `api.IfMatchParam` and call
   `in.CheckIfMatch(api.RevisionETag(rev))`. Dangerous retries embed
   `api.IdempotencyKeyParam` and set `Idempotency: api.IdempotencyJob`
   (key passed to the job engine) or `api.IdempotencyStored` (response
   replay). Long operations return `api.Accepted(job)` (`*api.JobAccepted`,
   202 + Location, #26). SSE streams write through `api.StartSSE`.
   Conventions: `docs/api/conventions.md`; streams: `docs/api/streams.md`.
5. Run `bash scripts/generate.sh` and commit `api/openapi.json` and
   `web/src/lib/api/schema.d.ts`. Breaking spec changes fail the
   `api-contract` workflow unless the PR has the `api-breaking-change`
   label (`docs/api/versioning.md`).
6. Agent protocol changes: keep `internal/protocol` and
   `docs/protocol/agent-v1.md` in sync (their tests compare them).
7. Audit (#30) is automatic: `Register` records every non-GET call (action =
   capability key, or `invitation.create`-style keys derived from the
   operation ID for public/authenticated/owner; override with
   `AuditAction`). GET downloads/exports set `Audit: api.AuditAlways`.
   Enrich from the handler: `audit.AddTarget(ctx, domain.AuditTarget{...})`
   (created resources), `audit.SetDiff(ctx, before, after)` (rule/settings
   changes), `audit.SetDetail(ctx, k, v)` (IDs, names, counts, paths — never
   secrets or contents), `audit.SetAction(ctx, "stack.stop")` (selector
   operations), `audit.SetPrincipal(ctx, p)` (sign-in).

## Audit (#30)

- Guide: `docs/architecture/audit.md`. Package `internal/manager/audit`.
- HTTP operations and job lifecycles (`job.queued/started/cancel_requested/
  finished`) are recorded by construction; never record them by hand.
- Other events (sign-in failures outside a route, lockouts, agent
  enrollment/rotation/revocation on `/agent/v1`, registry/Git credential
  use, owner-recovery CLI, scheduled work) call
  `audit.Record(ctx, domain.AuditEvent{Action: "agent.enroll", Actor:
  audit.AgentActor(id), Targets: ..., Outcome: ...})`; inside a DB
  transaction use `(*audit.Log).RecordTx(ctx, tx, ev)` (never `Record`: the
  single SQLite connection would deadlock). Scheduled work uses
  `audit.ServiceActor()`.
- Never pass secret values, tokens, file contents, `.env` values or error
  messages; the redaction layer is a safety net, not a licence. Record error
  classes (stable codes), not messages.
- The trail is append-only (DB triggers); there is no update/delete API.
  Diagnostics (#34) call `(*audit.Log).Verify(ctx)`.
- `audit.Capabilities()` lists `audit.read`/`audit.export` for the #17
  catalog (instance scope, owner-only by default, high-risk).

## Adding a migration

- New file `internal/db/migrations/<UTC YYYYMMDDHHMMSS>_<snake_name>.go`.
- In `init()`: `Migrations.MustRegister(Tx(up), Tx(down))` — call it directly
  (Bun derives the name from the caller's file name). `Exec(stmts...)` helps.
- Raw SQL, `STRICT` tables (`INTEGER`/`TEXT`/`REAL`/`BLOB`), timestamps as
  `TEXT` via Bun `time.Time` (UTC), IDs as `TEXT` UUIDv7 from `ids.New()`.
- Never edit a migration merged to `main`; add a new one.

## Tests

- Unit tests next to the code, Docker-free, deterministic: `go test ./...`.
- Docker/Engine tests: `//go:build integration`; browser/proxy: `//go:build e2e`
  (or under `test/`). They run in `.github/workflows/extended.yaml`.
- Time: production code takes a `clock.Clock` (`internal/clock`); tests use
  `testutil.FakeClock()` / `clock.NewFake`, `BlockUntilWaiters` + `Advance`.
  **No sleeps in assertions**, no `time.Now()` in logic tests depend on.
- Helpers: `testutil.Logger(t)`, `testutil.CaptureLogger()`,
  `testutil.Context(t)`, `migrationtest.WithFailing(...)`.
- Fuzz targets (`FuzzXxx`) keep a meaningful seed corpus via `f.Add`.

## Logging

- `log/slog` only (JSON by default); request-scoped logger via
  `logging.FromContext(ctx)` (carries `request_id`). No `fmt.Print*` outside
  `cmd/`.
- Never log secrets, tokens, passwords, credentials, keys, file contents,
  Compose/`.env` values, request bodies or query strings. Wrap anything
  possibly sensitive in `logging.Secret`. Config structs hold secrets as
  `logging.Secret`.

## Security defaults

- Containers run as UID 0 (decided, #25/#28); do not add non-root users.
- All `/api/v1` and `/agent/v1` responses are `no-store`; never cache API data
  in the service worker.
- Secrets at rest: `secrets.Keyring.Seal(value, "<table>/<id>/<field>")`.
- Client IP / scheme / host: `requestinfo.From(ctx)` (trusted proxies are
  resolved once; never read `X-Forwarded-*` or `RemoteAddr`). SSE goes
  through `api.StartSSE` (Huma) or `server/sse` (plain handlers; the one
  implementation), WebSockets through `server/ws`; `/agent/v1` handlers go in
  `server.Options.Agent` and reject with `server.AgentFailure`
  (`docs/deployment.md`, "For contributors").
- Identity (#16, ADR 0003): `internal/manager/auth` authenticates every
  `/api/v1` request (SCS session, CSRF, principal via `authz.WithPrincipal`
  for full sessions only). Handlers read `authz.PrincipalFrom(ctx)`; the
  evaluator allows the owner everything and denies everyone else until #17.
  Identity events enrich the #30 audit record of the request (`auth.TrailAuditor`).
  Never add auth routes outside Huma, never log passwords, codes, seeds or
  tokens (canary tests in `internal/manager/app/identity*_test.go`).
- New dependencies must pass `scripts/license-check.sh` and govulncheck; pin
  exact versions. Pin GitHub Actions by commit SHA with a `# vX.Y.Z` comment.

## Generated / pinned artifacts

- `api/openapi.json`, `web/src/lib/api/schema.d.ts`: `bash scripts/generate.sh`.
- `web/package-lock.json`: commit with any `web/package.json` change (npm 11).
- Image digests, restic version/SHA-256: in `deploy/docker/*.Dockerfile`.

## Repository rules

- LF line endings only; no LICENSE file; never commit the UI mockup (#22).
- Branches `feat/<issue>-<slug>`, conventional commits, squash merges.
- No git tags, GitHub Releases or semver images; `main` publishes `:edge`.
