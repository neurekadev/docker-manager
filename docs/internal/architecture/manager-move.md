# Moving Docker Manager to a new server

The owner moves Docker Manager, with every app on its own server, to a
new server. For the user it is three things: paste one generated
`compose.yaml` and `.env` on the new server and start it, press **Move
everything**, point DNS at the new server. Docker Manager does the rest:
the apps move first (an environment migration, [migrations.md](migrations.md),
"Environment migration"; the reverse proxy moves with the apps behind
it), then the manager hands itself over to the new server (a **live
handoff**: the old manager locks itself, hands an encrypted, checksummed
copy of its state to the new manager and never controls an agent again),
and the agents of both servers are told the new manager's address. The
new manager becomes the same instance: same database, secret key,
template drafts, users, sessions, API tokens and agent credentials; no
agent enrolls again.

The new server has **no HTTPS** during the move: the reverse proxy (the
"web server") is one of the old server's stacks and only moves with the
apps. So the two servers talk over plain HTTP on the owner's network, and
the handoff is encrypted and authenticated end to end by the move code
(see "Pairing and transport"); the code itself never crosses the
network.

## The flow

1. **Set up the new server** (old manager, owner, step-up:
   `create-manager-move`). The owner enters this server's address
   (prefilled from the service address of the environment next to the
   manager, `get-manager-move-defaults`) and the new server's, each an IP
   address or host name with an optional port (8080 when absent), and
   optionally the new environment's name (default: the new server's
   host). Docker Manager creates the move (its code), an enrollment token
   for the new server's agent (intent new, 24 hours) and returns once the
   new server's `compose.yaml` and `.env` (`agents.MoveFiles`, next to the
   install commands): the Quickstart's `compose.yaml` with
   - `docker-manager`: `DOCKER_MANAGER_PUBLIC_URL` and
     `DOCKER_MANAGER_TRUSTED_PROXIES` as on this manager (the Quickstart's
     `172.16.0.0/12` when unset), `DOCKER_MANAGER_MOVE_FROM=http://<this
     server>:<port>` and `DOCKER_MANAGER_MOVE_CODE=<code>` (waiting mode);
   - `docker-agent`: `DOCKER_AGENT_MANAGER_URL=http://<this server>:<port>`,
     `DOCKER_AGENT_MANAGER_ALLOW_HTTP=true`, the enrollment token and the
     environment name (it enrolls into the **old** manager as a new
     environment, directly, not through the proxy, so moving the proxy
     never cuts it off).
   The compose file reads the move variables from `.env` with defaults
   (`${DOCKER_AGENT_MANAGER_URL:-http://docker-manager:8080}`, empty
   otherwise), so removing the move lines from `.env` afterwards leaves
   exactly the Quickstart's setup. The owner runs `docker compose up -d`
   (the page also offers both files as one command to paste, see
   `docs/internal/web.md`); the old manager's page follows
   `get-manager-move` (`newServer`: the environment enrolled and online,
   the waiting manager checked in), kept current by live events (see "Live
   updates"). Files that are lost or expired are replaced, see "New setup
   files".
2. **Check**: the environment migration's preview (the environment next to
   the manager → the new server's, `create-environment-migration-preview`)
   plus the move's own state, in one screen, with the reminders: the usual
   address stops working when the proxy's group moves until DNS points at
   the new server; a proxy route that sends Docker Manager's address to
   this server's IP must be pointed at the new server's IP; progress can
   be followed at `statusUrl` (`http://<new server>:<port>`) meanwhile.
3. **Move everything** (`create-manager-move-run`, owner, step-up; the
   `manager.move` job, see below): runs the environment migration of
   every stack to the new environment, waits for it, then marks the move
   **ready**. The waiting manager, which has been checking in since it
   started, now asks for and gets the handoff: the old manager locks itself
   (read-only, no new jobs; running jobs finish), tells the agents of both
   servers the new manager's address (`manager.redirect`, see "Agents
   follow"), refuses agents, and streams the copy. The new manager checks
   it, restarts as the instance and confirms. If the migration does not
   complete, nothing is handed over: the move goes back to open, the old
   manager keeps running and the owner fixes and retries (**Move
   everything** again moves what is left).
4. **Point DNS at the new server.** The new manager's status page at
   `http://<new server>:8080` says so (`GET /api/v1/move/status`, phase
   `complete`); once the usual address leads to it the owner signs in as
   always and sees **Move complete** (`get-manager-move` on the new
   manager), with removing the old server's stopped copies and archiving
   the old environment one click away.

## New setup files

The files are shown once and the enrollment token lives 24 hours. When
the owner lost them (a reload) or the token expired before the new server
was set up, **Create new setup files** (`create-manager-move-setup-files`,
owner, step-up, `managermove.NewSetupFiles`) returns them again, in the
creation's shape:

- **A new move code for the same move** (`dmm_<same id>_<new secret>`,
  sealed in place of the old one). The old code stops working at once:
  the waiting manager started with the old `.env` gets 401
  `move_code_invalid` (its status page says to use the newest `.env`).
  The check-in and handoff compare the request's code with the sealed one
  again under the service lock (`sameCode`), so a request signed with the
  old code that raced the change is refused too. The last check-in is
  forgotten (`checkedInAt`, `handoffAddress`): the new manager checks in
  again with the new code. The move's expiry does not change.
- **The agent part:** unless the new server's agent already enrolled with
  the move's token, the old token is revoked and a new one issued (24
  hours, the same environment name). Once it enrolled (the move's
  `targetEnvironmentId`, or the used token's agent) its environment is
  kept: the `.env` has no `DOCKER_AGENT_ENROLLMENT_TOKEN` and no
  `DOCKER_AGENT_ENVIRONMENT_NAME` (a comment says the agent is already
  connected; `agents.MoveFiles` without a token) and the answer says
  `agentEnrolled`. The agent keeps its credential in its volume (an
  enrolled agent ignores an enrollment token anyway): the owner replaces
  the `.env` in the same folder and runs `docker compose up -d`. An
  environment removed or archived since gets a new token.
- **Allowed while the move is `open` or `ready`.** `moving` is refused
  (409 `manager_move_state`): the new server's agent restarts with the new
  `.env` and would cut off the app that is moving. From `draining` on the
  handoff has begun (the lock refuses it with `manager_moved` first).
  Audited `manager.move.setup_files` (details `state`, `enrollmentId`,
  `previousEnrollmentId`, `agentEnrolled`; never the code or the token).

The wizard offers it on the New server step while the move is open and
the new server not ready, when the files are no longer on the page or the
token expired.

## Live updates

The old manager's wizard, the shell's banner and Move complete follow the
live stream ([live-sync.md](live-sync.md)) instead of polling. The move
service (`managermove/live.go`) publishes on the bus:

- `manager_move.updated` (`ResourceID` the move's ID; the owner only,
  `manager.move`): the move's state changed (created, moving, ready,
  draining, handed off, confirmed, cancelled, expired), new setup files,
  the target environment recorded, the migration started, the redirects
  recorded, a check-in that starts counting (or comes from another
  address; not every check-in), a check-in that stopped counting (the Run
  loop wakes when `checkedInAt + CheckInFresh` passes), each confirmation
  attempt and the acknowledgement on the new manager. What the view shows
  from elsewhere is followed on the bus (`followBus`): the new server's
  environment online, offline, archived or reattached, enrollments used or
  revoked, agents enrolled, and every change of the `manager.move` job
  (its progress carries the apps' progress); each republishes the move
  the view shows (active, else arrived).
- `manager_move.lock_changed` (`ResourceID` `instance`, no move ID; every
  signed-in stream): the lock `GET /auth/session` reports (`none`,
  `moving`, `moved`) changed: draining begins, the new manager confirms, a
  locked move ends.

The stream shapes both as `invalidate` on topic `manager` (kinds
`manager_move`, `manager_move_lock`); the web refreshes the move, or the
session. The new server's status page keeps polling `GET
/api/v1/move/status` (no sign-in, no stream there), and a manager in
waiting mode starts no live stream at all (`MoveGate`).

## Waiting mode and status page (new manager)

A manager started with `DOCKER_MANAGER_MOVE_FROM` and
`DOCKER_MANAGER_MOVE_CODE` (both or neither, `config.MoveConfig`) on an
empty data directory (no owner and no environment; `app.decideWaiting`)
is **waiting**: the move lock is at `movelock.Waiting`, so the job engine
starts nothing, the scheduler fires nothing, agents are refused with 503
and `Retry-After: 60` (never 401-class), and every API operation answers
503 `manager_move_waiting` with `Retry-After: 10` (`waitingGuard`) except
health, capabilities and `GET /api/v1/move/status` (public, read-only:
phase, the old manager's progress it last heard, bytes copied, error
class and recovery, never the code or any content). The web app is
served, so the SPA can render the status page from that route alone.

The waiting loop (`managermove` `runWaiting`, in the move service's
`Run`) checks in with the old manager at once and then every 10 s
(`WaitInterval`, `GET /api/v1/manager/move/check-in`, a signed read that
the audit trail leaves out): while the move is `open` or `moving` it
keeps checking in (phase `waiting`, with the old move's state and its
apps' progress); from `ready` on it asks for the handoff, where
`jobs_running` (phase `finishing_jobs`) and a racing
`manager_move_not_ready` keep it asking at the answer's `Retry-After`; an
unreachable old manager or a broken transfer keeps it asking with
backoff up to a minute (phase `connecting`, `manager_move_unreachable`,
`manager_move_transfer_failed`); a refusal (`move_code_invalid`,
`move_clock_skew`, `manager_move_refused`, `manager_move_state_invalid`)
keeps it asking once a minute with the recovery shown (only a change on
either side helps). A received package is checked and staged (phases
`copying`, `checking`, `staging`); a refused copy
(`manager_move_schema_incompatible`, `manager_move_state_invalid`,
`manager_move_not_handed_off`, `move_code_invalid`,
`manager_move_stage_failed`) stops the loop (phase `failed`) until the
manager restarts. A staged copy requests the controlled restart (phase
`restarting`); the next start applies it and runs as the instance
(`finishMove`, confirm). Once it runs the instance the two variables are
ignored (the status page answers `complete` while they are set: remove
them from `.env`). With other data (an owner or an environment) they are
ignored with a warning.

## Pairing and transport

- The move's code (`dmm_<id>_<secret>`, 256-bit secret) is shown only
  inside the generated `.env`. The old manager keeps the whole code
  **sealed** with its keyring (`manager_moves.sealed_code`, context
  `manager_moves/<id>/code`): it needs the secret to authenticate and
  encrypt; there is no plain copy and no verifier. An ended move
  (cancelled, expired) forgets it.
- **Authentication without sending the code**: requests carry
  `Authorization: DMM <id>:<unix time>:<nonce>:<mac>` with a random
  128-bit nonce (base64url) and mac = base64url(HMAC-SHA256(K, `DMM1\n`
  method `\n` path `\n` time `\n` nonce `\n` move ID)), K =
  HKDF-SHA256(secret, info `docker-manager-move/auth/v1`)
  (`managermove.SignRequest`). The old manager checks the MAC first
  (unknown or ended moves, wrong MACs and replays answer 401
  `move_code_invalid` alike), then a time within ±5 minutes of its clock
  (401 `move_clock_skew`, only to a holder of the code), then the nonce
  once (in-memory replay cache kept for the window). The identity
  middleware leaves a `DMM` request anonymous and drops its cookies.
- **Encryption**: the handoff stream is encrypted and authenticated with
  XChaCha20-Poly1305 in 64 KiB chunks (STREAM construction: an 8-byte
  magic and a random 19-byte nonce prefix as header and associated data;
  chunk i's nonce is the prefix, i as big-endian uint32 and a last-chunk
  flag; the final chunk is shorter than 64 KiB, possibly empty) under
  HKDF-SHA256(secret, info `docker-manager-move/package/v1\x00<move ID>`).
  A stream that is cut, reordered or changed does not open. The inner tar
  and its per-part SHA-256 manifest are unchanged. The secret key part
  (`secret-key.sealed`) stays sealed under its own HKDF of the code as
  before (defence in depth; it also binds the instance and key IDs).
  Plain HTTP is therefore allowed for `DOCKER_MANAGER_MOVE_FROM` and on
  the old manager's move routes (no secure-origin check); an attacker on
  the network sees neither the state nor the code, and cannot replay or
  alter a request.
- The code is valid until the move is confirmed or cancelled (a large
  migration may take hours), at most 7 days (`CodeLifetime`); from the
  handoff on it no longer expires. New setup files replace it with a new
  code of the same move (see "New setup files").

## Agents follow

Just before it refuses agents (once jobs finished, once per move), the old
manager sends `manager.redirect {url, generation: current + 1}` to every
connected agent it can place, in parallel, each bounded by 5 s:

- the new server's environment (enrolled with this move's token):
  `http://docker-manager:8080` (its own server's manager in the generated
  compose);
