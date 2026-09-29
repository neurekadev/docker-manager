# Moving Docker Manager to a new server

The owner moves Docker Manager, with the environment on its own server,
to a new server. The move is a **live handoff** between two managers: the
old manager locks itself, hands a checksummed copy of its state to a new
manager started empty on the new server, and never controls an agent
again. The new manager becomes the same instance: same database, secret
key, template drafts, users, sessions, API tokens and agent credentials.
Agents follow it without enrolling again. The stacks of the old server
then move to the new server with an environment migration
([migrations.md](migrations.md), "Environment migration") run by the
new manager, and the old environment is archived.

Apps never stop for the handoff itself: only control pauses. Containers
keep running on every server while no manager accepts agents.

## Order of the whole move

1. **Prepare** — the owner starts the usual Quickstart `compose.yaml`
   (manager + agent) on the new server with the **same**
   `DOCKER_MANAGER_PUBLIC_URL`. The new manager opens its setup page.
2. **Move code** — on the old manager the owner creates a move code
   (owner only, step-up). The code is shown once.
3. **Handoff** — on the new manager's setup page the owner enters the old
   manager's address and the code. The new manager pulls the state from
   the old one (`manager.receive` job), which locks itself; the new
   manager checks the copy and restarts as the instance.
4. **Point the address** — the owner points `DOCKER_MANAGER_PUBLIC_URL`
   (DNS or reverse proxy) at the new server. Remote agents reconnect to it
   on their own.
5. **Finish** (checklist on the new manager) — connect the new server's
   agent (a new environment), point the old server's agent at the public
   address (it dialed the old manager's internal address), migrate the old
   environment's stacks to the new one, archive the old environment.

## Old manager: move states

One row per move in `manager_moves` (id, code verifier, state, times,
creator). States:

| State | Meaning | API | Jobs, schedules | Agents |
| --- | --- | --- | --- | --- |
| `open` | code created, nothing locked | normal | normal | normal |
| `draining` | a new manager asked for the handoff | read-only | no new job; running jobs finish; queued jobs wait (they travel in the copy) | connected |
| `handed_off` | the state was copied out | read-only | none | refused |
| `confirmed` | the new manager runs the instance | read-only | none | refused |
| `cancelled`, `expired` | the move did not happen | normal | normal | normal |

- An `open` or `draining` move expires one hour after its code was
  created (the lock ends, the code is refused); from `handed_off` on it no
  longer expires. One move is `open` or later at a time (409
  `manager_move_exists`).
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
- **Cancel** (owner, step-up): allowed in `open` and `draining`. In
  `handed_off` only with `resumeHere` and the typed instance name (the
  owner states the new manager never started with the copy; agents that
  met it refuse this manager anyway); never in `confirmed` (409
  `manager_move_state`).

## Handoff protocol (old manager's routes)

Both routes are authenticated by the move code (`Authorization: Bearer
dmm_<id>_<secret>`; the database keeps the SHA-256 verifier, compared in
constant time), not by a session, and are refused unless the request
arrives over a secure origin (`requestinfo`), like setup.

- `POST /api/v1/manager/move/handoff`
  - Unknown, wrong, expired and cancelled codes answer 401
    `move_code_invalid` alike; a confirmed move 409 `manager_move_state`.
  - `open` → `draining`. While jobs are dispatched, running or cancelling:
    409 `jobs_running` with `Retry-After: 10` and the count in
    `X-Docker-Manager-Jobs-Running`; the caller retries. Queued jobs are
    not waited for: they are not dispatched while the manager moves and
    run on the new manager (they are in the copy).
  - With no job left: agents are refused (before the copy is taken, so no
    agent changes anything the copy misses), the state is copied
    (`VACUUM INTO` `<data>/move-outgoing/<move>/`; in the copy the
    instance's `generation` goes up by one and the move's row becomes
    `arrived`), the state becomes `handed_off`, and the response streams a
    tar (`X-Docker-Manager-Move-Size`: the parts' total length):
    `state.json` (format `docker-manager-move`, instance ID, generation,
    app version, schema migrations, secret key ID, template drafts
    included), `docker-manager.db`, `secret-key.sealed` (the secret key
    sealed with XChaCha20-Poly1305 under HKDF-SHA256 of the code's secret,
    AAD = format, instance ID, key ID) and `templates.tar.gz`, then
    `manifest.json` with the length and SHA-256 of every part.
  - Repeating it in `handed_off` streams the same copy again (a transfer
    that broke is retried; nothing else changed since).
- `POST /api/v1/manager/move/confirm` — the new manager applied the copy
  and runs: `handed_off` → `confirmed` (repeating it is harmless; the
  package is deleted). Any other state answers 409 `manager_move_state`
  (the move was cancelled or resumed here, or the address reaches the new
  manager itself).

## New manager: receiving

- `POST /api/v1/setup/move` (setup open: no owner yet, secure origin)
  with the old address and the code starts `manager.receive` (manager
  executor, service principal, target `manager`), shown on
  `GET /api/v1/setup/status` (`managerMove`: state, step, waiting jobs,
  bytes, error code and recovery). The address must be an `https` origin;
  the code must look like a move code (422 otherwise); a running receive or
  import, or a staged restore, answers 409 `manager_move_in_progress`. The
  code stays in memory for the job only (never in its input): a restart
  interrupts the job (start again with the same code).
- `manager.receive` steps:
  1. `handoff` — calls the old manager's handoff (TLS verified, no
     redirects, a transfer idle for 2 minutes is cut), retrying on
     `jobs_running` (up to 30 minutes), after a broken or damaged transfer
     (5 times) and while the old manager is unreachable (5 times, with
     backoff), and stores the stream under `<data>/move-incoming/<job>/`,
     checking every part's length and SHA-256 against the manifest
     (unknown, repeated or oversized parts are refused).
  2. `verify` — opens the sealed key with the code; opens the database
     copy: `PRAGMA quick_check`, the instance ID and generation equal
     `state.json`'s,
     the schema is one this version knows (a newer old manager is
     refused: upgrade the new one first), the key opens the sealed
     settings (the Recovery Key record, when there is one), the move row is
     `arrived`.
  3. `stage` — writes the copy as a staged restore of kind `move` into
     `restore-pending` (`backups.StageRestore`; the marker carries the
     move code sealed with the moved secret key) and requests a controlled
     restart. A failed receive deletes its files.
  Failure classes: `move_code_invalid`, `manager_move_refused` (403, 404:
  not a manager that can hand off), `manager_move_unreachable`,
  `manager_move_jobs_running`, `manager_move_transfer_failed`,
  `manager_move_state_invalid`, `manager_move_schema_incompatible`,
  `manager_move_not_handed_off`, `manager_move_code_lost`.
- At start `ApplyPendingRestore` puts the copy in place (as for a backup
  import). `finishMove` (instead of `finishRestore`) keeps sessions, API
  tokens and agents, records the old manager's address on the arrived
  move (the code stays sealed there until the confirmation), records
  `system.move`, and calls the old manager's confirm in the background:
  retried with backoff (5 s to 5 min) on network errors, 5xx, 429 and
  403 `insecure_origin`; it stops at 200 (confirmed) and at 401 or 409
  (refused: the old manager must no longer run the instance). Attempts,
  the last error class and the confirmation time show on
  `GET /api/v1/manager/move`.

## Two managers never control the same agents

- The old manager refuses agents from `handed_off` on, before the copy
  leaves it.
- **Generation**: the instance row carries `generation`, raised by one in
  every handed-off copy. The `welcome` and `manager.identity` send it;
  the agent keeps the highest generation it has seen
  (`<state>/manager.json`) and closes the session of a manager with a
  lower one with 4421 (then retries with backoff, keeping its
  credential). A resurrected old manager, or an old backup
  restored somewhere, can never command an agent that met the new one.

## Finish checklist (new manager)

`GET /api/v1/manager/move` answers the arrived move: from where, when,
whether the old manager confirmed, and the environments to act on:

- the new server's environment is not there yet (no environment was
  created after the arrival, `newEnvironmentAdded`): the checklist offers
  the co-located install command (**Add environment**);
