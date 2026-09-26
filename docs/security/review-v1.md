# Security review v1 (#12)

A review of the whole codebase against the #12 security checklist, done
on 2026-09-25 on `main` at the start of track R3 (after the #22 UI tracks
merged). For each item: what was read, the tests that hold it in place,
the finding, and what remains.

> **Update 2026-09-25.** After this review the code moved to Forgejo
> (`https://code.neureka.dev/docker-manager/docker-manager`) and the owner reduced the
> automated checks to format/lint, isolated unit tests and a test-free
> build. The release verification map, the Playwright browser and proxy
> specs, the deploy smoke test, the Docker-backed suites (Engine matrix,
> proxy agent sessions, `TestEngineSelfProtection`), fuzzing and the race
> detector were removed. Tests named below that still exist are unit tests
> and run on every push to `main`; evidence that came only from the removed
> suites is marked **(removed)** and is no longer verified by automated
> tests. The canary unit tests (secrets in logs, audit, storage, job output,
> API responses and the support bundle) remain.

Threat model in one paragraph: the manager holds credentials for, and
through its agents controls, every enrolled Docker Engine. Docker socket
access is root on that host. So the manager's authentication, authorization
and secret handling protect root on every host, and an attacker who owns
the manager (or its data volume and key) owns every host. Browsers, API
clients and remote agents reach the manager only through one HTTPS origin
behind the operator's reverse proxy.

## Findings

| # | Area | Finding | Severity | Resolution |
| --- | --- | --- | --- | --- |
| F1 | Capability boundaries | `stack.create` was marked **normal** risk although creating a stack writes a whole Compose definition that, once deployed, can bind host paths or the Docker socket and run privileged containers, i.e. host root. `stack.definition.write` was already high risk. The risk mark drives the permission editor's high-risk labels and previews, so an owner could delegate host-root power without a warning. | medium | fixed: `stack.create` is high risk with a description saying so; `TestHostAccessCapabilitiesAreHighRisk` pins every capability that grants code execution or host paths (stack create/definition/files, container create/exec, volume file writes, restores, agent enrollment, repositories) |
| F2 | Invite redemption | One-use redemption was tested sequentially only. Reviewed `auth.RedeemInvitation`: the invitation is consumed and the account created in one transaction with a conditional consume, so concurrent redemptions cannot both succeed. No bug. | none | added `TestConcurrentInvitationRedemptionIsOneUse` (six simultaneous redemptions: exactly one account, the invitation ends redeemed) |
| F3 | Jobs and sessions | #12 asks for in-flight recovery after the requester signs out; no test covered it. Jobs carry their principal and are re-authorized at dispatch against the user's permissions, not the session, so a sign-out does not orphan or fail them. No bug. | none | added `TestJobOutlivesTheRequestersSession` |
| F4 | Dependencies | govulncheck v1.8.0: no reachable vulnerability; GO-2026-5932 (`golang.org/x/crypto/openpgp`, unmaintained) is in a required module but not imported. `npm audit --omit=dev`: 5 low (`cookie` < 0.7.0 via `@sveltejs/kit`); the UI ships as a static SPA (adapter-static), so SvelteKit's server-side cookie parsing never runs. | low | accepted; re-check by hand (govulncheck, `npm audit`) with every dependency update; no CI job runs them any more |

No other defects were found. The remaining items below record what was
checked.

## Owner bootstrap

- Read: `auth.Service.SetupOwner`, `SetupOpen`, `requestinfo.CheckSecureOrigin`,
  the unique owner index in the store, the setup import routes
  (`api/backup_imports.go`).
- Setup needs HTTPS on the public origin as seen through a trusted proxy,
  is rate limited per IP, and the database admits exactly one owner:
  `TestConcurrentFirstRunSetupCreatesOneOwner`, `TestSetupRefusesInsecureOrigin`,
  `TestOneOwnerAndUniqueUsernames`, `TestOwnerIsProtected`. The browser
  flow (`ui.spec.ts`) and the check against the published images
  (`smoke:owner-setup`) were **(removed)**.
