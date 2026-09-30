# ADR 0001: Foundation of the Docker Manager codebase

- Status: accepted
- Date: 2026-09-24
- Issues: #2 (foundation), #4 (API conventions), #11 (web foundation), #29 (test harness); decisions in #25 are binding

## Context

Docker Manager v1 is built by several agents in parallel. The first change fixes
the layout, conventions and gates every later workstream builds on.

## Decisions

1. **One Go module, two static executables.** `github.com/neurekadev/docker-manager`
   with `cmd/docker-manager` and `cmd/docker-agent`, built with
   `CGO_ENABLED=0 -trimpath` for linux/amd64 and linux/arm64. Go 1.27.1 is
   pinned via the `toolchain` directive; `go.mod` `ignore`s `web/node_modules`
   (some npm packages ship stray `.go` files).
2. **SQLite through Bun + `sqliteshim`.** sqliteshim selects
   `modernc.org/sqlite` (pure Go) on linux/amd64, linux/arm64 and
   windows/amd64 unless the `cgosqlite` tag is set; a unit test asserts the
   driver and `build-static.sh` rejects binaries linking `mattn/go-sqlite3`.
   WAL, `busy_timeout=5000`, `foreign_keys=on`, `synchronous=NORMAL`,
   `_txlock=immediate`, and a **single-connection writer pool** (serializes
   writes in-process instead of fighting over `SQLITE_BUSY`; a read-only pool
   can be added later). Tables are `STRICT`.
3. **Migrations:** Go-registered Bun migrations, one timestamp-named file each
   (`YYYYMMDDHHMMSS_name.go`), each wrapped in a transaction (`migrations.Tx`)
   and only recorded on success (`WithMarkAppliedOnSuccess`). Pending
   migrations on a non-empty database trigger a `VACUUM INTO` snapshot first
   (newest 3 kept). A fresh, empty database is not snapshotted. Migration
   failure aborts startup before any listener. A database with migrations
   unknown to the binary (downgrade) is refused. Bun's migration lock table is
   not used: a crashed process would leave it locked, and v1 has exactly one
   manager process per data volume.
