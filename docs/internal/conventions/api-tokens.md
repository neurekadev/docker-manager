# API tokens (#31)

Binding conventions (split out of AGENTS.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/api-tokens.md`. Tokens are `dy_<id>_<secret>`
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