- the environment next to the old manager (learned from its
  `manager.identity` answer, `colocated`): `http://<new server>:<port>`;
- other agents: none (they dial the public address, which DNS moves).

The outcome of each (sent, or `offline`, `unsupported`, `timeout`,
`refused`) is recorded on the move before the copy is taken, so it
travels to the new manager. The agent keeps the address in its state (it
replaces `DOCKER_AGENT_MANAGER_URL` from then on; plain HTTP allowed
because an authenticated manager sent it; `docs/internal/configuration.md`,
"Manager address after a move"), raises its generation to the one given
and reconnects there; the waiting manager refuses it with 503 until it
runs the instance. An agent that did not get the redirect is listed in
Move complete with its fix.

## Old manager: move states

One row per move in `manager_moves` (addresses, enrollment, the source and
target environments, the check-in, the latest `manager.move` and its
migration, redirects, times, the sealed code). States:

| State | Meaning | API | Jobs, schedules | Agents |
| --- | --- | --- | --- | --- |
| `open` | created: waiting for the new server, then for Move everything | normal | normal | normal |
| `moving` | `manager.move` moves the apps | normal | normal | normal |
| `ready` | the apps moved; the waiting manager's next request gets the handoff | normal | normal | normal |
| `draining` | the waiting manager asked for the handoff | read-only | no new job; running jobs finish; queued jobs wait (they travel in the copy) | connected |
| `handed_off` | the state was copied out | read-only | none | refused |
| `confirmed` | the new manager runs the instance | read-only | none | refused |
| `cancelled`, `expired` | the move did not happen | normal | normal | normal |

