# Verification matrix (#12 → #29)

Every verification item of the v1 release issue #12 mapped to an automated
test or suite, or to a documented manual procedure with the reason it is
manual. Suites are described in [harness.md](harness.md).

Status is honest: **implemented** means the named test exists and runs in
CI today; **partial** means the harness/fixture or part of the check exists
and the rest is planned; **planned** means the owner issue must add it
(the harness piece it should use is named). Update a row in the PR that
lands its test. Release gate: every automated row implemented and green,
every manual row executed and recorded on #12.

Legend for "where": PR = `ci.yaml` PR suite; X:`job` = `extended.yaml` job.

## Support boundary and artifacts

| # | #12 item | owner | test / suite | where | status |
| --- | --- | --- | --- | --- | --- |
| V01 | Linux amd64/arm64 support | #2, #21 | `static-build` (linux amd64+arm64 static binaries), `images` (multi-arch manifest check); Engine fixtures on both arches (`TestEngine*`); product tests on arm64 via the same matrix | PR, X:`engine-matrix` | partial |
| V02 | Root-only containers (non-root unsupported) | #2, #28 | `TestRefusesNonRoot`, `TestRunRefusesNonRoot` (agent); smoke `fresh-start` asserts UID 0 for manager and agent | PR, X:`smoke` | implemented |
| V03 | Docker Engine/API versions incl. minimum and negotiated downgrade | #21 | `TestEngineServesMatrixVersion` (Engine version, API version, negotiated client version per matrix entry); Moby adapter tests per Engine | X:`engine-matrix` | partial |
| V04 | Supported Compose features | #7, #28 | Compose projects (depends_on healthy/completed_successfully/optional, relative binds, env_file, build) against DinD Engines, `TestCompose*` | X:`compose-fixtures` | planned |
| V05 | Separate manager/agent executables and images | #2 | `images` job (both images, both platforms), `static-build` | PR (push) | implemented |
| V06 | Pinned official Moby client/API and Compose SDK versions | #21 | go.mod pin check + `policy-check.sh` legacy-client rule; adapter integration evidence per Engine | PR, X:`engine-matrix` | partial (policy rule implemented; pins land with #21) |
| V07 | Pinned restic compatibility | #2, #10 | `TestResticPinMatchesDockerfiles` (test pin = image pin), `TestResticPinnedBinary` (0.19.1: local + S3 round trip, `check`) | PR, X:`storage` | implemented |
| V08 | Deployment topology (single origin, reverse proxy) | #27 | Playwright `proxy.spec.ts` + `smoke.spec.ts` through Caddy, Traefik and nginx (`deploy/` configs: HTTP/2 shell + API, spoofed `X-Forwarded-*` ignored, credential separation, SSE and `/agent/v1` WebSocket idle > 60 s unbuffered); `TestTLSProxyAgentTransport`; `test/deploy` example checks; smoke `health-ready` through Caddy | X:`e2e`, X:`smoke`, PR | partial (passkeys/PWA install #16/#23, enrolled agent sessions #3, exec #6) |
| V09 | Browser support | #22, #23 | Playwright Chromium project today; Firefox/WebKit projects added with #22/#23 | X:`e2e` | partial |
| V09m | Mobile PWA install (iOS Safari, Android Chrome) | #23 | **Manual**: install from the share/menu sheet on one iOS and one Android device, launch standalone, go offline, reconnect. Reason: CI has no real mobile browsers or install UI | release checklist | planned |
| V10 | Manager/agent protocol compatibility | #3, #4, #34 | `FuzzDecodeFrame` + frame round-trip tests; version negotiation/compatibility tests with old/new agent builds | PR, X:`fuzz` | partial |
| V11 | Rootless Engines, Docker Desktop, NAS variants | #21, #25 | **Manual** best-effort check per variant once #25 Q2 decides the boundary. Reason: not reproducible on GitHub-hosted runners | release checklist | planned |

## Security

