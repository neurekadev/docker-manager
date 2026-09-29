# Adding an API operation

Binding conventions (split out of AGENTS.md). Read this file when your change touches this area.

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
   [authorization.md](authorization.md).
3. Errors: return `api.NotFound(...)`, `api.Invalid(msg, api.Field("body.name", "..."))`,
   `api.Conflict("stack_name_taken", ...)`, `api.PreconditionFailed`,
   `api.Unavailable`, `api.Internal(err)`. Plain errors become a 500 with the
   cause logged, never returned. Codes are stable snake_case; add every new
   code to `ErrorCodes()` (`errorcodes.go`) and `docs/internal/api/errors.md`.
4. Lists return `api.Page[T]` and embed `api.PageParams` (+ `api.SortParam`);
   page with `api.ScanPage` + `api.CursorFor`. Revisioned GETs embed
   `api.ETagHeader`; edits embed `api.IfMatchParam` and call
   `in.CheckIfMatch(api.RevisionETag(rev))`. Dangerous retries embed
   `api.IdempotencyKeyParam` and set `Idempotency: api.IdempotencyJob`
   (key passed to the job engine) or `api.IdempotencyStored` (response
   replay). Long operations return `api.Accepted(job)` (`*api.JobAccepted`,
   202 + Location, #26). SSE streams write through `api.StartSSE`.
   Conventions: `docs/internal/api/conventions.md`; streams: `docs/internal/api/streams.md`.
5. Run `bash scripts/generate.sh` and commit `api/openapi.json` and
   `web/src/lib/api/schema.d.ts`. Review the `api/openapi.json` diff for
   breaking changes and follow `docs/internal/api/versioning.md` (no automated
   contract check).
6. Agent protocol changes: keep `internal/protocol` and
   `docs/internal/protocol/agent-v1.md` in sync (their tests compare them).
7. Audit (#30) is automatic: `Register` records every non-GET call (action =
   capability key, or `invitation.create`-style keys derived from the
   operation ID for public/authenticated/owner; override with
   `AuditAction`). GET downloads/exports set `Audit: api.AuditAlways`.
   Enrich from the handler: `audit.AddTarget(ctx, domain.AuditTarget{...})`
   (created resources), `audit.SetDiff(ctx, before, after)` (rule/settings
   changes), `audit.SetDetail(ctx, k, v)` (IDs, names, counts, paths — never
   secrets or contents), `audit.SetAction(ctx, "stack.stop")` (selector
   operations), `audit.SetPrincipal(ctx, p)` (sign-in).
8. While the manager moves to a new server, `Register` refuses every
   non-GET operation with 409 `manager_moved` (`moveGuard`, audited). An
   operation a moving manager must still serve (sign-in, sign-out, the
   move routes) goes in `allowedWhileMoved` (`managermove.go`). A new
   manager in waiting mode answers every operation, GETs included, with
   503 `manager_move_waiting` (`waitingGuard`) except `allowedWhileWaiting`
   (health, capabilities, `get-move-status`); see
   [manager-move.md](manager-move.md).