- Limitation: there is no setup token (#25 decision): whoever reaches the
  origin first after the first start becomes the owner. Until then the
  public setup import routes also let an anonymous client make the manager
  test an S3 endpoint URL or a path below `DOCKER_MANAGER_BACKUP_LOCAL_ROOTS`
  (rate limited, HTTPS origin only, refused once an owner exists). Operators
  must complete setup right after the first start or restrict the origin
  at the proxy until then (documented in `docs/guide/first-run.md`).

## Invite redemption and revocation

- Read: `CreateInvitation` (owner, recent authentication, 256-bit
  `dyi_` code, SHA-256 verifier only, code in the URL fragment),
  `RedeemInvitation`, `RevokeInvitation`.
- Unknown, expired, revoked, used and wrong-email codes fail identically
  and count against the per-IP failure budget: `TestInvitations`,
  `TestConcurrentInvitationRedemptionIsOneUse` (F2).

## Restricted default group

- New accounts join the default group, initially **Restricted** with no
  grants; the invariant that a default group always exists is enforced in
  the store. Lists, counts, search and streams hide what a user may not
  see: `TestRestrictedUserSeesNoResources`, `TestRestrictedUserSeesNothing`,
  `TestRestrictedDefaultAndGroupDocuments`, `TestDefaultGroupInvariant`,
  `TestSearchRestrictedUserFindsNothing`.

## Capability and override boundaries

- Read: the evaluator (`authz/policy`: owner bypass, most specific user
  rule, most specific group rule, deny), the catalog, `api.Register`'s
  per-route capability, job authorization at request and dispatch.
- `TestDecisionCorpus`, `TestEveryCapabilityScopeAndOverride` (every
  capability × scope × override), `TestRouteInventory` (every route
  declares a catalog capability), `TestEveryJobKindHasCatalogCapabilities`,
  `TestMetricsAndRestartOnlyThroughRealRoutes`, `TestRecheckAtDispatch`.
- Finding F1 (risk labelling). Limitation: a grant of `stack.create`,
  `stack.definition.write`, `stack.files.write` or `container.exec` on an
  environment is, in effect, root on that host (see Docker socket
  exposure); the editor marks them high risk but cannot make them safe.

## TOTP and passkey enforcement and recovery

- Read: `auth/signin.go` (TOTP with ±1 step skew and replay protection by
  compare-and-set of the accepted step; recovery codes; step-up),
  `auth/passkey` (go-webauthn, RP ID from `DOCKER_MANAGER_PUBLIC_URL`, user
  verification required, signature counter), the required-factor policy
  with enrollment grace, the owner-recovery CLI.
- `TestEvaluatePolicyMatrix`, `TestRequiredTOTPPolicy`,
  `TestPasskeyPolicyAndWebAuthn`, `TestBothFactorsPolicy`,
  `TestSkewWindowAndReplay`, `TestCounterAndChallengeReplay`,
  `TestUserVerificationRequired`, `TestOwnerRecovery`,
  `TestAPITokensNeverSatisfyFactorPolicy`. The real-browser check
  `auth-factors.spec.ts` (TOTP set-up and sign-in, one-use recovery code,
  virtual-authenticator passkey sign-in) was **(removed)**.
- Limitation: a user without TOTP or passkey steps up with the password
  alone. Passkeys break when the public host name changes (documented).

## Session revocation

- SCS sessions in the manager's own Bun store, `__Host-` cookie
  (HttpOnly, Secure, SameSite=Strict), idle 1 h / absolute 24 h on the
  injectable clock, token renewal at sign-in. Disabling a user, password
  or factor changes, access changes and "sign out everywhere" end sessions
  and close open streams: `TestSessionLifecycle`, `TestRenewalPreventsFixation`,
  `TestDisableAndResetsEndSessionsAndStreams`, `TestLiveStreamRevocation`,
  `TestOwnerJobStreamClosesOnRevocation`.

## API tokens

- `dy_<id>_<secret>`, verifier only at rest (constant-time compare), owned
  by one user, explicit grants intersected with the owner's current
  permissions on every check, session-only routes refuse them, expiry and
  revocation close their streams: `TestAPITokenActsOnlyWithinItsScope`,
  `TestAPITokenNarrowedByGrantChanges`, `TestOwnerAndSessionRoutesRefuseAPITokens`,
  `TestExpiredTokenStreamSwept` (`smoke:api-token` against the published
  images was **(removed)**).

## Secret storage

- Read: `internal/manager/secrets` (XChaCha20-Poly1305 under the
  secret-protection key; key ID, envelope version and a per-field context
  in the associated data, so ciphertexts cannot be moved between fields),
  every `Keyring.Seal` caller, verifier-only storage of all one-time codes
  and tokens (`authsep`), registry/Git/S3 credentials write-only in the API,
  per-operation in-memory credentials on agents (never journaled).
- `TestSealOpenRoundTrip`, `TestOpenFailures`, `TestMissingKeyForExistingInstallFailsClosed`,
  `TestSecretsAtRest` (database dump scanned for canaries),
  `TestCommandSecretsStayInMemory`, `TestCredentialsNeverTouchDisk`, the
  secret-canary sweeps of logs, audit, job output, API responses and the
  support bundle (`TestSupportBundleHasNoSecrets`).
- Limitation: the secret-protection key sits in the data volume by default
  (`secret.key`); anyone with the volume has the key. Mount it from a
  secret store with `DOCKER_MANAGER_SECRET_KEY_FILE` to separate them. The
  Recovery Key opens every backup repository; a leaked key exposes all of
  them until rotated, a lost key makes them unrecoverable.

## TLS enforcement

- The manager never terminates TLS; it refuses a non-https
  `DOCKER_MANAGER_PUBLIC_URL` except for `http://localhost` development, refuses
  setup and passkeys outside the secure origin, and trusts forwarded
  headers only from `DOCKER_MANAGER_TRUSTED_PROXIES`. Agents require https
  (validated certificates, optional private CA, redirects refused); plain
  HTTP only with the explicit co-located opt-in, which the UI flags.
  Registry and Git clients never send credentials to plain-HTTP realms
  without an opt-in.
- `TestPublicURLValidation`, `TestCheckSecureOrigin`, `TestRefusesPlainHTTPWithoutOptIn`,
  `TestHTTPSRejectsUnknownCA`, `TestRedirectsAreRefused`,
  `TestForwardedHeadersOnlyFromTrustedProxies`, `TestNoCredentialsToPlainHTTPRealm`.
- Limitation: the co-located agent's internal plain-HTTP connection relies
  on the Docker network being private to Docker Manager's Compose project.

## Docker socket exposure

- Only the agent mounts the socket; the manager has no Docker access at
  all and the agent listens on nothing (`TestAgentNeverListens`; the
  deploy examples' mounts are checked statically by `test/deploy`, the
  running images by `smoke:fresh-start` **(removed)**). Standalone
  containers created through Docker Manager may
  not bind the socket, a directory containing it or the Docker data root
  (`TestDockerSocketBindsRefused`, agent-side data-root check); Compose
  `use_api_socket`, `provider` and `models` are rejected; Docker Manager's own
  containers, volumes and images are protected for everyone
  (`TestCheckMatrix`; against a real Engine `TestEngineSelfProtection`
  **(removed)**).
- Limitation (by design): a Compose stack may bind any host path,
  including the socket, and run privileged containers, like `docker compose`
  itself. Who may create or edit stacks may therefore act as root on that
  host; F1 makes that visible in the permission editor. The validation
  warns about bind paths outside the project directory.

## Path traversal

- Every agent file operation goes through an `os.Root` on the scope root;
  walks never follow symlinks; multiply-linked files are refused; archives
  are extracted entry by entry with slip, special-file and bomb checks;
  migrations and restores write through the same kind of root.
- `TestTraversalCorpusStaysInsideRoot` (the hostile path corpus of
  `internal/testutil/fscorpus`, generated in memory),
  `TestArchiveSlipAndSpecialEntries`, `TestDecompressionBombs`,
  `TestHardlinkToOutsideIsRefused`, `TestExtractRefusesUnsafeMembers`; the
  symlink escape tree and TOCTOU race tests (`TestEscapeTreeIsRefused`,
  `TestTOCTOUDirectorySwap`) need Linux, so they run in CI's `Unit Tests`
  job, not on a Windows development host. Fuzzing of the path and archive
  code was **(removed)**.

## CSRF

- Unsafe cookie-authenticated requests pass Go's `CrossOriginProtection`
  (Sec-Fetch-Site / Origin against the public origin); the session cookie
  is `SameSite=Strict`; bearer requests drop the cookie and need no CSRF
  check; `/agent/v1` strips cookies entirely.
- `TestCrossOriginRequests`, `TestCrossOriginRequestsRejected`,
  `TestCookiesNeverAuthenticateAgentRoutes`.

## WebSocket authorization

- Browser WebSockets (container exec) accept only the public origin and a
  one-use, 60-second attach ticket carried in the subprotocol (never the
  URL), bound to the session's principal and container; the ticket is
  cleared on first use. Agent WebSockets authenticate the `dya_` credential
  in a bounded pre-auth phase.
- `TestAcceptRejectsPlainHTTPAndForeignOrigins`, `TestExecAuthorizationBoundaries`,
  `TestExecSessionEndToEnd`, `TestAuthorizeExecNeedsExplicitTokenGrant`,
  `TestSessionUpgradeRefusals`, `TestHandshakeRefusals`. The checks through
  each proxy (`TestTLSProxyAgentSessions`: exec WebSocket and agent
  sessions) and against a real Engine (`terminal.spec.ts`) were
  **(removed)** before they ever ran on the current `main`.

## Rate limiting

- Per-IP and per-account token buckets for sign-in, TOTP, recovery codes,
  step-up, invitations, password resets and setup, with a bounded table
  that fails closed; a constant Argon2id cost for unknown accounts;
  `/agent/v1` per client IP (IPv6 per /64), bounded bodies and pre-auth
  deadlines.
- `TestSignInThrottling`, `TestSignInEnumerationResistance`,
  `TestTableBoundFailsClosed`, `TestUnknownAccountCostsOneComputation`,
  `TestAgentRateLimitPerClientIP`, `TestAgentBodyBound`,
  `TestAgentPreAuthTimeout`.
- Limitation: the limiters live in the manager's memory; a restart resets
  them (one manager per instance, so they are not shared or bypassed across
  replicas).

## Audit coverage

- Every non-GET route is audited by construction (`api.Register`), plus
  downloads/exports, job lifecycles, agent events, credential use,
  scheduled work and the owner-recovery CLI; records carry classes, IDs and
  diffs, never secrets; the table is append-only (database triggers) and
  hash-chained.
- `TestEveryCatalogedMutatingRouteIsAudited`, `TestEveryServedMutatingOperationIsAudited`,
  `TestEveryJobKindEmitsLifecycleAuditRecords`, `TestAgentEventsAreAudited`,
  `TestVerifyDetectsTampering`, `TestAppendOnlyAtTheDatabase`,
  `TestSecretCanariesNeverReachTheAuditTrail`.
- Limitation: tamper evidence is a hash chain in the same database;
  someone with write access to the data volume can rewrite the whole
  chain. Ship the redacted mirror (`DOCKER_MANAGER_AUDIT_LOG_MIRROR`) to an
  external log store for records that must outlive a compromised host.

## Dependency vulnerabilities

- Run locally on 2026-09-25: govulncheck v1.8.0 over `./...` and
  `npm audit --omit=dev` in `web/`; licenses checked by
  `scripts/license-check.sh`. Result: finding F4. None of these runs in CI
  any more; repeat them by hand when dependencies change. Pinned Go
  modules, npm lockfiles, base images by digest and workflow actions by
  commit SHA.

## Residual limitations

1. Docker socket access is root: owning the manager (or its data volume
   and key) means owning every enrolled host. Protect the manager host and
   origin accordingly; remove unused agents; rotate agent credentials on
   suspicion.
2. Stack creation and editing, stack file writes, container creation and
   exec are host-root-equivalent grants (F1 labels them; they stay
   powerful by design).
3. First-run window: no setup token; complete setup immediately (the setup
   import routes are public until then).
4. The secret-protection key lives in the data volume unless mounted
   separately; the Recovery Key is all-or-nothing for backups.
5. Audit tamper evidence is local; use the mirror for off-host retention.
6. Rate limits are in memory and reset on restart.
7. Agent sessions through the example proxies, a real Engine behind a
   proxy-connected agent and the published images are not verified by
   automated tests: the proxy sessions test (`TestTLSProxyAgentSessions`,
   #27) and the deploy smoke test were removed on 2026-09-25.
8. Not verified by automated tests since 2026-09-25 (suites removed with
   the move to Forgejo): the Docker-dependent suites (Engine matrix,
   Compose fixtures, MinIO/S3 restic, TLS proxies, smoke against the
   published images), browser flows and accessibility, fuzzing, race
   detection and crash/kill recovery. Their pass status is unknown, not
   assumed (see [support-matrix.md](../support-matrix.md#verification-status)).
9. All browsers (Chromium included), mobile PWA installation and
   accessibility of whole pages are manual checks.