- A move that was not handed off (`open` to `draining`) expires seven days
  after it was created (the lock ends, the code is forgotten, a running
  `manager.move` is cancelled, an unused enrollment token revoked). One
  move is `open` or later at a time (409 `manager_move_exists`).
- Every check-in and handoff request of the waiting manager records its
  check-in (`checkedInAt`, `handoffAddress`); `newServer.managerCheckedIn`
  means within the last two minutes.
- **Read-only** (`draining`, `handed_off`, `confirmed`): every non-GET
  `/api/v1` request is refused with 409 `manager_moved` except sign-in
  (password, passkey, recovery code, step-up), sign-out and the move
  routes; the job engine refuses new jobs (`jobs.ErrManagerMoved`, also
  scheduled ones and follow-ups of running jobs) and does not dispatch
  queued ones; the scheduler fires nothing.
- **Agents refused** (`handed_off`, `confirmed`): the agent handler
  answers the upgrade with HTTP 503 and `Retry-After: 60`, and open
  sessions are closed with 1012 (service restart). Never 401, 4401, 4403
  or 4409: agents delete their credential or go idle on those.
- The state is read at start: a restarted old manager stays locked.
- **Cancel** (owner, step-up): allowed until the handoff (`open`,
  `moving`, `ready`, `draining`; stacks that already moved stay on the new
  server). In `handed_off` only with `resumeHere` and the typed instance
  name (the owner states the new manager never started with the copy);
  never in `confirmed` (409 `manager_move_state`). When agents already
  heard the new address (a redirect was sent, or the copy left), ending
  the move raises the instance generation by two and restarts the manager
  (see "Two managers never control the same agents").