| # | #12 item | owner | test / suite | where | status |
| --- | --- | --- | --- | --- | --- |
| V12 | Auth stack builds statically with Bun/SQLite | #18 | `static-build` (`CGO_ENABLED=0`), `TestDriverIsPureGo`; auth libraries added by #18 must keep both green | PR | partial |
| V13 | Session revocation | #16, #18 | API tests: revoked session rejected on next request and on open streams; canary sweep | PR, X:`e2e` | planned |
| V14 | Factor recovery (TOTP/passkey) | #16 | Playwright with `totp()` and `addVirtualAuthenticator()` (helpers implemented, `helpers.spec.ts`) | X:`e2e` | planned |
| V15 | CSRF | #3, #16 | API tests: state-changing requests without the CSRF token/origin check fail | PR | planned |
| V16 | Throttling / rate limiting | #3, #16, #27 | `/agent/v1` per-client-IP limiter: `TestAgentRateLimitPerClientIP` (fake clock, spoofed XFF, IPv6 /64, fail-closed table); login, enrollment and API rate-limit tests follow with #16/#3 | PR | partial |
| V17 | Owner bootstrap, concurrent first-owner setup | #16 | Race test: N concurrent setup requests → exactly one owner; smoke `owner-setup` step (pending) | PR, X:`race`, X:`smoke` | planned |
| V18 | Invite redemption (one-use) and revocation | #16 | API tests incl. concurrent redemption | PR, X:`race` | planned |
| V19 | Restricted default group; no-access new user | #17 | Permission decision corpus | PR | planned |
| V20 | Scoped capabilities, group/user override precedence and boundaries | #17 | Decision corpus (table-driven, every capability × scope × override) | PR | planned |
| V21 | Metrics-only and restart-only grants | #17 | Decision corpus + API tests | PR | planned |
| V22 | TOTP/passkey enforcement and recovery | #16 | Playwright (helpers implemented) | X:`e2e` | planned |
| V23 | Secret storage | #2, #10, #19 | `TestSealOpenRoundTrip`, `TestMissingKeyForExistingInstallFailsClosed`; canary sweeps over DB dumps per secret kind | PR, X:`secret-canary` | partial |
| V24 | Secrets never in logs, audit, job output, API responses | #16, #19, #10, #30, #31 | `internal/testutil/canary` (implemented, unit-tested); `TestJSONOutputAndSecretRedaction`, `TestAccessLogOmitsQuery`; audit: `TestSecretCanariesNeverReachTheAuditTrail`, `TestSecretCanariesNeverStored`, `FuzzCanonicalDetails`; per-feature canary tests | PR, X:`secret-canary` | partial |
| V25 | TLS enforcement | #2, #3, #27 | `TestPublicURLValidation`, `TestRefusesPlainHTTPWithoutOptIn`, `TestRunRefusesPlainHTTPManager`; TLS proxy fixtures | PR, X:`e2e` | implemented |
| V26 | Docker socket exposure | #2, #28, #32 | `TestAgentNeverListens`; smoke `fresh-start`: socket mounted only in the agent | PR, X:`smoke` | implemented |
| V27 | Path traversal | #15 | `test/corpora/fs` + `fscorpus` (implemented: generator, `os.Root` reference tests, TOCTOU race); file manager consumers | PR, X:`fs-security` | partial |
| V28 | WebSocket authorization | #3, #23 | Agent session and UI stream tests: unauthenticated/revoked/foreign-scope connections refused | PR, X:`e2e` | planned |
| V29 | Audit coverage and tamper evidence | #30 | `TestEveryCatalogedMutatingRouteIsAudited` (every non-GET inventory route), `TestEveryServedMutatingOperationIsAudited`, `TestOpenAPICompleteness` (`x-dockyard-audit`), `TestOperationsRegisteredOnlyThroughRegister`, `TestEveryJobKindEmitsLifecycleAuditRecords`; `TestVerifyDetectsTampering`, `TestRetentionPurgeKeepsChainVerifiable`, `TestSizeCapPurge`, `TestAppendOnlyAtTheDatabase` | PR | implemented |
| V30 | Dependency vulnerabilities | #2 | `vuln` (govulncheck, `npm audit --omit=dev`), `licenses` | PR | implemented |
| V31 | API token scoping and expiry | #31 | Token × capability × scope tests; expired/revoked token tests (fake clock) | PR | planned |
| V32 | Self-protection of DockYard's own containers/images/volumes | #32 | Engine tests: stop/remove/prune of own resources refused | X:`engine-matrix` | planned |

## PWA and live synchronization (#23)

| # | #12 item | owner | test / suite | where | status |
| --- | --- | --- | --- | --- | --- |
| V33 | Responsive installable PWA | #11, #22, #23 | `pwa.spec.ts` "manifest is valid…", "service worker registers at the root scope under the TLS proxy", "deep links reload into the app shell, online and offline", "offline shell shows when the network is cut"; `helpers.spec.ts` "web app manifest and service worker"; `verify-build.mjs` (manifest, icon sizes); `TestPWAAssets`; update prompt: `register.spec.ts`. Responsive layouts and viewport projects: #22 | X:`e2e`, PR | partial |
| V34 | Safe service-worker caching | #11, #23 | `sw-core.spec.ts` (API/agent/non-GET never handled or stored, precache only), `verify-build.mjs` (precache list), `pwa.spec.ts` "API responses are never served from or stored in Cache Storage", `helpers.spec.ts` "service worker never caches API data", `TestAPIResponsesAreNoStore`, `TestPWABuildOnlyPathsNeverFallBackToHTML`. Stream endpoints (#23) inherit the `/api` rule | PR, X:`e2e` | partial |
| V35 | Live authorized updates across all open views | #23 | Playwright multi-page test with `collectSse`/`wsRoundTrip` (helpers implemented through the proxy) | X:`e2e` | planned |
| V36 | Reconnect and gap recovery | #23 | Playwright + fault injection (drop stream, resume from last event ID) | X:`e2e`, X:`fault-injection` | planned |
| V37 | File changes made outside the UI | #15, #23 | Simulated fsnotify events incl. missed events → rescan | X:`fs-security` | planned |
| V38 | Unsaved editor conflict handling | #15, #23 | Playwright: concurrent edit → conflict dialog, no silent overwrite | X:`e2e` | planned |
| V39 | Permission revocation while views are open | #17, #23 | Playwright: revoke grant → view and stream close | X:`e2e` | planned |

