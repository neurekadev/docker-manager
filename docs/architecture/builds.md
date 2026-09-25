# Image builds (#33)

Manual builds from an HTTP(S) Git repository on an environment, saved build
definitions, and Git credentials for private repositories. Builds run on
the agent through the Engine's BuildKit (Moby client `/build` with a remote
Git context): no docker, buildx or git CLI anywhere.

| package | role |
| --- | --- |
| `internal/gitremote` (+ `gittest`) | pure-Go `git ls-remote` over HTTP(S): smart (pkt-line v0/v1, symref) and dumb protocol; ref resolution; used by manager and agent |
| `internal/manager/gitcreds` | Git credentials: owner-only CRUD/rotation/revocation, sealing, matching (`Select`), connection tests, per-dispatch resolution (`CommandSecrets`) |
| `internal/manager/builds` | build validation, credential selection, `image.build` enqueue, build records, definitions and runs |
| `internal/manager/api/builds.go`, `gitcredentials.go` | routes |
| `internal/agent/builds` | agent executor of `image.build` |
| `internal/jobspec/imagebuild.go` | the job input shared by manager and agent |

## Git credentials

Handled exactly like registry connections ([registries.md](registries.md)):
shared instance resources administered by the owner (recent step-up; API
tokens refused), token sealed with the secret-protection key (context
`git_credentials/<id>/secret`), write-only (keyed fingerprint and version
only), revocation erases the token and keeps the matching rule (no
anonymous fallback), a new `secret` in PATCH rotates and re-activates.
Fields: host (`host[:port]`), optional path prefix (`acme` or
`acme/team`), username, plain-HTTP flag (only then is the token sent to
`http://` repositories).

Matching (`gitcreds.Select`): same host; path prefix matches segment-wise;
the longest prefix wins; a tie is `ambiguous_git_credential` (name one with
`gitCredentialId`); a revoked winner is `git_credential_revoked`.

`POST /git-credentials/{id}/connection-tests` runs an ls-remote from the
manager and resolves a ref; failures are classified (`unauthorized`,
`forbidden`, `not_found`, `rate_limited`, `git_unavailable`,
`ref_not_found`, ...).

## A build

`POST /environments/{id}/images/builds` (capability `image.build` in the
environment, `Idempotency-Key` honored) validates the source (http(s) URL
without credentials, query or fragment; ref; context path inside the
repository; Dockerfile; target; platform; 1-16 tags without digest; build
arguments; timeout ≤ 6 h), selects:

- the Git credential (explicit or matching; none for public repositories);
- registry connections for base images: the explicit `registryIds`, or else
  per registry host the host-wide connection (no repository matcher) that
  applies in the environment (environment binding over unbound, then
  priority; tied hosts are skipped and logged). Base-image references are
  only known inside BuildKit, so repository-specific and stack-bound
  connections must be named explicitly.

It enqueues an `image.build` job whose input names the credentials by ID
(`jobspec.CredentialRefs`), never a secret, and answers **202 + job**. The
build record's ID is the job ID (`GET .../image-builds/{jobId}`); its log
is the job's event stream. Job targets are the tags (image locks,
exclusive); builds per environment are capped by the job engine's build
class (`DOCKYARD_JOB_MAX_CONCURRENT_BUILDS`, default 1).

At dispatch the manager resolves the IDs into the command's `secrets`
(Git and registry credentials, audited as `git_credential.use` /
`registry.use`). The agent executor:

1. `fetch_context` — ls-remote with the Git credential (from memory) and
   resolve the ref to a commit (items `commit`, `ref`);
2. `build` — BuildKit builds `<url>#<commit>[:<context>]`, i.e. exactly
   that commit; the Git credential is served over the build's BuildKit
   session as the secret `GIT_AUTH_HEADER.<host>` (HTTP basic value; never
   in the URL), registry credentials by the session's auth provider
   (#19, #21). BuildKit status and log lines become job progress (step
   changes at once, output batched per step, at most 300 output messages),
   scrubbed of every credential of the attempt (raw, base64 and basic-auth
   forms). Cancellation (`POST /jobs/{id}/cancellations`) stops the running
   build (outcome `cancelled`, via `jobexec.ErrStepCancelled`); the timeout
   stops it with a failure. The result carries the image ID (item `image`).

Build records copy the job's outcome when read (status, resolved commit
and ref, image ID, error, start/finish; duration in the API). Build
argument **values** are stored only in the job input (not exposed by the job
API) and in definitions (shown with `build_definition.read`); records keep
their names, audit records never contain them (#30).

Requirements and limits: the Git server must allow fetching the resolved
commit (protocol v2 or `uploadpack.allowReachableSHA1InWant`; dumb HTTP
works); SSH Git URLs are not supported in v1; the Engine's BuildKit passes
the Git credential to its own `git` process (inside dockerd) as a
configuration argument, as BuildKit always does for Git authentication.

## Build definitions

`/environments/{id}/build-definitions` CRUD (`build_definition.read` /
`.manage`, If-Match on edits) stores a source per environment (names unique
per environment, case-insensitive). `POST .../{definitionId}/runs` starts a
build like the manual route; it needs `image.build` on the definition and
its job targets the definition, which covers the images it tags for
authorization (`authz.TargetResources`). Deleting a definition keeps its
build records. Scheduled rebuilds are not in v1.

## Stack builds (follow-up)

`POST /stacks/{stackId}/builds` (`stack.build`) and building Compose
`build:` sections during deploy need the stack model of #7 (project
directories, revisions). The Compose adapter already builds `build:`
services through the Engine adapter's BuildKit before the SDK runs
(engine-integration.md); what remains is the `stack.build` executor
(load the stack project, build its services with `regauth.All(sc.Secrets)`,
stream progress, honor cancellation) and the route, both on top of #7.

## Tests

Docker-free: `internal/gitremote` (smart/dumb listings, auth, failures,
URL rules), `internal/agent/builds` (exact commit, Git and registry
credentials passed to BuildKit, scrubbed progress/result/journal, anonymous
fallback refused, cancellation, timeout, resume), `internal/agent/engine`
`TestGitAuthServedAsSessionSecret`, `internal/jobexec`
`TestStepCancelledEndsCancelled`, `internal/manager/gitcreds`,
`internal/manager/builds` (end to end on the manager with the job engine:
credential selection, secrets only in the command, rotation/deletion before
dispatch, concurrency cap, records, definitions and runs, whole-database
canary scan), `internal/manager/app/builds_test.go` (HTTP, shaping,
owner-only, audit without credentials or build argument values).

Integration (`-tags integration`, extended `compose-fixtures`):
`TestGitBuildsOnTwoEngines` (Git server fixture, two DinD Engines, public
and private repositories).