## Move everything (`manager.move`)

`create-manager-move-run` (owner, step-up) needs an `open` or `ready`
move whose new server's agent enrolled with the move's token and is
online and whose waiting manager checked in within two minutes (409
`manager_move_new_server_missing`); a `moving` move answers 409
`manager_move_state`. The move becomes `moving` and the `manager.move`
job (manager executor, the owner's principal, target `manager:instance`
with an exclusive lock: one at a time; interrupted by a manager restart)
runs:

1. `migrate` — the environment migration (`migrations.Service.StartEnvironment`,
   the job's principal, idempotency key `manager-move-<job>`) of every
   movable stack from the environment next to the manager (recorded at
   creation, else resolved now) to the new server's environment; its ID
   is recorded on the move and in the step output (a resumed step waits
   for the same migration). Nothing to move (no environment next to the
   manager, or only the `no_stacks` blocker) goes straight on. Other
   blockers fail the job (`manager_move_apps_blocked`, the blockers'
   messages). The step waits for the migration (polled and on its
   changes, a cancellation of `manager.move` cancels it); a migration that
   does not succeed fails the job (`manager_move_apps_not_moved`, the
   migration's recovery and "press Move everything again").
2. `ready` — the move becomes `ready`.

A `manager.move` that does not succeed puts the move back to `open` (its
finish hook); the next Move everything starts a new migration, which
moves what is left. `get-manager-move` shows the latest run's state,
error and recovery and the migration's progress (`stacksMoved` of
`stacksTotal`, `currentStack`).

## Handoff protocol (old manager's routes)

The three routes are authenticated by the move request signature (see
"Pairing and transport"), not by a session, over HTTP or HTTPS.

- `GET /api/v1/manager/move/check-in` — records the check-in (until the
  handoff) and answers `state` (`open`, `moving`, `ready`, `draining`,
  `handed_off`, `confirmed`), `stacksMoved`, `stacksTotal`,
  `currentStack` and, while draining, `jobsRunning`. Not audited (a read
  every 10 s for as long as the apps move).
- `POST /api/v1/manager/move/handoff`
  - `open`, `moving`: 409 `manager_move_not_ready` with `Retry-After: 10`
    and `X-Docker-Manager-Move-State`, `X-Docker-Manager-Move-Stacks`
    (`<moved>/<total>`) and `X-Docker-Manager-Move-Current-Stack`.
  - `ready` → `draining`. While jobs are dispatched, running or cancelling:
    409 `jobs_running` with `Retry-After: 10` and the count in
    `X-Docker-Manager-Jobs-Running`; the caller retries. Queued jobs are
    not waited for: they are not dispatched while the manager moves and
    run on the new manager (they are in the copy).
  - With no job left: the redirects are sent and recorded (once), agents
    are refused (before the copy is taken, so no agent changes anything
    the copy misses), the state is copied (`VACUUM INTO`
    `<data>/move-outgoing/<move>/`; in the copy the instance's
    `generation` goes up by one and the move's row becomes `arrived`), the
    state becomes `handed_off`, and the response streams the encrypted
    package (`application/octet-stream`, `X-Docker-Manager-Move-Size`: the
    parts' total length): a tar of `state.json` (format
    `docker-manager-move`, instance ID, generation, app version, schema
    migrations, secret key ID, template drafts included),
    `docker-manager.db`, `secret-key.sealed` (the secret key sealed with
    XChaCha20-Poly1305 under HKDF-SHA256 of the code's secret, AAD =
    format, instance ID, key ID) and `templates.tar.gz`, then
    `manifest.json` with the length and SHA-256 of every part.
  - Repeating it in `handed_off` streams the same copy again (a transfer
    that broke is retried; nothing else changed since); a `confirmed`
    move answers 409 `manager_move_state`.
- `POST /api/v1/manager/move/confirm` — the new manager applied the copy
  and runs: `handed_off` → `confirmed` (repeating it is harmless; the
  package is deleted). Any other state answers 409 `manager_move_state`
  (the move was cancelled or resumed here, or the address reaches the new
  manager itself).

## New manager: receiving

- Waiting mode stores the decrypted stream under
  `<data>/move-incoming/waiting/`, checking every part's length and
  SHA-256 against the manifest (unknown, repeated or oversized parts are
  refused), then verifies it: the sealed key opens with the code; the
  database copy passes `PRAGMA quick_check`, its instance ID and
  generation equal `state.json`'s, the schema is one this version knows (a
  newer old manager is refused: update the new one first), the key opens
  the sealed settings (the Recovery Key record, when there is one), and
  the move row is `arrived`. It stages the copy as a restore of kind
  `move` into `restore-pending` (`backups.StageRestore`; the marker
  carries the old manager's address and the move code sealed with the
  moved secret key) and requests a controlled restart. Anything left in
  the incoming directory is deleted.
- At start `ApplyPendingRestore` puts the copy in place (as for a backup
  import). `finishMove` (instead of `finishRestore`) keeps sessions, API
  tokens and agents, records the old manager's address on the arrived
  move (the code stays sealed there until the confirmation), records
  `system.move`, and calls the old manager's confirm (signed) in the
  background: retried with backoff (5 s to 5 min) on network errors, 5xx,
  429 and `move_clock_skew`; it stops at 200 (confirmed) and at 401 or 409
  (refused: the old manager must no longer run the instance). Attempts,
  the last error class and the confirmation time show on
  `GET /api/v1/manager/move`. When the old manager never confirms, the
  owner acknowledges it (`create-manager-move-acknowledgement`, step-up):
  the confirmation stops and the sealed code is forgotten.

## Two managers never control the same agents

- The old manager refuses agents from `handed_off` on, before the copy
  leaves it.
- **Generation**: the instance row carries `generation`, raised by one in
  every handed-off copy and sent in `manager.redirect`. The `welcome` and
  `manager.identity` send it; the agent keeps the highest generation it
  has seen (`<state>/manager.json`) and closes the session of a manager
  with a lower one with 4421 (then retries with backoff, keeping its
  credential). A resurrected old manager, or an old backup restored
  somewhere, can never command an agent that met the new one.
- Agents raise their generation as soon as a redirect arrives, before the
  copy leaves. So ending a move after a redirect or a handoff (cancel,
  resume here, expiry) raises the old instance's generation by two (above
  any copy that left) and restarts the manager: agents that got the
  redirect accept it again, and a new manager that did start from the
  copy is refused by agents that met the resumed old one.