## Backups, jobs and recovery

| # | #12 item | owner | test / suite | where | status |
| --- | --- | --- | --- | --- | --- |
| V40 | Clean-manager multi-repository backup import | #24 | MinIO fixture (implemented) + two local repos; import into a fresh manager | X:`storage` | planned |
| V41 | Portable manifest and secret-key recovery (Recovery Key) | #24 | Recovery with the key; failure without it; corrupt/truncated manifest fixtures | X:`storage` | planned |
| V42 | Shared backup/job ownership; authorized cross-user job visibility | #10, #26, #17 | API tests over the job engine | PR | planned |
| V43 | Scheduled runs after creator removal | #13 | Fake-clock scheduler test | PR | planned |
| V44 | Queued manual job rejected after grant revocation | #26, #17 | Job engine test | PR | planned |
| V45 | In-flight recovery after requester logout | #26 | Job engine test | PR | planned |
| V46 | Local and S3 restic backup | #10 | `TestResticPinnedBinary` (fixture round trip, implemented); agent backup jobs against local + MinIO | X:`storage` | partial |
| V47 | Selective file/stack/volume restore | #10 | Restore tests on a DinD Engine | X:`storage` | planned |
| V48 | Retention | #10 | Fake-clock retention tests + restic `forget` against MinIO | PR, X:`storage` | planned |
| V49 | Backup/restore of a relative bind-mounted directory beside compose.yaml | #10, #28 | `test/smoke/sample-stack` (has `./html`), restore on a DinD Engine | X:`storage` | planned |
| V50 | Optional pre-backup shutdown with dependency-aware resume (success/failure/cancel) | #10 | Engine test with a depends_on stack | X:`storage` | planned |
| V51 | Long-running job recovery; job engine crash recovery and locking | #26 | Lock-matrix completeness test; kill manager/agent mid-job (`-tags faultinject`, `test/fault`) | PR, X:`fault-injection` | planned (#26 in progress) |

## Registry, updates and builds

| # | #12 item | owner | test / suite | where | status |
| --- | --- | --- | --- | --- | --- |
| V52 | Private-registry pulls on two agents | #19 | `StartRegistry` + `StartEngines` (implemented; `TestRegistryFixtureAuthAndFaults` pulls with auth through the proxy); agent-side tests | X:`compose-fixtures` | partial |
| V52m | Authenticated Docker Hub pulls | #19 | Automated only when Docker Hub test credentials are configured as secrets; otherwise **manual**: pull a private Docker Hub image on two agents. Reason: needs a real Docker Hub account and is subject to its rate limits | X:`compose-fixtures` / release checklist | planned |
| V53 | Registry 401 and 429 handling | #19 | `FaultProxy` (implemented, `TestFaultProxy*`, Engine pull under injected 429 in `TestRegistryFixtureAuthAndFaults`); agent retry/backoff honouring `Retry-After` | PR, X:`compose-fixtures` | partial |
| V54 | Credential secrecy and rotation; unauthorized credential access | #19, #17 | Canary sweep + API tests | PR, X:`secret-canary` | planned |
| V55 | Fixed-tag host-platform digest change triggers an update | #20 | Push a new digest to the registry fixture (`PushOCIImage`), run the updater | X:`compose-fixtures` | planned |
| V56 | Unchanged digest is a no-op | #20 | Same fixture | X:`compose-fixtures` | planned |
| V57 | Digest-pinned / build-only services shown ineligible | #20 | Unit + Engine tests | PR | planned |
| V58 | Failed updates reported and quarantined | #20 | `FaultProxy` injected failures during update | X:`compose-fixtures` | planned |
| V59 | Compose/override/env files byte-for-byte identical through successful and failed updates | #20 | SHA-256 of files before/after in the update tests | X:`compose-fixtures` | planned |
| V60 | Dependency-aware automatic updates | #20, #9 | Engine test with depends_on | X:`compose-fixtures` | planned |
| V61 | Git and Compose builds | #33 | `StartGitServer` (implemented; `TestGitServerFixture` proves Engine-side reachability incl. private repo auth); build tests | X:`compose-fixtures` | partial |