- environments whose agent last dialed a manager address other than the
  public URL (capabilities `transport.managerUrl`, e.g.
  `http://docker-manager:8080`): they reach the locked old manager and
  need `DOCKER_AGENT_MANAGER_URL` set to the public URL;
- those environments are the old server's: still holding stacks,
  **Migrate environment** (action `migrate`); once empty, archive
  (`archive`). Each listed environment carries `set_manager_url` first.

## Security

- The move code is a secret: shown once, never logged, stored as a
  verifier; one hour; single move. The secret key only leaves the old
  manager sealed under it, and only over a secure origin.
- Only the owner creates or cancels a move; the new manager accepts a
  move only while its setup is open.
- Audit: `manager.move.create`, `manager.move.cancel`, `manager.move.handoff`
  (with the requesting address), `manager.move.confirm` on the old
  manager; `system.move` on the new one.

## Routes

| Route | Capability | Notes |
| --- | --- | --- |
| `POST /api/v1/manager/moves` (`create-manager-move`) | owner (`manager.move`), step-up | 201: the move and the code (once); audited `manager.move.create` |
| `GET /api/v1/manager/move` (`get-manager-move`) | owner | the open or in-progress move (`jobsRunning` while draining), else the arrived move with `checklist`; 404 none |
| `POST /api/v1/manager/move/cancellations` (`create-manager-move-cancellation`) | owner, step-up | body `resumeHere`, `instanceName`; audited `manager.move.cancel` |
| `POST /api/v1/manager/move/handoff` (`create-manager-move-handoff`) | public, move code | the tar stream; audited `manager.move.handoff` |
| `POST /api/v1/manager/move/confirm` (`create-manager-move-confirmation`) | public, move code | audited `manager.move.confirm` |
| `POST /api/v1/setup/move` (`create-setup-move`) | public, setup open | 202 + `manager.receive` |

The owner routes check `manager.move` (owner-only catalog key) before
availability. API tokens never reach them. The move code routes read the
bearer themselves: the identity middleware leaves a `dmm_` bearer
anonymous (never an API token, never cookie-authenticated).

## Implementation

| Piece | Where |
| --- | --- |
| Move rows, generation | `manager_moves`, `instance.generation` (`store/managermoves.go`, `store/instance.go`); `domain.ManagerMove` |
| Move codes | `authsep.MintMoveCode` / `ParseMoveCode` (`dmm_<id>_<secret>`, SHA-256 verifier) |
| Lock | `internal/manager/movelock` (levels open, read-only, agents refused), set by `managermove` from the current move and at start (`managermove.LockLevel`); consulted by `api.Register` (`moveGuard`, allowlist `allowedWhileMoved`), `jobs.Engine` (`Enqueue`, `DispatchPending`), `scheduler.Service` (`Tick`) and `agents` (handler 503, hub 1012) |
| Handoff, confirm, cancel, expiry | `internal/manager/managermove` (`service.go`, `handoff.go`, `package.go`, `seal.go`); `Run` expires moves and confirms arrivals |
| Receive | `managermove/receive.go` (`manager.receive`, `jobspec.ManagerReceive`, lock `manager` X on the target `manager:instance`) |
| Arrival | `app.(*Manager).finishMove` → `managermove.FinishArrival`, `StartConfirming` |
| Checklist | `managermove/checklist.go` |