## Move complete (new manager)

`GET /api/v1/manager/move` answers the arrived move: from where, when,
whether the old manager confirmed (or the owner acknowledged it), and:

- `redirects`: the agents the old manager could place, with `sent`,
  `connected` (online here or connected since the arrival) and
  `needsFix` (not sent and not connected since: set
  `DOCKER_AGENT_MANAGER_URL` to `url` by hand; on the new server removing
  the move lines from `.env` does it). The rule never looks at the address
  an agent reports (the new server's agent reports
  `http://docker-manager:8080`, which reaches this manager).
- `oldEnvironment`: the old server's environment with the stacks still
  there (`stackCount`), the moved stacks' stopped copies still held
  (`stoppedCopies`, `migrations.Service.RetainedSources`; removed from the
  environment migration, `migrationId`) and whether it is archived.

## Security

- The move code is a secret: shown once inside the `.env`, never logged,
  audited or stored in clear (sealed with the secret key), never sent over
  the network (requests are signed with it, the package is encrypted with
  it); seven days at most; one move at a time. The secret key only leaves
  the old manager inside the encrypted package, sealed once more under the
  code.
- Only the owner creates, runs or cancels a move; a new manager accepts a
  move only on an empty data directory.
- Audit: `manager.move.create`, `manager.move.setup_files`,
  `manager.move.run`, `manager.move.cancel`, `manager.move.handoff` (with
  the requesting address and the number of redirects),
  `manager.move.confirm` on the old manager; `system.move` and
  `manager.move.acknowledge` on the new one.

