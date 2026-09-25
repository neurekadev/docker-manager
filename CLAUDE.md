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
   keys must exist in the #17 catalog (`internal/manager/authz/catalog`,
   checked by `TestRouteInventory`); authorize and shape responses as in
   "Authorization (#17)" below.
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
- `audit.read`/`audit.export` are instance-scoped, high-risk catalog
  capabilities (no group holds them until the owner grants them).

## Authorization (#17)

Guide: `docs/architecture/authorization.md`. Owner bypass, then the most
specific user rule, then the most specific group rule, then deny.

- **Declare a capability:** add a `catalog.Capability` to
  `internal/manager/authz/catalog/entries.go` (key `<type>.<action>`, type,
  plain-language label, description, compatible scopes via
  `res(...)`/`instEnv`/`instRes(...)`, `high(...)` for risky actions,
  `adv(...)` for rare ones). Never add generic read/write keys; never rename
  a key. Every route, job kind (`jobspec.Spec.Capability`) and bus event
  type needs one (`TestRouteInventory`,
  `TestEveryJobKindHasCatalogCapabilities`,
  `TestEveryEventTypeHasAVisibilityRule`). File keys are per root:
  `stack.files.*`, `volume.files.*`.
- **Check:** one checker per request: `c, p, err := api.CheckerFor(ctx,
  deps.Authorizer)`; `c.Can("container.restart", res).Allowed`. Build
  resources with `authz.Resource{Type: catalog.TypeContainer, ID: name,
  EnvironmentID: env, Parents: []authz.ResourceRef{{Type: "service", ID:
  authz.ServiceID(stackID, svc)}, {Type: "stack", ID: stackID}}}` (nil
  Parents → the type's Locator), `authz.EnvironmentResource(id)`,
  `authz.InEnvironment(type, env)` (creation), `authz.Instance()`.
- **Resource graph:** register `perms.RegisterLocator(type,
  permissions.LocatorFunc(...))` (`app.Manager.Permissions()`) returning
  `permissions.Location{Found, EnvironmentID, Parents}`; call
  `perms.ForgetResource(ctx, ref)` after deleting a resource through
  DockYard.
- **Shaping:** `v := authz.ViewOf(c, res)`: `Hidden` → drop from lists/
  counts/streams, 404 on direct access; `Minimal` → only identity/status
  fields (`catalog.ResourceType.Minimal`); `Full` → everything. DTOs carry
  `view` and `actions` (`api.Actions(v)`); actions still check
  `v.Has(key)` (403 when visible but not granted). Events:
  `authz.EventVisible(c, e)`. Jobs: `c.Can("job.read",
  authz.JobResource(j))` (targets or the kind's own capability).
- **Jobs:** the engine authorizes `spec.Capabilities(targets, input)` on
  every target at request and again at dispatch; never authorize job work
  by initiator.
- **Tests:** `internal/manager/authz/authztest`: `Only(user, rules...)` /
  `New().Member().Group().User().Token().Locate()`, `Authenticate(h)`,
  `Routes(t, params, prefixes...)`, `Split(calls, caps...)`,
  `Discoverable(...)`, `AssertOnly(t, h, user, allowed, denied)`,
  `AssertAbsent`. Rule shorthand `"allow container.restart
  @container:<env>/web"` (`policy.ParseRule`). Extend
  `authz/policy/testdata/corpus.yaml` for new precedence cases.
- Permission/group changes are owner-only, need step-up, are revisioned,
  audited with diffs and end the affected users' streams
  (`auth.Service.AccessChanged`); session tokens are not rotated.

## API tokens (#31)

Guide: `docs/architecture/api-tokens.md`. Tokens are `dy_<id>_<secret>`
(`authsep.MintAPIToken`), verifier-only at rest, owned by one user, with
explicit grants; every check is token grants ∩ the user's current
permissions (the permission service does it; handlers need nothing
special). The identity middleware authenticates bearer requests (cookie
dropped, no CSRF, generic 401).

- **Routes refusing tokens:** owner routes, owner-only catalog keys and
  cookie-only security refuse tokens automatically; set
  `Operation.SessionOnly: true` (and `sessionOnly: true` in the route
  inventory, checked by `TestRouteInventory`) for anything else that must
  need an interactive session (sign-in/factor flows, token management,
  Recovery Key administration). Never declare the bearer scheme on them.
- **Jobs:** pass the request principal (`CheckerFor`'s `p`) as
  `jobs.Request.Principal`; tokens get origin `api_token` automatically.
- **Exec (#8):** authorize terminals only with `api.AuthorizeExec` /
  `authz.CanExec` (tokens need `container.exec` in their own grants).
- **Streams:** tokens are registered in the identity request hub; revoking
  closes them. Nothing to do in stream handlers beyond `api.StartSSE` /
  `CloseIfRevoked`.
- **Restore (#24):** call `auth.Service.RevokeAllAPITokens(ctx,
  domain.RevokedRestore)` after a manager restore.
- Never log, audit or return a token value; tests register created tokens
  as `canary.APIToken`.

## Registry connections (#19)

Guide: `docs/architecture/registries.md`. Owner-administered, write-only
registry credentials (`internal/manager/registries`, `app.Manager.Registries()`).

- **Never accept a credential in a request or job input.** Resolve the
  image's connection with `Registries().Select(ctx,
  domain.RegistrySelectRequest{Reference, EnvironmentID, StackID,
  ConnectionID})` (map `*domain.AmbiguousRegistryError` →
  `ambiguous_registry_connection`, revoked → `registry_connection_revoked`
  via the API's `registryError`) and put the selected ID into the job input
  as `jobspec.CredentialRefs` (`"registryConnections": [id]`). The engine
  resolves it to `protocol.CommandSecrets` at every dispatch and audits the
  use; a deleted/revoked connection fails the job (`credential_unavailable`).
- **Agent executors** read `sc.Secrets` (memory only, never journaled) and
  call `regauth.ForReference(sc.Secrets, ref, required)` /
  `regauth.All(sc.Secrets)` for the Engine/Compose adapters. Never fall
  back to anonymous when the input named a connection.
- **Manager-side digest checks** (#20): `Registries().Check(ctx,
  registries.CheckRequest{...})` (cached, deduplicated, rate-limit aware,
  `regclient` error classes). Image references and hosts: `internal/imageref`.
- Tests: fake registry `regclient/regtest`; register secrets as
  `canary.RegistryCredential`.

## Agent transport (#3)

Manager side: `internal/manager/agents` (`Service`: enrollment, agents,
environments; `Hub`: live sessions). Agent side: `internal/agent/session`
(outbound session client), `internal/agent/state` (install ID, credential,
handed-over tokens), `internal/agent/runtime` (control loop). Protocol:
`docs/protocol/agent-v1.md`.

- **Call an agent** (named, bounded, non-job operation):
  `out, err := hub.RequestEnvironment(ctx, envID, protocol.ReqContainerList, input, 0)`.
  Errors: `jobs.ErrAgentOffline` (map to 503 `unavailable`),
  `*agents.RequestError{Code}` (agent `error` frame; map per the protocol
  doc), `agents.ErrRequestTimeout` (504 `timeout`). Never retry mutating
  requests automatically. The request name must be in
  `protocol.RequestNames()` and advertised in the agent's capabilities.
- **Serve a request on the agent:** add a `session.RequestHandler` to
  `runtime.Options.Requests` (keyed by request name); return output (JSON
  encoded) or `&session.HandlerError{Code: protocol.CodeNotFound, ...}`.
  The handler's ctx ends at the request deadline; at most 16 run at once per
  session. The session advertises every registered name in the
  capabilities' `requests`.
- **Jobs:** do not talk to agents for job work; enqueue in the job engine.
  `Hub` is the engine's `jobs.AgentDispatcher`; agent executors go in
  `runtime.Options.Executors`.
- **Reconcile after reconnect:** inventory owners register
  `hub.AddReconciler(func(ctx, s *agents.Session) error)` (re-read what
  changed while the agent was away). The environment is reported online only
  after every reconciler returned.
- **Events and file invalidations:** the agent publishes with
  `client.Events().Publish(protocol.EventPayload{...})` /
  `client.FileInvalidations().Publish(...)` (per-session `seq`, drops count
  as gaps). The manager republishes them on the in-process bus
  (`internal/manager/events`, `Bus.Subscribe`) as `docker.event` /
  `files.invalidated`, plus `environment.resync` after reconnects and gaps;
  environment/agent/enrollment changes are published there too (#23
  consumes the bus). File-scope paths on the bus are internal: filter by the
  reader's file permissions before anything leaves the manager.
- **Identity:** agent ID ≠ Engine ID; `(engineId, installId)` identifies an
  installation; one active agent per Engine and per environment. Agent
  secrets are `dye_…` (enrollment) / `dya_…` (credential), minted with
  `authsep.Mint*`, stored as verifiers only, never logged.

## Observation (#5)

Guide: `docs/architecture/metrics.md`. Agent: `internal/agent/observe`
(procfs sampler, container stats, ring, `engine.info`/`host.metrics`, Docker
event relay). Manager: `internal/manager/observe` (collector, inventory
cache, event journal) and `internal/manager/metrics` (separate
`<data>/metrics.db`, own migrations in `internal/db/metricsmigrations`).

- **Read metrics:** `Store.Query(ctx, domain.MetricQuery{Kind:
  domain.MetricContainer, Name: containerName, ...})` (container charts,
  #6/#7), `Store.Latest`; values are `nil` for gaps, never 0. Units: CPU %
  of the environment's total cores, bytes, bytes/s.
- **Inventory:** `observe.Service.Inventory(envID)` (last known, also
  offline). Refreshes are triggered by `docker.event`,
  `agent.capabilities_updated` and `environment.resync` on the bus.
- **Live invalidations:** `metrics.sampled` (`Members` = container names,
  internal: filter per member with `authz.ContainerMetricsVisible`) and
  `inventory.updated` on the bus; `stream-environment-events` relays them
  through the per-environment `observe.Journal` (cursor replay, resets).
- Sample keys are `(series, 10 s slot)`: ingestion is idempotent; never add
  a path that writes samples without going through `Store.Ingest`.
- `metrics.db` is expendable and excluded from manager-state backups (#10).

## Docker resources (#6)

Guide: `docs/architecture/docker-resources.md`. Manager:
`internal/manager/resources` (`app.Manager.Resources()`); agent:
`internal/agent/resources` (wired by the runtime); wire types and the
create-form validation: `internal/protocol/docker.go`; in-memory Engine for
tests: `internal/agent/engine/enginefake`.

- Read Docker objects through `resources.Service` (`ListContainers`,
  `InspectContainer`, ...: agent requests scoped by environment, errors are
  `*domain.DockerError` with stable codes); never a second path to the
  agent for the same data.
- DockYard's labels (`protocol.Label*`, prefix `dev.neureka.dockyard.`)
  and Compose's are reserved: user input may not set them. Stack
  membership: `protocol.StackRef` (`Managed`: working directory in a
  verified stack root) plus `resources.Service.StackManaged`; containers of
  a managed stack are changed through the stack, never directly
  (`stack_managed`).
- Job executors return `jobexec.ClassedError` (class + recovery) for
  failures users must tell apart (Engine/registry codes, refusals).
- Hooks: #7 installs `SetStackResolver` (Compose project -> stack ID).
  Pulls select their registry connection through `registries.Service`
  (#19; `jobspec.CredentialRefs` in the input, `regauth` on the agent).
- Recreate specifications of standalone containers created through
  DockYard: `resources.Service.ManagedSpec` (sealed; never return
  environment values).

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
  permission service (`internal/manager/permissions`, #17) decides.
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
