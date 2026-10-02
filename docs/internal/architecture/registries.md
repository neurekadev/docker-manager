# Registry connections (#19)

Manager-owned registry credentials so authorized pulls, Compose
deployments, builds and digest checks work for private images with the
intended account. The owner administers them; everyone else uses them
without ever seeing a secret.

| package | role |
| --- | --- |
| `internal/imageref` | reference and host normalization (Docker Hub aliases), repository matchers; shared by manager and agent |
| `internal/manager/registries` | service: CRUD, rotation, revocation, matching (`Match`, `Select`, `Preview`), connection tests, digest checks (`Check`), pull limits (`PullLimits`, `pulllimits.go`), per-dispatch credential resolution (`CommandSecrets`) |
| `internal/manager/regclient` | minimal OCI Distribution client (manifest HEAD/GET, token/basic auth, platform selection, 401/403/429 classification, backoff, cooldown per host and credential, rate-limit headers, cache, request sharing); `regtest` is its fake registry |
| `internal/manager/api/registries.go` | `/api/v1/registries...` routes |
| `internal/agent/regauth` | turns a command's credentials into `engine.RegistryAuth` for the Engine adapter |

## Model

`registry_connections` (migration `20260925151847_create_registry_connections`):
normalized host (`docker.io` for `docker.io`, `index.docker.io`,
`registry-1.docker.io`, `registry.hub.docker.com`; `host:port` kept
exactly), display name (unique, case-insensitive), credential type
(`password` or `token`; both are sent as username + secret), username,
optional repository matcher (`org/app` exact or `org/*` namespace; on
Docker Hub `nginx` means `library/nginx`), optional environment **or**
stack binding, priority (-1000..1000, higher wins), plain-HTTP flag
(manager-side checks of self-hosted registries only; never Docker Hub),
status (`active`/`revoked`), secret version, keyed fingerprint, last
successful use and last check result.

- The secret is sealed with the secret-protection key
  (`secrets.Keyring.Seal`, context `registry_connections/<id>/secret`) and
  is write-only: API responses, audit records and logs carry only the
  fingerprint (`fp_` + HMAC-SHA256 under a key derived from the
  secret-protection key; not reversible, not brute-forceable without the
  key) and the version. A CHECK constraint keeps "active ⇔ sealed secret".
- Revocation erases the sealed secret but keeps the connection and its
  matching rules: jobs that match it fail visibly (`registry_connection_revoked`,
  `credential_unavailable`) instead of pulling anonymously. Rotating a new
  credential re-activates it; deleting removes it.