## Routes

| Route | Capability | Notes |
| --- | --- | --- |
| `GET /api/v1/manager/move/defaults` (`get-manager-move-defaults`) | owner (`manager.move`) | this server's address prefill and the environment next to the manager |
| `POST /api/v1/manager/moves` (`create-manager-move`) | owner, step-up | 201: the move, `composeYaml`, `env` (once), `statusUrl`; audited `manager.move.create` |
| `POST /api/v1/manager/move/setup-files` (`create-manager-move-setup-files`) | owner, step-up | open or ready move: the files again (same shape, `agentEnrolled`), a new code, a new token unless the agent enrolled; audited `manager.move.setup_files` |
| `GET /api/v1/manager/move` (`get-manager-move`) | owner | the open or in-progress move (new server, source environment, progress, `jobsRunning`, redirects), else the arrived move (Move complete); 404 none |
| `POST /api/v1/manager/move/runs` (`create-manager-move-run`) | owner, step-up | 202 + `manager.move`; audited `manager.move.run` |
| `POST /api/v1/manager/move/cancellations` (`create-manager-move-cancellation`) | owner, step-up | body `resumeHere`, `instanceName`; audited `manager.move.cancel` |
| `POST /api/v1/manager/move/acknowledgements` (`create-manager-move-acknowledgement`) | owner, step-up | new manager: the old manager never confirmed; audited `manager.move.acknowledge` |
| `GET /api/v1/manager/move/check-in` (`get-manager-move-check-in`) | public, signed | the waiting manager's check-in: state and progress; not audited |
| `POST /api/v1/manager/move/handoff` (`create-manager-move-handoff`) | public, signed | not_ready, jobs_running or the encrypted stream; audited `manager.move.handoff` |
| `POST /api/v1/manager/move/confirm` (`create-manager-move-confirmation`) | public, signed | audited `manager.move.confirm` |
| `GET /api/v1/move/status` (`get-move-status`) | public | waiting mode's status (`none` elsewhere, `complete` after the move while the variables are set) |