## Operations

| # | #12 item | owner | test / suite | where | status |
| --- | --- | --- | --- | --- | --- |
| V62 | Fresh install | #2 | Smoke `wait-images` → `fresh-start` → `health-ready` | X:`smoke` | implemented |
| V63 | Upgrade | #34 | Smoke variant: start the previous edge, upgrade to the new one, data intact | X:`smoke` | planned |
| V64 | Two-host operation with live state convergence | #3, #5 | `StartEngines(t, 2, …)` (implemented, `TestEngineMultiHost`) + two agents | X:`engine-matrix` | partial |
| V65 | Agent offline/reconnect | #3 | Fault injection: drop the agent connection, reconnect, state converges | X:`fault-injection` | planned |
| V66 | Duplicate-agent rejection/replacement for one Engine | #3 | Two agents on one DinD Engine | X:`engine-matrix` | planned |
| V67 | Stack revision restore | #7 | Engine test | X:`compose-fixtures` | planned |
| V68 | Editable cron defaults, DST and restart behaviour | #13 | Fake-clock scheduler tests (DST zones, missed runs, restart) | PR | planned |
| V69 | Every prune category; disabled-by-default automation; durable background runs; maintenance preview | #14 | Engine tests per category + preview equals effect | X:`engine-matrix` | planned |
| V70 | Scoped file browser/editor: selection, drag-drop, archives, permissions, conflicts, root containment | #15 | `fscorpus` consumers + Playwright | X:`fs-security`, X:`e2e` | planned |
| V71 | Fresh-manager import with Recovery Key | #24 | See V40/V41 | X:`storage` | planned |
| V72 | Single-origin reverse-proxy deployment and agent connectivity | #27 | Playwright `proxy.spec.ts` per proxy; `TestTLSProxyAgentTransport` (remote agent via the TLS origin with a private CA, co-located agent on the internal URL with opt-in); `internal/agent/transport` tests; enrolled sessions through each proxy with #3 | X:`e2e`, PR | partial |
| V73 | Identical-path storage layout | #28 | Agent startup check tests on DinD Engines; smoke stack with relative bind | X:`engine-matrix`, X:`smoke` | planned |
| V74 | Stack and volume migration between environments | #35 | Two Engines (`StartEngines`) | X:`engine-matrix` | planned |
| V75 | Version compatibility and environment removal | #34 | Old/new agent builds against the manager; removal leaves Engine resources intact | X:`engine-matrix` | planned |
| V76 | First agent enrolled from the PWA UI; stack deployed | #3, #7 | Smoke `enroll-agent`, `deploy-stack` (pending steps, enabled by #3/#7); Playwright enrollment flow | X:`smoke`, X:`e2e` | planned |

## Contract and documentation

| # | #12 item | owner | test / suite | where | status |
| --- | --- | --- | --- | --- | --- |
| V77 | Public JSON routes match Huma OpenAPI | #4 | `TestOpenAPISnapshot`, `TestServedSpecMatchesSnapshot`, `TestOpenAPICompleteness`, `generate.sh --check` | PR | implemented |
| V78 | Streaming and agent protocols documented | #4 | **Manual** review of the protocol docs against `internal/protocol` at release; `FuzzDecodeFrame` covers the codec. Reason: documentation completeness needs human judgment | release checklist, X:`fuzz` | partial |
| V79 | User/admin docs (deployment, PWA install/update/offline, first-agent enrollment, multi-host, upgrades/migrations, backup/restore, policy safety, troubleshooting) | #12 | **Manual** review checklist per topic on #12. Reason: content quality cannot be asserted by tests | release checklist | planned |
| V80 | Custom UI derived from the mockup, official Lucide Svelte icons | #22 | **Manual** visual review against the mockup on #22 (the mockup is never committed, so no pixel diff in CI); dependency check for `@lucide/svelte` can be automated with #22 | release checklist | planned |
| V81 | Deployment examples, API docs, backup recovery instructions, migration notes available | #27, #4, #24, #34 | **Manual** checklist. Reason: presence and accuracy of docs | release checklist | planned |
| V82 | Security and recovery checks recorded with remaining limitations | #12 | This matrix plus the manual procedure log on #12 | release checklist | partial |
| V83 | Digest update, registry auth/rate-limit and Compose source-integrity evidence linked from #19 and #20 | #19, #20 | V52–V59 test names and CI run URLs linked on the issues | release checklist | planned |
| V84 | All accepted v1 issues have evidence; release only after #1 exit criteria | #1, #12 | **Manual** release gate. Reason: process decision | release checklist | planned |
