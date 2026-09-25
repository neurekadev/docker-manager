# ADR 0003: Authentication libraries, sessions and the authorization evaluator

- Status: accepted
- Date: 2026-09-25
- Issues: #18 (auth foundation proof), #16 (identity), #17 (authorization),
  #31 (API tokens), #27 (single public origin); builds on ADR 0001

## Context

DockYard needs local accounts with passwords, TOTP, passkeys, recovery,
server-side sessions, CSRF protection and throttling, inside one static
(`CGO_ENABLED=0`) manager binary on Bun/SQLite, with every JSON route in
the Huma/OpenAPI contract. #18 asked to reuse maintained components for the
security-sensitive primitives and keep DockYard's own code to the product
workflows (owner, invitations, factor policy, permissions), and to prove the
combination before #16/#17 build on it.

Integrated frameworks were rejected in #18 already: Authboss has no
passkeys and mounts its own routes; PocketBase brings its own data model and
stateless tokens; theauth-go has no SQLite/Bun adapter and a fixed password
policy; an external identity server adds a deployable service. This ADR
records the library choice, how each is integrated and pinned, the threat
model, and what DockYard still owns.

## Decisions

### Pinned libraries

All versions are pinned exactly in `go.mod`. `scripts/build-static.sh`
fails unless the manager binary links exactly these versions (and the agent
links none of the browser-auth libraries); `scripts/license-check.sh`
covers their licenses; govulncheck runs in CI.