The owner routes check `manager.move` (owner-only catalog key) before
availability. API tokens never reach them.

## Implementation

| Piece | Where |
| --- | --- |
| Move rows, generation | `manager_moves`, `instance.generation` (`store/managermoves.go`, `store/instance.go`); `domain.ManagerMove` |
| Configuration | `config.MoveConfig` (`DOCKER_MANAGER_MOVE_FROM`, `DOCKER_MANAGER_MOVE_CODE`); waiting mode decided in `app.decideWaiting` |
| Lock | `internal/manager/movelock` (levels open, read-only, agents refused, waiting), set by `managermove` from the current move and at start (`managermove.LockLevel`) and by `app.decideWaiting`; consulted by `api.Register` (`moveGuard` with `allowedWhileMoved`, `waitingGuard` with `allowedWhileWaiting`), `jobs.Engine` (`Enqueue`, `DispatchPending`), `scheduler.Service` (`Tick`), `alerts.Service` (reconcile and dispatch pause) and `agents` (handler 503, hub 1012) |
| Creation, files | `managermove/service.go` (`CreateMove`, `Defaults`, `Current`, `Cancel`, `endMove`), `setupfiles.go` (`NewSetupFiles`), `render.go` (addresses), `agents.MoveFiles` |
| Live updates | `managermove/live.go` (`publish`, `published`, `followBus`, `announceStaleCheckIn`), `events.ManagerMoveUpdated`, `events.ManagerMoveLockChanged`, topic `manager` |
| Move everything | `managermove/movejob.go` (`manager.move`, `jobspec.ManagerMove`) |
| Signatures, encryption | `managermove/auth.go`, `crypt.go`, `seal.go` (the key part) |
| Check-in, handoff, redirects, confirm | `managermove/handoff.go`, `redirect.go`, `package.go` |
| Waiting mode | `managermove/waiting.go` (`runWaiting`, `WaitStatus`, verify and stage) |
| Arrival | `app.(*Manager).finishMove` → `managermove.FinishArrival`, `StartConfirming` (`finish.go`) |
| Move complete | `managermove/complete.go` |
| Co-location | `app`'s `manager.identity` reconciler → `managermove.ObserveColocation` |
| Web UI | `web/src/lib/features/managermove` (`docs/internal/web.md`): Settings, Move to a new server (`ManagerMoveWizard`: the files and the one command to paste, Create new setup files, waiting for the restart after a resume), the new server's status page `/moving` (`WaitingStatus`, reached through the root layout's `MoveGate`, which starts the live stream only when not waiting), Move complete (`MoveCompleteCard`), the shell's banner of a locked manager |