4. **Secret-protection key:** 32 random bytes (base64 file, 0600), generated
   only for a fresh installation. If the database already has an instance
   record and the key is missing, startup fails instead of silently creating
   a new key. Values are sealed with XChaCha20-Poly1305 into versioned
   envelopes `dy1.<keyID>.<base64url(nonce‖ciphertext)>`; the key ID and a
   caller context string are bound as associated data; a keyring keeps
   retired keys for rotation (#24).
5. **Public API conventions (#4):** Huma v2 on `net/http` `ServeMux`
   (humago). Absolute paths (`/api/v1/...`) in the spec so the generated
   client uses the same paths as the docs. Operation IDs are kebab-case.
   `api.Register` is mandatory and panics without `OperationID`, `Summary`,
   capability (`x-docker-manager-capability`) and scope (`x-docker-manager-scope`).
   Huma's error model is replaced by one Docker Manager shape
   `{code,message,details,requestId,retryable}` served as
   `application/problem+json` (valid RFC 9457 with extension members only).
   Arrays are documented as non-nullable. No interactive docs UI and no
   `$schema` links (they would break the strict CSP and the exact shape).
6. **HTTP hardening:** request IDs (well-formed inbound `X-Request-ID` kept
   until #27 restricts it to trusted proxies), access log without query
   strings, panic recovery into the error shape, `no-store` on API/agent
   routes, `nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`
   and a CSP with `frame-ancestors 'none'` whose `script-src` allows only
   `'self'` plus SHA-256 hashes of the inline bootstrap script, computed from
   the embedded `index.html` at startup (no `'unsafe-inline'` scripts).
7. **Web embedding:** SvelteKit adapter-static writes to `web/build/app`; a
   committed `web/build/fallback` placeholder guarantees `//go:embed all:build`
   compiles on a clean checkout. No build tags, so no binary can accidentally
   be built "without UI" when the build output exists.
8. **Web dependencies:** Svelte 5, SvelteKit 2, TypeScript 5.9 (openapi-typescript
   7 does not yet accept TypeScript 6), openapi-typescript + openapi-fetch for
   the typed client. Code that ships in the bundle (`svelte`, `@sveltejs/kit`,
   `openapi-fetch`) is in `dependencies` so `npm audit --omit=dev` and the
   license check cover it; tooling is in `devDependencies`.
9. **Agent:** refuses non-root (`Geteuid` injectable for tests), refuses
   `http://` manager URLs without `DOCKER_AGENT_MANAGER_ALLOW_HTTP=true`, opens no
   listener (static AST test over every in-module package the agent links),
   reports liveness via an atomically replaced `health.json` checked by
   `docker-agent healthcheck` (max age 60 s).
10. **Images:** multi-stage, all builder stages on `$BUILDPLATFORM`, final
    stage `gcr.io/distroless/static-debian12` (root variant) with no `RUN`,
    so arm64 needs no QEMU (images were linux/amd64 only from 2026-09-25 to
    2026-09-30, see "Later changes"). restic 0.19.1 is downloaded per `TARGETARCH` and
    verified against SHA-256 values from the release's GPG-signed
    `SHA256SUMS` (key `CF8F18F2844575973F79D4E191A6868BD3F7A907`). No
    `VOLUME` instruction: persistence is explicit via named volumes. All
    base/builder images are pinned by digest.
11. **CI:** `ci.yaml` (PR + main) runs the same scripts as the local gate;
    third-party actions pinned by commit SHA. Only pushes to `main` publish,
    and only the `edge` tag, with BuildKit provenance (`mode=max`) and SBOM.
    `actions/attest-build-provenance` runs with `continue-on-error` because
    GitHub artifact attestations are unavailable for private user-owned
    repositories on the current plan; its outcome is shown in the job summary.
    `extended.yaml` hosts slow suites as separate jobs gated by a `suites`
    input. (Superseded on 2026-09-25, see "Later changes".)
12. **License policy:** shipped Go modules and production npm packages must
    use permissive licenses or MPL-2.0 (allowlist in
    `scripts/license-check.sh`); anything else, including unknown licenses,
    fails CI until reviewed. The project itself has no license file (#25).
    (Since 2026-09-25 the license check is a manual review, not a CI job.)
13. **Injectable time:** production code takes `clock.Clock`; tests use
    `clock.Fake` and never sleep to wait for behavior.
14. **IDs:** Docker Manager-owned records use UUIDv7 strings (`ids.New()`).

## Consequences

Later workstreams add operations via `api.Register`, migrations as new
timestamped files, and extend `scripts/policy-check.sh` and `extended.yaml`
rather than inventing parallel mechanisms. Changing any decision above needs
a new ADR.

## Later changes

- **2026-09-25, CI and tests (supersedes decision 11 and the
  `extended.yaml` part of the consequences).** The code moved from GitHub
  to Forgejo on code.neureka.dev (and back to GitHub,
  `https://github.com/neurekadev/docker-manager`, on 2026-09-30); the
  GitHub issues stay the written record. The owner reduced the automated checks to
  format/lint, isolated deterministic unit tests and a test-free build:
  `.github/workflows/CI.yaml` (GitHub Actions) runs on pushes to `main`
  and manual dispatch only, mirrors `bash scripts/check.sh`
  (`lint`, `unit-tests`, `build`), builds linux/amd64 images (linux/arm64
  too since 2026-09-30, on the native `ubuntu-24.04-arm` runner) and publishes
  `ghcr.io/neurekadev/docker-{manager,agent}:edge` from `main` with
  BuildKit provenance and SBOM attestations. `ci.yaml`, `extended.yaml`,
  `api-contract.yaml`, the Docker-backed, browser, fuzz, race and crash
  suites and the test harness (#29) were removed; the license check,
  govulncheck and `npm audit` are manual. linux/arm64 images are blocked
  until a native arm64 runner exists (no QEMU); the arm64 binaries are
  still built. What is no longer verified automatically is listed in
  [support-matrix.md](../support-matrix.md#verification-status).