| Module | Version | License | Owns |
| --- | --- | --- | --- |
| `github.com/alexedwards/scs/v2` | v2.9.0 | MIT | session tokens (256-bit random), cookie writing, `RenewToken`, idle and absolute expiry, load/save middleware |
| `github.com/go-webauthn/webauthn` | v0.18.2 (pre-v1: bump deliberately, rerun the passkey tests) | BSD-3-Clause | WebAuthn registration/assertion verification: challenge, origin, RP ID hash, UP/UV flags, signature, sign counter, backup flags (+ `go-webauthn/x`, `fxamacker/cbor` MIT, `google/go-tpm` Apache-2.0, `tinylib/msgp` MIT) |
| `github.com/pquerna/otp` | v1.5.0 | Apache-2.0 | RFC 6238/4226 secret generation, `otpauth://` URI, HOTP with constant-time comparison (+ `boombuler/barcode` MIT, linked but unused) |
| `github.com/alexedwards/argon2id` | v1.0.0 | MIT | Argon2id hashing (`golang.org/x/crypto/argon2`), PHC encoding, constant-time comparison, parameter decoding |
| `golang.org/x/time` | v0.16.0 (already used by the #27 agent guard) | BSD-3-Clause | token buckets for throttling |
| `net/http.CrossOriginProtection` | Go 1.27 standard library | BSD-3-Clause | cross-origin request rejection (Sec-Fetch-Site / Origin) |
| `golang.org/x/text/unicode/norm` | v0.42.0 (already in the graph) | BSD-3-Clause | NFKC normalization of passwords |
| `github.com/descope/virtualwebauthn` | v1.0.5, **tests only** | MIT | software authenticator for passkey tests (never linked into a binary) |

Packages under `internal/manager/auth/` wrap each library with DockYard's
policy: `sessions`, `password`, `totp`, `passkey`, `csrf`, `throttle`, and
`auth.Kit`, which assembles them from the configuration at startup (an
unusable `DOCKYARD_PUBLIC_URL` fails startup before anything listens).

### Sessions: SCS with a DockYard Bun store (departure from `scs/bunstore`)

The provisional plan named SCS's Bun store. Upstream
`github.com/alexedwards/scs/bunstore` exists but is **not used**:

- it has no release (only pseudo-versions such as
  `v0.0.0-20251002162104-209de6e426de`), so it cannot be pinned to a
  reviewed release;
- it reads `time.Now()` directly and starts an unmanaged cleanup goroutine
  that logs with the standard library logger (DockYard injects its clock
  and uses `log/slog` only);
- `FindCtx` reports "not found" when the query fails, turning a database
  outage into silent sign-outs instead of an error;
- its module requires the MySQL and PostgreSQL drivers.

`internal/manager/auth/sessions.Store` implements the same
`scs.CtxStore` + `scs.IterableCtxStore` contracts (about 100 lines of Bun
queries) on the manager's single-writer connection, with the injectable
clock, and the manager sweeps expired rows every 15 minutes
(`Kit.RunHousekeeping`). Migration `20260925090000_create_sessions`
creates the `STRICT` table `sessions(token TEXT PK, data BLOB, expiry TEXT)`
plus an expiry index. It never uses SCS's CGO-based `sqlite3store`.

Cookie policy (`sessions.NewManager`): name `__Host-dockyard_session`
(browser-enforced `Secure`, `Path=/`, no `Domain`), `HttpOnly`,
`SameSite=Strict`, `Secure` always (browsers accept it on
`http://localhost`, the only plain-HTTP mode), persistent until the
session's expiry. `HashTokenInStore` is on: the database holds only
SHA-256 hashes of session tokens. Idle timeout 1 h and absolute lifetime
24 h by default (NIST SP 800-63B AAL2), configurable with
`DOCKYARD_SESSION_IDLE_TIMEOUT` / `DOCKYARD_SESSION_LIFETIME`. Handlers call
`RenewToken` on every privilege change (sign-in, second factor, step-up,
completed enrollment, password change) against session fixation.

Split of responsibilities (found while integrating #16): SCS owns the
token, the cookie, renewal and the absolute lifetime (row expiry and cookie
`Max-Age`). SCS's own idle timeout is **off**: it marks every loaded
session modified and rewrites it on every request, and it computes
deadlines from `time.Now()`. The identity middleware enforces both idle
timeout and lifetime on the injected clock at every request (and writes the
last-activity time at most once a minute), and additionally checks the
account (exists, active, current session epoch). The Bun store compares row
expiry with the wall clock, the clock SCS computed it with.

SCS limitation found by the proof: with `HashTokenInStore`, `Iterate`
yields the stored (hashed) tokens, so `Destroy` inside `Iterate` hashes
them again and deletes nothing. `sessions.RevokeWhere` deletes by the
stored token instead; revocation in #16 does not depend on it anyway
(below).

### Passwords

`password.Hasher` uses Argon2id with the versioned parameter set
`ParamsV1` = 64 MiB, t=3, p=2, 16-byte salt, 32-byte key (RFC 9106's
memory-constrained profile, about 100-250 ms on supported hosts, including
Raspberry Pi class arm64). Every encoded hash carries its parameters;
`Verify` reports `needsRehash` after a successful check when they differ
from `password.Current`, and the caller stores a fresh hash
(upgrade-on-login). Changing parameters means adding `ParamsV2` and
switching `Current`, never editing a set. Hashing concurrency is bounded
(at most 4) so parallel sign-ins cannot exhaust a small host's memory.
Unknown accounts are verified against a dummy hash, so every sign-in costs
exactly one Argon2id computation (tested).

Passwords are NFKC-normalized before hashing and checking. The policy
follows NIST SP 800-63B: minimum 8 code points, or a configurable minimum
(default 15, up to 64) when the owner enables the strict policy; maximum
256; long passphrases welcome; no composition rules, no expiry. Rejected:
the 38,451 passwords of at least 8 characters among the 100,000 most
common in the xato-net breach corpus (SecLists, MIT, embedded gzip, see
`internal/manager/auth/password/THIRD_PARTY.md`; a full offline breach
corpus such as HIBP is tens of gigabytes and out of scope), passwords
built mostly from the username/email/name or "dockyard"/"docker", and
single repeated or sequential runs.

### TOTP

`pquerna/otp` generates 160-bit secrets and the `otpauth://` URI and
computes/compares HOTP values. DockYard owns the window and replay rules
(`totp.Verify`): SHA-1, 6 digits, 30 s steps (what every authenticator app
supports), ±1 step of clock skew, and only steps strictly after the last
accepted step are valid, so a code can never be used twice, not even
within its own 30 seconds. The accepted step is persisted atomically
(compare-and-set) before the factor counts. Seeds are sealed at rest with
the secret-protection keyring (`secrets.Keyring.Seal`), shown once at
enrollment, activated only after a correct code, and never returned again.

### Passkeys

`go-webauthn` verifies both ceremonies. The relying party is derived from
`DOCKYARD_PUBLIC_URL` only: RP ID = its host name, the single accepted
origin = its exact origin (scheme, host, port). Requests that reach the
manager on its internal address or another host name can never complete a
ceremony (tested with a software authenticator for five origin/RP ID
mismatches in both directions). Every ceremony requires a discoverable
credential and user verification; attestation is not requested (`none`),
so any passkey provider works. Sign-in options are username-less (no
`allowCredentials`), so they never reveal accounts or credentials.
Ceremony state lives in the server-side session for at most 5 minutes and
is used once. The sign counter must increase when either side is non-zero;
a clone warning refuses the sign-in. Backup eligibility/state are stored
with each credential and their consistency is enforced by the library.
Changing the public host name invalidates passkeys (documented in
`docs/deployment.md`).

### CSRF and throttling

`csrf.Guard` wraps `http.CrossOriginProtection` with the public origin
added as trusted (a proxy may forward another `Host`). Unsafe methods from
a browser must be same-origin by `Sec-Fetch-Site`, or by `Origin` for older
browsers; requests with neither header are not from a browser and pass.
Bearer-token requests (#31) are exempt because they are not authenticated
by ambient cookies; the session middleware ignores cookies on them.
`SameSite=Strict` is the independent second layer. WebSocket upgrades are
origin-checked by `server/ws` (#27).

`throttle.Limiter` is a bounded table of `x/time/rate` buckets, keyed by
`requestinfo.ClientIP` (IPv6 per /64; forwarding headers are honoured only
from `DOCKYARD_TRUSTED_PROXIES`) and by the account name as typed, whether
or not it exists. Only failed attempts consume tokens: 20 per client IP
refilled every 6 s, 10 per account refilled every minute. A full table
refuses new keys instead of growing (fail closed).

### Authorization evaluator: small deterministic evaluator, not Casbin

#18 asked to evaluate Apache Casbin against #17's precedence. #17 requires:
owner bypass; otherwise the most specific matching **user** rule wins, then
the most specific matching **group** rule, then deny; exact resource beats
environment beats all-resources *within* a principal tier, but any user
rule beats any group rule even when the group rule is more specific;
tri-state Inherit/Allow/Deny; ambiguous duplicates rejected; wildcard
rules never grant future capability keys; API tokens (#31) intersect with
the user's effective permissions.

Casbin can express allow/deny with `priority` or a custom effector, but
only by encoding DockYard's two-tier specificity order into numeric policy
priorities computed outside Casbin, plus custom matching functions for the
scope hierarchy. The decision logic would then be split between a string
DSL model file, generated priorities and Go glue; duplicate/ambiguity
rejection, the token intersection and the "why" explanation the #17 editor
must show are still application code; and Casbin adds an expression
evaluator and a sizeable dependency graph to the static binary. The model
is neither simpler to audit nor smaller than the rule itself, which is a
sort over at most a few dozen rules per request.

Decision: #17 implements a small deterministic evaluator in Go (pure
function over the user's rules, group rules and the resource's scope
chain, returning the decision and the rule that produced it) with a
table-driven decision corpus, and does not add Casbin. Until #17 lands,
`authz` uses an owner-only evaluator: the instance owner is allowed
everything, everyone else is denied (deny by default). #17 implemented it
as `internal/manager/authz/policy` (evaluator and decision corpus) and
`internal/manager/permissions` (rule storage and the manager's
Authorizer); see `docs/architecture/authorization.md`.

### Threat model

| Threat | Mitigation |
| --- | --- |
| Account enumeration | Identical `401` body and one Argon2id computation for unknown, wrong-password and disabled accounts; username-less passkey options; invitation/reset redemption answers one generic error; per-account throttle keys exist for unknown names too. |
| Credential stuffing / brute force | Per-IP and per-account throttling of failures (`429 rate_limited` + `Retry-After`); blocklist and length policy; TOTP/recovery/step-up attempts share the limits; invitation and reset codes are 256-bit and throttled. |
| CSRF | `CrossOriginProtection` on every unsafe `/api/v1` request, `SameSite=Strict`, `__Host-` cookie, bearer requests never use cookies. |
| Session fixation / theft | Tokens are generated only by SCS, renewed on every privilege change, stored hashed, `HttpOnly`/`Secure`; idle + absolute expiry; revocation is immediate (below). |
| Replay | WebAuthn challenges single-use and 5-minute bounded, counters checked; TOTP steps single-use; recovery, invitation and reset codes consumed atomically. |
| Reverse proxy origin / RP ID confusion | RP ID and origin only from `DOCKYARD_PUBLIC_URL`; first-run setup refuses non-HTTPS or wrong-host requests (`requestinfo.CheckSecureOrigin`, `403 insecure_origin`); forwarding headers honoured only from trusted proxies. |
| Stolen database | Argon2id hashes, hashed session tokens and one-time codes (only verifiers stored), TOTP seeds sealed with the key file kept outside the database. |
| Secrets in logs | Passwords, codes, seeds and tokens are never logged or returned after creation; canary tests assert it (`internal/testutil/canary`). |

### Update policy

- Dependabot/renovate-style bumps are manual: read the changelog, bump one
  library per change, rerun `go test ./internal/manager/auth/...` (the
  proof suite), `scripts/build-static.sh`, `scripts/license-check.sh` and
  govulncheck.
- go-webauthn is pre-v1: review API and default-policy changes in the
  release notes (attestation, UV, backup flags) before bumping.
- Argon2id parameter increases go through a new `ParamsVn`, never an edit.
- Security advisories for any of these modules are patched out of band.

### Identity decisions made while implementing #16

- **No first-run setup token.** Setup is protected by HTTPS on the public
  origin (`403 insecure_origin`), single use and race-safe (a partial
  unique index admits one owner). An exposed, not yet set-up instance can be
  claimed by the first visitor; the deployment guide says to finish setup
  right after the first start, and owner recovery needs data-volume access.
- **Revocation by session epoch.** Each account has a session epoch stored
  in its sessions and checked on every request; disabling, credential
  changes, resets, revocations and policy changes bump it. In-flight
  requests and streams are cancelled through an in-process hub at once, and
  a sweeper (15 s) catches changes made by another process (the
  owner-recovery CLI).
- **Factor policies** (`none`, `totp`, `passkey`, `either`, `both`) are
  alternatives of factor sets (`internal/manager/auth/factors.go`); a
  recovery code stands in for TOTP/passkey after a password. Accounts
  lacking enrolled factors get a limited enrollment session with a
  deadline (grace period); the owner has none.
- **Recovery codes**: ten 80-bit codes, stored as SHA-256 bound to the user.
  **Invitation / reset / owner-recovery codes**: 256-bit with prefixes
  `dyi_`, `dyr_`, `dyo_`, stored as SHA-256 verifiers, consumed atomically.

### Residual app-owned workflows

DockYard code owns, on top of the primitives: first-run owner setup
(race-safe, single-use, HTTPS-only), invitation issue/redeem, account
status (disable/reactivate, owner protection), the instance sign-in policy
(strict passwords; required factors none/TOTP/passkey/either/both; staged
enrollment sessions), factor enrollment/removal, recovery codes,
owner-mediated password and factor resets, the owner-recovery CLI,
step-up windows, session revocation by per-user session epoch (every
request re-checks it, so disabling a user or resetting a credential ends
their sessions and open streams at once), credential records and the Huma
routes. #16 implements them; #17 the evaluator; #31 API tokens.

## Consequences

- The manager binary stays static and pure Go; the auth libraries add
  about 2 MB.
- Passkeys are tied to the public host name; moving DockYard to another
  host name requires users to register passkeys again (or use TOTP /
  recovery codes / owner reset).
- Session revocation does not depend on SCS iteration; SCS remains
  responsible for tokens and cookies only.
