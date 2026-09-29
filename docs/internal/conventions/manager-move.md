# Moving the manager (manager move)

Binding conventions (split out of AGENTS.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/manager-move.md`. Packages:
`internal/manager/managermove` (the move, `manager.move`, the signed and
encrypted handoff, redirects, waiting mode, arrival, Move complete) and
`internal/manager/movelock` (the lock). The new server's files are
rendered next to the install commands (`agents.MoveFiles`, the policy
check's documented socket exception).

- **The move code is a secret** (`dmm_<id>_<secret>`,
  `authsep.MintMoveCode`): it appears once, inside the `.env` that
  `create-manager-move` returns, and in the new server's
  `DOCKER_MANAGER_MOVE_CODE` (`config.MoveConfig.Code`, a
  `logging.Secret`). The database keeps it only sealed with the secret key
  (`manager_moves.sealed_code`, `managermove.SealContext`); an ended move
  forgets it. Never log, audit, return, put it in job inputs or error
  messages. The enrollment token in the same `.env` follows the agents'
  rules. New setup files (`create-manager-move-setup-files`,
  `NewSetupFiles`, open or ready only) are the one other answer that
  carries them: a new code of the same move replaces the sealed one, and
  a new token unless the new server's agent already enrolled (then its
  environment is kept and the `.env` has no token). Anything that accepts
  a signed request and changes the move compares its code with the
  current one under the service lock (`sameCode`).
- **The code never travels.** Move requests carry `Authorization: DMM
  <id>:<unix time>:<nonce>:<mac>` (`managermove.SignRequest`; HMAC-SHA256
  under HKDF-SHA256(secret, `docker-manager-move/auth/v1`) over method,
  path, time, nonce and move ID); the old manager checks the MAC first,
  then ±5 minutes, then the nonce once (in-memory replay cache). The
  handoff stream is encrypted (`crypt.go`, XChaCha20-Poly1305 STREAM under
  HKDF-SHA256(secret, `docker-manager-move/package/v1\x00<moveId>`)), so
  both sides accept plain HTTP; never add a code-in-header or a
  secure-origin shortcut. The identity middleware leaves a `DMM` request
  anonymous without cookies (`authsep.IsMoveAuthorization`); only the
  check-in, handoff and confirmation routes read it (`moveAuthMiddleware`).
  The waiting manager polls the unaudited check-in (a GET) and calls the
  audited handoff only once the move is ready: never poll a mutating
  route.
- **One lock, one mechanism:** anything that changes state while the
  manager moves consults `movelock.Lock` (nil-safe) and never reads
  `manager_moves` itself. Read-only: `api.Register`'s `moveGuard` (every
  non-GET operation except `allowedWhileMoved`: sign-in, sign-out, the
  move routes; a new operation a moved manager must still serve goes in
  that list), `jobs.Engine.Enqueue` (`jobs.ErrManagerMoved`) and
  `DispatchPending`, `scheduler.Service.Tick`. Agents refused: the agent
  handler (503 + `Retry-After: 60`, before any credential check) and the
  hub (1012, also for sessions racing the lock). Waiting (a new manager
  before the handoff): everything above, and `waitingGuard` closes every
  operation except `allowedWhileWaiting` (health, capabilities,
  `get-move-status`) with 503 `manager_move_waiting`. Never answer agents
  with 401, 4401, 4403 or 4409 for a move. A new background writer or job
  source must respect the lock too.
- The lock follows the current move only through the move service
  (`LevelFor`, `LockLevel` at start); waiting mode is decided once at
  start (`app.decideWaiting`: both variables, no owner, no environment).
  Never set it elsewhere.
- **The apps move first, as an environment migration:** `manager.move`
  calls `migrations.Service.StartEnvironment` with the owner's principal
  and waits for it; it never moves data itself and never bypasses the
  migration's rules. The move becomes `ready` only from `manager.move`'s
  last step; the handoff answers `manager_move_not_ready` before. A failed
  run puts the move back to `open` in the job's finish hook (the hook
  writes only through the hook's `db`, never the service lock).
- **Redirects before the refusal:** the handoff sends `manager.redirect`
  (generation + 1) only to the agents it can place (the new server's
  environment, the one next to the manager), once, best effort
  (`RedirectTimeout`), records the outcome on the move (it travels in the
  copy) and only then refuses agents. Resuming (or ending) a move after a
  redirect or a handoff raises the instance generation by two and restarts
  the manager (`endMove`); keep that rule for any new way to end a move.
- **The copy, not the live database, changes:** the handoff raises the
  instance generation and marks the move `arrived` in the `VACUUM INTO`
  copy only (`prepareCopy`); the package directory appears complete or not
  at all, and a repeated handoff streams the same package.
- The receiver trusts nothing before the stream decrypted, the manifest
  matched (fixed part names, size limits, SHA-256) and the copy passed
  `verifyPackage` (sealed key, integrity, instance and generation, known
  schema, sealed settings, arrived row). No redirects are followed.
- The copy is applied only through `restore-pending` (`backups.StageRestore`,
  marker kind `move`) and a controlled restart; `finishMove` (not
  `finishRestore`) keeps sessions, API tokens and agents. Data that must
  not survive a move is removed there, in `FinishArrival`.
- The generation reaches agents in `welcome` and `manager.identity`
  (`agents.Options.Generation`); any other manager-issued identity must
  carry it too. The environment next to the manager is learned from the
  `manager.identity` answer (`ObserveColocation`), never guessed.
- "Move complete" decides "needs a fix" from the redirects that were not
  sent and the agent not connecting since the arrival, never from the
  address an agent reports.
- **Live, not polled:** every change of a move publishes
  `manager_move.updated` (owner) and, when the session's lock changes,
  `manager_move.lock_changed` (everyone, no ID) through
  `managermove/live.go` (`published`); a new transition or field the
  view shows publishes too, and what changes elsewhere is followed on the
  bus (`followBus`). Never add polling of `GET /manager/move`; only the
  new server's status page polls (no stream without a sign-in).
- **Web UI** (`web/src/lib/features/managermove`, `docs/internal/web.md`):
  the new server's `.env` (and the one-command setup built from it,
  `setupScript`) is shown once and kept only in the page's memory; lost
  or expired files are replaced with "Create new setup files", never by
  cancelling; a locked manager shows the shell's `MoveBanner` (the owner
  reads the move, everyone the session's lock, both refreshed live, and
  the first 409 `manager_moved`, heard through `onApiFailure`), never a
  toast; the new manager's status page is built from `GET
  /api/v1/move/status` alone and the app reads that route before any
  sign-in or setup routing (`MoveGate`, which starts the live stream only
  when not waiting); an action that restarts the manager waits for it
  (`waitForRestart`) and reloads, never asks the user to reload; the
  check step reuses the environment migration's check
  (`EnvironmentMigrationCheck`), never a copy of it; new move states get
  their words in `model.ts` first.
- Tests: `managermove` fixtures (fakes of the agents, hub and migration
  services; a plain-HTTP `httptest` old manager for waiting mode), fake
  clocks for expiry, retries and polls; the lock is tested where it is
  consulted (`api`, `jobs`, `scheduler`, `agents`).
