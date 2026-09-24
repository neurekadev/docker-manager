# Test harness

How DockYard's tests are organised and how to run each suite (#29). The
release verification map is in [verification-matrix.md](verification-matrix.md).

## Tiers

| tier | where | when | budget |
| --- | --- | --- | --- |
| PR suite | `.github/workflows/ci.yaml` (`bash scripts/check.sh` locally) | every PR and push to `main` | ~15 min wall clock |
| Extended suites | `.github/workflows/extended.yaml` | every push to `main` (amd64), nightly 03:17 UTC (full matrix, amd64 + arm64), on demand | per job 20–60 min |
| Release gate | [verification-matrix.md](verification-matrix.md) | before a v1 release (#12) | automated rows green + manual procedures recorded |

The PR suite is Docker-free: `go test ./...`, lint, vet (also with the
`integration,e2e,faultinject` tags so tagged code keeps compiling), policy,
generated artifacts, web checks, static builds, vulnerability and license
checks. Everything that needs a Docker Engine or a browser runs in the
extended workflow.

## Build tags and naming

| tag | contents | runs in |
| --- | --- | --- |
| none | unit tests, corpus generators, harness helpers that need no Docker | PR suite, `race` |
| `integration` | testcontainers-go fixtures and Engine/Compose/storage tests | `engine-matrix`, `compose-fixtures`, `storage`, `e2e` (TLS proxy) |
| `e2e` | test-only manager routes for the proxy E2E stack (`internal/manager/server/e2e_routes.go`, `TestE2ERoutes`) | `e2e` |
| `faultinject` | fault-injection hooks and tests (`internal/faultinject`, #26) | `fault-injection` |

Integration tests are selected by name, so name new ones accordingly:

| job | `-run` filter |
| --- | --- |
| `engine-matrix` | `^TestEngine` (runs once per Engine × architecture) |
| `compose-fixtures` | `^Test(Registry\|Git\|Compose)` |
| `storage` | `^Test(MinIO\|Restic\|Storage\|Backup\|Restore)` |
| `e2e` | `^TestTLSProxy` (all packages), `^TestE2E` with `-tags e2e`, plus Playwright |
| `fault-injection` | all tests with `-tags faultinject`; subprocess kill suites `^Test(Kill\|Crash)` in `test/fault` |
| `fs-security` | `internal/testutil/fscorpus` plus every package importing it |
| `secret-canary` | `internal/testutil/canary` plus every package importing it |

## Running the suites

Anything under the `integration` tag needs a Docker Engine (Linux, or Docker
Desktop); DinD Engines are privileged containers. There is no Docker on the
Windows development hosts, so these run in CI:

```bash
gh workflow run extended.yaml --ref <branch>                          # all suites
gh workflow run extended.yaml --ref <branch> -f suites=fuzz,e2e       # a subset (comma-separated, no spaces)
gh workflow run extended.yaml --ref <branch> -f suites=fuzz -f fuzztime=5m
gh workflow run extended.yaml --ref <branch> -f suites=engine-matrix -f arm64=false
gh workflow run extended.yaml --ref <branch> -f suites=smoke -f smoke-images=local
gh workflow run extended.yaml --ref <branch> -f suites=smoke -f smoke-images=edge -f smoke-revision=<main sha>
```

On a machine with Docker:

| suite | command |
| --- | --- |
| race | `CGO_ENABLED=1 go test -race -count=1 ./...` |
| fuzz | `FUZZTIME=30s bash scripts/ci/fuzz-all.sh` (discovers every `FuzzXxx`) |
| engine-matrix | `DOCKYARD_TEST_ENGINE=24.0.9 go test -tags integration -run '^TestEngine' ./...` |
| compose-fixtures | `go test -tags integration -run '^Test(Registry\|Git\|Compose)' ./...` |
| storage | `go test -tags integration -run '^Test(MinIO\|Restic\|Storage\|Backup\|Restore)' ./...` |
| fs-security | `go test -race ./internal/testutil/fscorpus/...` |
| fault-injection | `go test -tags faultinject ./...` |
| secret-canary | `go test ./internal/testutil/canary/...` (+ consumers) |
| e2e | see [Browser E2E](#browser-e2e) |
| smoke | `SMOKE_IMAGES=local bash scripts/smoke/deploy-smoke.sh` after building `dockyard-manager:smoke` / `dockyard-agent:smoke` with `REVISION=$(git rev-parse HEAD)` |

Suites whose product features do not exist yet run their fixture
self-tests and every real test that exists, and print a `::notice::` with
the pending checks (also in the job summary). A pending check is never
reported as passed.

## Environment variables

| variable | used by | meaning |
| --- | --- | --- |
| `DOCKYARD_TEST_ENGINE` | `testharness.SelectEngine` | Engine version from `test/matrix/engines.json`, or `default` / `minimum` / `latest` (default: the matrix default) |
| `DOCKYARD_TEST_CACHE` | `testharness.FetchRestic` | download cache (default: user cache dir `dockyard-test`) |
| `DOCKYARD_TEST_AGENT_IMAGE` | `testharness.AgentImage` | locally built agent image (`deploy/docker/agent.Dockerfile`) that `TestEngineAgentImage` runs inside DinD; the engine-matrix job builds `dockyard-agent:test`. Unset: skipped locally, fails under `CI` |
| `FUZZTIME` | `scripts/ci/fuzz-all.sh` | `-fuzztime` per target (default `60s`) |
| `E2E_BASE_URL` | Playwright | test one origin (project `custom`) instead of the three proxy origins |
| `E2E_REVISION` | `e2e/compose.yaml` | build revision |
| `E2E_IGNORE_HTTPS_ERRORS=1` | Playwright | local runs without trusting the E2E CA (WebAuthn then fails) |
| `SMOKE_*` | `scripts/smoke/deploy-smoke.sh` | see the script header |
| `DOCKYARD_ARM64_RUNNERS` (repository variable) | `extended.yaml` | `false` = arm64 runners unavailable; the Engine matrix falls back to amd64 with a notice |

## Engine matrix

`test/matrix/engines.json` is the single list of Engine versions, read by
`testharness.LoadMatrix` and by the workflow's `plan` job. Each entry pins
the official `docker:<version>-dind` image by multi-arch digest and records
the Engine's API version, which `TestEngineServesMatrixVersion` verifies.
`TestLoadMatrixIsValid` / `TestWorkflowReadsMatrixFile` keep the file valid
and the workflow free of hard-coded versions.

Current entries: 24.0.9 (API 1.43, the mockup's Docker 24) and 25.0.5
(API 1.44) as minimum candidates, 28.5.2 and 29.8.1 as the latest two
majors. The supported minimum is decided by #21 and recorded in #25 (Q2);
drop the losing candidate from the matrix then.

Runners: `ubuntu-24.04` and `ubuntu-24.04-arm` (available to this private
repository, verified 2026-09-24). Pushes to `main` run amd64 only; the
nightly schedule and manual runs cover both architectures. If arm64 runners
become unavailable, set the repository variable `DOCKYARD_ARM64_RUNNERS` to
`false`: the matrix falls back to amd64 and the `plan` job writes a notice
(a job for an unavailable label would queue instead of failing).

## Fixtures (`internal/testharness`)

Docker-free helpers (unit-tested in the PR suite): `LoadMatrix`,
`SelectEngine`, `FaultProxy`, `HtpasswdLine`, `RegistryAuth`, `SignV4` /
`S3Client`, `NewTestImage` / `PushOCIImage`, `SeedGitFixtures`,
`FetchRestic`.

Container fixtures (`//go:build integration`, testcontainers-go v0.44.0;
each has a self-test in `fixtures_integration_test.go`):

| fixture | returns | notes |
| --- | --- | --- |
| `NewNetwork(t)` | user-defined network | fixtures and Engines resolve each other by alias; DinD dockerd does too |
| `StartEngine(t, EngineOptions{})` | `Engine{Host, InternalHost, Alias}` | privileged `docker:<ver>-dind`, plain TCP 2375; `Host` is a `DOCKER_HOST` for the Moby client; `Client(t)` negotiates the API version |
| `StartEngines(t, 2, opts)` | two Engines on one network | multi-host tests (`engine-1`, `engine-2`) |
| `StartRegistry(t, opts)` | `Registry{Direct, ProxyURL, EngineAddress, Proxy}` | `registry:3.1.1` with htpasswd auth behind a `FaultProxy` (401/403/429 + `Retry-After`, per path, `Times`); start Engines with `reg.EngineOptions()` to pull through the proxy |
| `StartGitServer(t, opts)` | `GitServer{URL, InternalURL, IP}` | dumb-HTTP Git over Caddy; repos seeded from `test/fixtures/git/<name>`; `private*` need basic auth; reachable from inside DinD by alias or IP |
| `StartMinIO(t, opts)` | `MinIO{Endpoint, InternalEndpoint, AccessKey, SecretKey, S3}` | random root credentials, buckets created; `ResticRepository`, `ResticEnv` |
| `FetchRestic(ctx)` | path | restic 0.19.1, SHA-256 from `deploy/docker/*.Dockerfile` (checked by `TestResticPinMatchesDockerfiles`) |
| `StartTLSProxy(t, opts)` | `TLSProxy{URL, RootCAPEM, Client}` | Caddy `tls internal` for `localhost`, unbuffered SSE/WebSocket; `Client` verifies against Caddy's root |
| `e.LoadWorkload(t)` | — | loads `WorkloadImage` (`dockyard-test/workload:1`): `test/fixtures/workload` built statically for the runner's architecture, packed by `WorkloadArchive` as a `docker save` archive. Every container of the Engine/Compose tests runs it (serve, health checks, one-shots, exec, TTY, logs, listeners), so no test pulls from Docker Hub inside DinD |
| `e.LoadHostImage(t, ref)` | — | copies an image from the runner's Docker into the DinD Engine (the agent image under test) |
| `e.StartAgent(t, AgentOptions{})` | container ID | runs the agent image inside DinD with the deploy mounts (socket + `/var/lib/docker/volumes` at the identical path); `WaitLog`, `WaitHealthy`, `Logs` |
| `e.Listeners(t, id)` | listening sockets | runs the workload in the container's network namespace and reads `/proc/net/{tcp,tcp6,udp,udp6,unix}` |
| `e.StartWorkload(t, args)` / `e.RunWorkload(t, args, hc)` | ID / output | long-running / one-shot workload containers |

Test-process servers (the fault proxy, httptest upstreams) are exposed to
containers as `host.testcontainers.internal:<port>` via `HostAccessPorts`.
MinIO note: upstream stopped publishing `minio/minio` images in October 2025;
the fixture pins the `alpine/minio` rebuild of the last release by digest.

## Secret canaries (`internal/testutil/canary`)

```go
c := canary.New()
pw := c.New(canary.Password, "owner password")   // also APIToken, RegistryCredential,
                                                   // S3AccessKey, S3SecretKey, EnvValue, TOTPSeed
logger := c.CaptureLogger(t)                       // checked at test end
h := c.Handler(t, srv.Handler)                     // every response checked
out := c.Writer(t, "job output")                   // checked at test end
c.AssertClean(t, "audit rows", rows)               // any value (JSON + %+v)
c.Register(canary.APIToken, "issued token", tok)   // secrets the code generated
```

Detection covers raw values, JSON/HTML escaping, URL escaping, hex and
base64/base64url at every alignment (e.g. inside a basic-auth header or a
Docker config `auth` blob).

## Filesystem corpora (`internal/testutil/fscorpus`)

See [test/corpora/README.md](../../test/corpora/README.md): traversal
strings, zip-slip/tar-slip archives, decompression bombs, symlink/hardlink
escape trees and the TOCTOU `Swapper` / `RaceWhile`.

## Browser E2E

`e2e/` is a separate npm package (pinned `@playwright/test`, own lockfile).
`e2e/compose.yaml` builds the manager from source with the `e2e` build tag
(`GO_TAGS=e2e`, adding the test-only routes of
`internal/manager/server/e2e_routes.go`: `/api/v1/__e2e/request-info`,
`/api/v1/__e2e/sse`, `/agent/v1/__e2e/request-info`,
`/agent/v1/__e2e/echo`) and runs one manager behind each example proxy from
`deploy/`, using the proxies' configuration files unchanged:

| project | origin | proxy | direct (untrusted) manager port |
| --- | --- | --- | --- |
| `caddy` | `https://localhost:8443` | `deploy/caddy/Caddyfile` | `http://127.0.0.1:18080` |
| `traefik` | `https://localhost:8444` | `deploy/traefik/dynamic` + the example's arguments | `http://127.0.0.1:18081` |
| `nginx` | `https://localhost:8445` | `deploy/nginx/templates` | `http://127.0.0.1:18082` |

All three serve a `localhost` certificate from one throw-away CA
(`test/e2e/certgen`, service `certs`); every Playwright spec runs once per
project. `TestE2EComposeMatchesDeploy` (`test/deploy`) keeps the proxy
images, arguments and mounts identical to `deploy/`.

```bash
docker compose -f e2e/compose.yaml up -d --build --wait
docker compose -f e2e/compose.yaml cp certs:/certs/ca.crt e2e/.e2e-ca.crt
# trust it for Chromium (Linux): certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n dockyard-e2e -i e2e/.e2e-ca.crt
cd e2e && npm ci && npx playwright install chromium
NODE_EXTRA_CA_CERTS=.e2e-ca.crt npx playwright test               # all proxies
NODE_EXTRA_CA_CERTS=.e2e-ca.crt npx playwright test --project nginx
```

The proxy topology specs (`e2e/tests/proxy.spec.ts`, #27) check per proxy:
PWA shell and API over HTTP/2 on one origin; spoofed `X-Forwarded-*`
ignored (through the proxy and directly from an untrusted peer); agent
credentials refused on `/api/v1` and cookies stripped on `/agent/v1`; an
SSE stream and a WebSocket under `/agent/v1` kept open through 70 s of
idleness (longer than the 60 s proxy read timeouts) by heartbeats and
pings, with arrival times proving the proxy does not buffer.
`TestTLSProxyAgentTransport` (`test/proxy`, Go, `integration`) connects the
agent transport through a real TLS proxy with a private CA.

Helpers (`e2e/helpers`): `addVirtualAuthenticator` (Chromium CDP
`WebAuthn.enable` / `addVirtualAuthenticator`), `totp` (RFC 6238, verified
against the RFC vectors), `collectSse` / `wsRoundTrip` (in-page, through
the proxy), `sseTimeline` (raw SSE lines with arrival times, including
heartbeat comments), `wsIdleRoundTrip` (WebSocket round trip across an
idle period), `checkManifest` / `checkServiceWorker` / `cachedUrls` /
`cacheContents` / `waitForServiceWorkerControl` (PWA). `tests/pwa.spec.ts`
covers the PWA shell (#11): manifest, service-worker scope under the proxy,
deep-link reloads, API responses absent from Cache Storage, the offline
shell (`context.setOffline`, which Playwright also applies to service
workers) and lazily loaded libraries. The CI job uploads the HTML report as
the `playwright-report` artifact.

## Deploy smoke test

`scripts/smoke/deploy-smoke.sh` (job `smoke`) runs `deploy/caddy` with
the images under test: on `main` it waits (up to 30 min) until
`ghcr.io/neurekadev/dockyard-{manager,agent}:edge` carry
`org.opencontainers.image.revision == github.sha`; on branches it builds
the images locally (`smoke-images` input; `smoke-revision` tests the
current edge against a given commit). If `:edge` has already moved on to a
newer `main` commit that contains the run's commit, that newer image is
tested instead of waiting (GitHub compare API). Steps: fresh start (healthy,
UID 0, Docker socket only on the agent) → readiness, UI shell and OpenAPI
through the Caddy TLS proxy with a verified CA → owner setup (pending #16)
→ agent enrollment (pending #3) → deploy `test/smoke/sample-stack` (pending
#7). Pending steps print `::warning::` and are listed in the job summary;
the warning changes once matching operations appear in the served OpenAPI.

## Time and clocks

Scheduler, timeout and expiry tests use `internal/clock` (`clock.NewFake`,
`testutil.FakeClock()`), never wall-clock sleeps in assertions. Fixture
setup may poll for readiness with a deadline; that is setup, not an
assertion.
