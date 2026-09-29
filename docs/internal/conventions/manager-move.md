# Moving the manager (manager move)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/manager-move.md`. Packages:
`internal/manager/managermove` (moves, handoff, `manager.receive`,
arrival, checklist) and `internal/manager/movelock` (the lock).

- **The move code is a secret** (`dmm_<id>_<secret>`,
  `authsep.MintMoveCode`): returned once by `create-manager-move`, stored
  only as its SHA-256 verifier (the new manager keeps it sealed with the
  secret key, `managermove.SealContext`, until the old one confirmed).
  Never log, audit, return, put it in job inputs or error messages;
  `manager.receive` keeps it in memory only. The identity middleware
  leaves a `dmm_` bearer anonymous; only the handoff and confirmation
  routes read it (`moveCodeMiddleware`).
- **One lock, one mechanism:** anything that changes state while the
  manager moves consults `movelock.Lock` (nil-safe) and never reads
  `manager_moves` itself. Read-only: `api.Register`'s `moveGuard` (every
  non-GET operation except `allowedWhileMoved`: sign-in, sign-out, the
  move routes; a new operation a moved manager must still serve goes in
  that list), `jobs.Engine.Enqueue` (`jobs.ErrManagerMoved`) and
  `DispatchPending`, `scheduler.Service.Tick`. Agents refused: the agent
  handler (503 + `Retry-After: 60`, before any credential check) and the
  hub (1012, also for sessions racing the lock). Never answer agents with
  401, 4401, 4403 or 4409 for a move. A new background writer or job
  source must respect the lock too.
- The lock follows the current move only through the move service
  (`LevelFor`, `LockLevel` at start); never set it elsewhere.
- **The copy, not the live database, changes:** the handoff raises the
  instance generation and marks the move `arrived` in the `VACUUM INTO`
  copy only (`prepareCopy`); the package directory appears complete or not
  at all, and a repeated handoff streams the same package.
- The receiver trusts nothing before the manifest matched (fixed part
  names, size limits, SHA-256) and the copy passed `verifyPackage`
  (sealed key, integrity, instance and generation, known schema, sealed
  settings, arrived row). Only `https` addresses, TLS verified, no
  redirects.
- The copy is applied only through `restore-pending` (`backups.StageRestore`,
  marker kind `move`) and a controlled restart; `finishMove` (not
  `finishRestore`) keeps sessions, API tokens and agents. Data that must
  not survive a move is removed there, in `FinishArrival`.
- The generation reaches agents in `welcome` and `manager.identity`
  (`agents.Options.Generation`); any other manager-issued identity must
  carry it too.
- Tests: `managermove` fixtures (two managers, an `httptest` TLS old
  manager), fake clocks for expiry and retries; the lock is tested where
  it is consulted (`api`, `jobs`, `scheduler`, `agents`).
