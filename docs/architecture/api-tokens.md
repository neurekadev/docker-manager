# API tokens (#31)

API tokens are a separately controlled credential type for scripts and
integrations. A token belongs to one user, carries an explicit list of
grants chosen at creation, and is evaluated on every request as **token
grants ∩ the user's current effective permissions** (#17).

| Where | What |
| --- | --- |
| `internal/manager/authsep` | `MintAPIToken` / `ParseAPIToken`: `dy_<id>_<256-bit secret>`; `Verifier` (SHA-256) and `VerifierMatches` (constant time). |
| `internal/manager/store/apitokens.go` | `api_tokens` (verifier only) and `api_token_scopes`; revocation, last use, credentials lookup. |
| `internal/manager/auth/apitokens.go` | Create/list/rename/revoke, `AuthenticateAPIToken` (middleware), `APITokenScopes` (permission service), `RevokeAllAPITokens` (restore hook). |
| `internal/manager/auth/session.go` | The `/api/v1` middleware: bearer requests drop the Cookie header, skip CSRF, authenticate the token and register in the request hub under the token. |
| `internal/manager/permissions/tokens.go` | `ValidateTokenScope`: a new token's grants must be held by its user. |
| `internal/manager/api/register.go` | `Operation.SessionOnly`, `AcceptsAPITokens`, the central token guard (`403 api_token_not_allowed`). |
| `internal/manager/api/apitokens.go` | The routes. |
| `internal/manager/authz/exec.go`, `internal/manager/api/exec.go` | `CanExec` / `AuthorizeExec`: the one exec check #8 uses. |

## Model

- **Value:** `dy_<tokenId>_<43 base64url chars>`, shown once in the
  creation response (and in a replay of that response for the same
  `Idempotency-Key`, sealed at rest). Only a SHA-256 verifier is stored and
  compared in constant time. The token ID is not secret: it lets a leaked
  value be matched to its record.
- **Record:** name (editable), user, creation time, expiry (required,
  bounded by the instance maximum; `NULL` only when the owner allows
  non-expiring tokens), last use (time and client IP from `requestinfo`,
  written at most once a minute), revocation (time, by, reason: `user`,
  `owner`, `user_disabled`, `credential_reset`, `restore`). The schema keeps
  owner, verifier, creation time, expiry and scope fixed and revocations
  final (triggers); tokens are deleted with their user.
- **Scope:** allow grants of catalog capabilities at instance, environment
  or resource scope (the same `PermissionRule` shape as #17 without an
  effect). At creation every grant must be grantable (owner-only
  capabilities never are) and held by the user right now: the capability
  is allowed on the grant's representative resource (all resources, a new
  resource in the environment, the resource itself or a new child of a
  stack/service), as in the effective-permission view. Afterwards nothing
  about the user's permissions is copied: `permissions.Service` loads the
  token's scope and the user's rules on every check (`policy.Subject.Token`),
  so narrowing a group, a user deny, a group move, disabling or deleting the
  user applies to the next request, and `AccessChanged` ends the token's
  open streams (`permissions_changed`).
- **Security settings** (owner, `PATCH /api/v1/settings/security`):
  `apiTokensEnabled` (default on; off makes every token fail with the
  generic 401 and closes their streams, creation answers
  `403 api_tokens_disabled`; turning it on again restores unrevoked tokens),
  `apiTokenMaxLifetimeDays` (default 90, applies to new tokens),
  `apiTokensNonExpiring` (default off).

## Authentication

`Authorization: Bearer …` on `/api/v1` (agent secrets are refused earlier
by `server.routeBoundaries`):

1. The Cookie header is removed: a bearer request is never also a session
   request. Cookie CSRF checks do not apply (there is no ambient
   credential).
2. The token must parse, exist, match its verifier, be neither expired nor
   revoked, belong to an active account, tokens must be enabled, and the
   account must have enrolled the factors the sign-in policy requires (a
   token never stands in for TOTP or a passkey). Any failure is the same
   `401 unauthenticated` (`WWW-Authenticate: Bearer error="invalid_token"`),
   logged without the value.
3. The request gets an `api_token` principal (`authz.Principal{Kind:
   KindAPIToken, UserID, TokenID}`) and is registered in the identity
   request hub under the token.

Tokens never create, list or revoke tokens and never step up. Creation
needs a full browser session, `api_tokens.create` and a recent step-up.

## Which routes accept tokens