- Administration (create, update, rotate, revoke, delete) is owner-only
  with a recent step-up; connection tests are owner-only. API tokens can
  never call these routes (owner routes refuse tokens, #31). Reading
  metadata needs `registry.read` (per connection or instance-wide); other
  capabilities on a connection show only id, name, host and status.

## Matching

`registries.Match` is deterministic and independent of storage order:

1. candidates: same normalized host, repository matcher matches, binding
   applies (stack-bound only for that stack, environment-bound only in
   that environment);
2. order: binding (stack > environment > none), matcher specificity
   (exact > deeper namespace > shallower namespace > any), priority
   (higher first), ID;
3. the first candidate is selected unless the second ties with it on all
   three: then the match is **ambiguous** (`ambiguous_registry_connection`)
   and the request must name one (`registryId`); an explicit connection must
   be a candidate (422 otherwise);
4. no candidate: anonymous access. A revoked winner fails; there is never a
   fallback to another credential or to anonymous access.

The corpus `internal/manager/registries/testdata/matching.yaml` covers
Docker Hub aliases, GHCR namespaces, self-hosted `host:port`, bindings,
priorities, ties and explicit selection. `POST /api/v1/registries/matches`
previews a match (metadata only; needs instance-wide `registry.read`).

## Using a connection in a job (#6, #7, #20, #33)

Handlers never accept credentials in a request. They resolve the
connection with `Service.Select` (returns the selection or
`*domain.AmbiguousRegistryError`, `ErrRegistryConnectionMismatch`,
`ErrRegistryConnectionRevoked`) and put its ID into the job input
(`jobspec.CredentialRefs`):

```json
{"reference": "ghcr.io/acme/app:1.4.2", "registryConnections": ["0190..."]}
```

At **every dispatch** (including re-dispatches after a resume) the job
engine calls `jobs.Options.CommandSecrets`; the manager resolves the IDs
to the current credentials and sends them in the command frame's
`secrets` (`protocol.CommandSecrets`, docs/internal/protocol/agent-v1.md). So:

- no credential is stored with the job, its events or its audit records;
- a rotation applies to every later attempt; a deleted or revoked
  connection fails the job with `credential_unavailable` before anything
  is sent;
- every use is audited as `registry.use` (actor: manager service, job ID,
  connection ID, secret version, outcome; never the secret).

On the agent, `jobexec.StepContext.Secrets` holds the credentials for the
running attempt only: `jobexec.State.Secrets` is never serialized or
cloned, so the fsync'd journal cannot contain them
(`TestCommandSecretsStayInMemory`). Executors call
`regauth.ForReference(sc.Secrets, ref, required)` (pulls; `required` when
the input named a connection for that image, so a missing credential is an
error, not an anonymous pull) or `regauth.All(sc.Secrets)` (BuildKit base
images) and pass the result to the Engine adapter, which keeps it in memory
for that one request (#21, [engine-integration.md](engine-integration.md)).

## Manager-side checks

Registry HTTP calls for connection tests and digest checks (#20) run on the
manager (it owns the credentials and needs no Docker socket); pulls always
run on the agents. `Service.Check` selects the connection, resolves the
digest (index digest and the requested platform's manifest digest) and
records the result and the use. `regclient`:

- HEAD first (Docker Hub counts GETs, not HEADs, against the pull
  allowance), GET only when the registry sends no digest or a platform must
  be selected from an index; the manifest bytes are verified against the
  digest;
- answers one Bearer (token service, `repository:<repo>:pull`) or Basic
  challenge; credentials go only over HTTPS unless the connection allows
  plain HTTP, and never to a plain-HTTP token realm; tokens are cached per
  (host, scope, credential version) for at most five minutes;
- classifies `unauthorized` (401, also a refused credential at the token
  service), `forbidden` (403), `not_found`, `rate_limited` (429),
  `registry_unavailable` (5xx, network), `platform_not_found`,
  `invalid_response`; after a refused credential there is no anonymous
  retry;
- retries 429/5xx/network errors at most 3 attempts with exponential
  backoff and full jitter (1 s base, 30 s cap), honoring `Retry-After`
  (seconds or date) and `RateLimit-Reset`/`X-RateLimit-Reset` up to one
  minute; a longer wait fails at once with the guidance and puts the host
  and credential into a cooldown in which their checks fail with
  `rate_limited` without a request (no retry storm). The cooldown is keyed
  by API host and credential key, because anonymous limits (per IP
  address) and an account's limits are counted apart: an anonymous 429
  never holds back a connection's checks of the same host, nor the
  reverse;
- reports every manifest and blob response (after the authentication
  challenge; a cached or shared result makes none) to the rate-limit
  observer (`Client.SetRateLimitObserver`, `regclient.RateLimit`): host,
  credential key, `RateLimit-Limit`/`RateLimit-Remaining` or their
  `X-RateLimit-*` forms (`100` or `100;w=21600`, the first entry of a
  list; unparsable values are ignored), `RateLimit-Reset`, and for a 429
  (also from the token service) `Retry-After`. Docker Hub sends the
  headers on manifest HEADs, which do not count as pulls; GHCR sends none.
  Other headers (Docker Hub's `docker-ratelimit-source`, the client IP)
  are never read;
- caches successful results for one minute and shares concurrent identical
  checks, keyed by registry, repository, tag/digest, platform and
  credential (connection ID + secret version), so a rotation never reuses
  an old result.

`Service.Created` (#20, display only) reads when the image behind a
host-platform manifest digest was created: one GET of the manifest by
digest (Docker Hub counts it against the pull allowance) and one GET of its
image config blob, both verified against their digests, without retries;
a host in a cooldown is not contacted and a 429 starts one. The result is
cached per digest and credential (a digest's content never changes); an
absent `created` or the Unix epoch of reproducible builds is
`regclient.ErrNoCreated`. Uses with a connection are audited as
`registry.use` (purpose `image_created`). The update check calls it once
per new candidate digest and ignores its errors.

Authentication raises Docker Hub's pull allowance depending on the account
tier; it does not remove rate or abuse limits, and the API says so in 429
messages.

### Pull limits (#217)

`registries.New` sets the service as the client's rate-limit observer.
Each report becomes a row of `registry_pull_limits` (migration
`20261002090000_create_registry_pull_limits`), keyed by the reference's
normalized host and the connection ID of the credential key (`''` for
anonymous access): the last reported limit, remaining pulls, window and
reset (`observed_at`, kept when a later answer reports none), the last
answer (`checked_at`), the last 429 (`last_limited_at`) and when its limit
resets (`limited_until`: `Retry-After`, else the reported reset; cleared by
a later answer that is not a 429). `RegistryPullLimit.Limited(now)`: the
last answer was a 429 and `limited_until` has not passed.

- A key is written when the limit, remaining count, window or 429 state
  changes, else at most once a minute (memory per host and connection,
  bounded); a failed write is logged at warn and never fails the check.
- Deleting a connection or rotating its credential (maybe another account)
  deletes its rows; anonymous rows stay.
- The numbers are what the registry reported to the manager's checks:
  pulls on the hosts go through the Docker Engine, which the registry
  counts but the manager does not see. An account's limit covers every
  pull with that account; anonymous access is counted per IP address.
- `GET /api/v1/registries/pull-limits` lists them (no paging): a
  connection's row with the full view of it (`registry.read` on it),
  anonymous rows with `registry.read` on the instance. Rows are written
  outside requests and publish no event: the Registries page refetches
  the list every minute and after its own changes (a connection test is an
  audited POST, so the `registries` topic refreshes it).

## Tests

Docker-free: `internal/imageref` (normalization, matchers),
`internal/manager/regclient` (fake registry: bearer/basic auth, no
anonymous fallback, 403/404, 429 with Retry-After, long Retry-After
cooldown, cooldown per credential, 5xx backoff, jitter bounds, platform
selection, cache and deduplication, plain-HTTP realm refusal, image
creation times, rate-limit headers and reports), `internal/manager/registries`
(matching corpus, sealing, owner-only, validation, connection tests with
401/403/429, rotation/revocation, `Select` errors, cached/audited checks,
pull limits per host and credential,
the job engine resolving credentials at dispatch across a rotation with a
whole-database canary scan), `internal/agent/jobs` (secrets never reach the
journal, logs, results or reports), `internal/agent/regauth`,
`internal/manager/app/registries_test.go` (HTTP: lifecycle, shaping,
owner-only, API tokens refused, audit, pull limits and their shaping,
whole-database canary scan).

Connections against a real registry and pulls through real Engines are
**not verified by automated tests**: the former integration tests (registry
fixture with a fault proxy and two DinD Engines, scheduled digest updates of
a private image, #20) were removed on 2026-09-25.