`api.Register` decides centrally (`Operation.AcceptsAPITokens`): an
operation refuses tokens when its capability is `owner`, its capability (or
a selector value) is owner-only in the catalog, it sets `SessionOnly`, or it
declares cookie-only security. Refusing operations document
`security: [{cookieSession: []}]` (a bearer declaration panics at startup),
and a token caller gets `403 api_token_not_allowed` from the token guard
before idempotency handling or the handler run; the refusal is audited as
`denied` with the token ID. `api/route-inventory.yaml` marks session-only
routes with `sessionOnly: true`, and `TestRouteInventory` checks the served
security against it, so the planned Recovery Key routes
(`create-backup-repository`, `…/recovery-confirmations`,
`…/key-rotations`, #10) cannot ship accepting tokens.
`TestOwnerAndSessionRoutesRefuseAPITokens` calls every refusing operation
with the owner's own token.

Owner-only surfaces covered: users, groups and permission documents,
invitations, security settings, registry and Git credential
administration, other users' tokens (`/api/v1/api-tokens`), permission
previews, ownership, manager backup and system restore (owner-only catalog
keys `manager.backup`, `system.restore`, `backup.import`), and Recovery Key
administration (session-only).

## Jobs, streams and exec

- **Jobs:** handlers pass the request principal to `jobs.Request.Principal`;
  a token principal gives origin `api_token` with `InitiatorTokenID` and
  `InitiatorUserID` (audit metadata). The engine authorizes with the token
  scope ∩ the user's permissions at request and again at dispatch; a token
  revoked (or a grant lost) while the job is queued fails it with
  `authorization_revoked`. Scheduled policies run as the service identity.
- **Streams:** SSE, log and job event streams accept tokens like sessions.
  Revoking a token, disabling or deleting its user, disabling tokens or
  changing the factor policy closes its open streams at once (`event: close`,
  `session_expired`); a permission change closes them with
  `permissions_changed`; an expiry while a stream is open is caught by the
  stream sweeper (`auth.StreamSweepInterval`). Ending a user's *sessions*
  does not end their token streams.
- **Exec (#8):** use `api.AuthorizeExec(ctx, deps.Authorizer, container)`
  (or `authz.CanExec` with a checker). Exec needs `container.exec`; for a
  token it must be in the token's own grants — chosen explicitly at
  creation — and held by the user now. No other capability implies it.

## Revocation

| Trigger | Effect |
| --- | --- |
| `DELETE /api/v1/me/api-tokens/{tokenId}` | the user revokes one of their tokens (`user`) |
| `DELETE /api/v1/api-tokens/{tokenId}` | the owner revokes any token (`owner`) |
| disabling the user | every token revoked (`user_disabled`), for good |
| deleting the user | tokens deleted with the account |
| `revokeApiTokens: true` on `POST /users/{id}/factor-resets`, `POST /users/{id}/password-resets`, `PATCH /me/password`, `POST /auth/password-resets/redemptions` | every token of the account revoked (`credential_reset`); without it tokens are kept |
| `auth.Service.RevokeAllAPITokens(ctx, domain.RevokedRestore)` | the disaster-restore hook: #24 calls it after restoring the manager, so no restored token works (`restore`); audited as `api_token.revoke_all` |

Every revocation closes the token's open requests and streams through the
request hub and forgets its stored idempotent responses.

## Audit (#30)

Token-authenticated requests are recorded with `actor.kind = api_token`,
`actor.tokenId` and `actor.userId` (the token's user), like every other
request; refused calls as `denied`. Token creation records the name, the
grants in rule shorthand and the expiry (never the value); rename records a
name diff; revocations record the reason. The value never reaches logs,
audit records, later responses or the database (canary tests in
`internal/manager/app/apitokens_test.go`).

## Tests

| Test | Proves |
| --- | --- |
| `app.TestAPITokenActsOnlyWithinItsScope` | a token scoped to one capability on one resource performs it over HTTP and in the job engine (origin, token ID, user), is refused on other resources and on capabilities its user holds but the token does not, cannot call owner/session routes, ignores cookies, skips CSRF, streams jobs; mutations audited with the token ID; revocation closes the stream |
| `app.TestAPITokenNarrowedByGrantChanges` | group narrowing, user deny and group move narrow the token on the next request and close its streams; re-granting restores it |
| `app.TestAPITokenRefusalsAreGeneric` | malformed, unknown, wrong-secret, expired, owner-revoked, instance-disabled, reset-revoked, disabled-user, restore-revoked and deleted-user tokens all get the same 401; streams close |
| `app.TestAPITokensNeverSatisfyFactorPolicy` | a token stops while its account lacks required factors |
| `app.TestAPITokenCreationRules` | `api_tokens.create`, step-up, scope ⊆ current permissions, expiry bounds, non-expiring opt-in, idempotent replay, listing, rename, ownership, last use frequency, creation audit |
| `app.TestOwnerAndSessionRoutesRefuseAPITokens` | every owner and session-only operation refuses the owner's own token |
| `app.TestExpiredTokenStreamSwept` | the sweeper closes streams of expired tokens; session revocation leaves token streams |
| `permissions.TestTokenScopeIntersectsCurrentPermissions`, `TestQueuedTokenJobRecheckedAtDispatch`, `TestValidateTokenScope` | evaluation, dispatch recheck, scope validation |
| `api.TestAuthorizeExecNeedsExplicitTokenGrant` | the exec check |
| `scripts/smoke/deploy-smoke.sh` step `api-token` | the same with curl against the deployed images behind Caddy |

**Pending (#6, #8):** the literal #31 example — a token scoped to
`container.restart` on one container restarts it via curl and cannot read
its logs — needs the container routes. #6 must add, next to its restart
route, an app test that creates a token with `allow container.restart
@container:<env>/web`, restarts `web` with it (202, job origin `api_token`),
gets `404` for restart of another container, `403` for `GET
…/containers/web/logs` (or `404` when the container is otherwise hidden),
and `403 api_token_not_allowed` on an owner route; #8 must use
`api.AuthorizeExec` for the terminal route and test that the same token is
refused a terminal while a token with `container.exec` gets one.
