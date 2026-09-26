# Scheduler (#13)

Every user-configurable scheduled job — backups and repository
verification (#10), image update checks and runs (#20), Docker prune (#14)
and any later scheduled kind — uses one cron parser and one runner. The
scheduler only decides **when** a policy's run is due; the job engine
([job-engine.md](job-engine.md)) executes, locks, serializes and recovers
the jobs it enqueues. Internal service intervals (heartbeats, metrics
sampling, retention purges) are not user schedules and do not use it.

| Package | Role |
| --- | --- |
| `internal/cron` | The parser and next-run calculator (five fields, IANA zones, DST rules). |
| `internal/manager/scheduler` | Schedule kinds and defaults, `PolicySource` registration, durable schedule state, the runner, previews. |
| `internal/manager/store` (`schedules.go`) | Rows of `schedule_settings`, `schedule_default_crons`, `schedules`, `schedule_runs`, `schedule_run_jobs` (migration `20260925214126_create_schedules`). |
| `internal/manager/api` (`schedules.go`) | `GET/PATCH /schedule-defaults`, `POST /schedules/previews`, `GET /schedules`, `api.ValidateSchedule`. |

## Expressions

Standard five fields: `minute hour day-of-month month day-of-week`. Each
field is a comma list of `*`, `N`, `N-M`, optionally with `/step` (`*/15`,
`8-18/2`, `5/10` = 5 to the maximum in steps of 10). Months accept
`JAN`–`DEC`, days of week `SUN`–`SAT` (case-insensitive); day of week `7` is
Sunday like `0`. When both day-of-month and day-of-week are restricted
(neither starts with `*`) a day matches if **either** matches (Vixie cron);
otherwise both must. Not supported: macros (`@daily`), seconds, years,
`L`, `W`, `#`, `?`, wrap-around ranges (`22-2`). An expression that can
never run (`0 0 30 2 *`) is invalid. Validation errors name the field
(`minute`, `hour`, `day-of-month`, `month`, `day-of-week`, `timeZone` or the
whole expression) and explain the problem in plain language.

Every schedule has an **explicit IANA time zone** (`Europe/Berlin`, `UTC`;
never "local"). The manager embeds the zone database (`time/tzdata`), so
evaluation does not depend on the image.

**Decision: in-house parser, not robfig/cron.** `robfig/cron/v3` evaluates
schedules by stepping wall-clock fields in the zone: it skips runs whose
local time falls into a DST gap and can run a repeated local time twice
after clocks fall back. Docker Manager promises the two rules below, so
`internal/cron` computes candidate *civil* (wall-clock) minutes without
zone arithmetic and maps each to an instant explicitly. It is ~300 lines,
has no dependency and is covered by unit tests (the former fuzz target
`FuzzParse` was removed on 2026-09-25).

### Daylight saving time

- **Skipped local time** (clocks jump forward, e.g. 02:30 on the US spring
  transition): the run fires **once, at the first instant after the gap**
  (03:00 EDT). Several skipped times of one schedule (`*/15` between 02:00
  and 02:59) collapse into that single run.
- **Repeated local time** (clocks fall back, e.g. 01:30 twice): the run
  fires **once, at the first occurrence** (01:30 EDT). The repeated hour's
  second pass has no runs, also for interval schedules (`*/30` runs at
  01:00 and 01:30 EDT, then 02:00 EST).
- Previews annotate such runs (`dst: gap | repeated` with a note), so users
  see the effect before enabling a policy.

Tests: `internal/cron` `TestDSTNewYork`, `TestDSTBerlin`,
`TestDailyRunsOncePerLocalDay` (8 zones including midnight and 30-minute
transitions, three years: exactly one run per local day, strictly
increasing); `internal/manager/scheduler` `TestDSTTransitionsEnqueueOnce`
(one job per day across both transitions in America/New_York and
Europe/Berlin while the runner evaluates every 5 minutes).

## Kinds and defaults

| Kind | Suggested default | Missed runs | Policy type / read capability | Job kinds |
| --- | --- | --- | --- | --- |
| `backup` | `0 * * * *` (hourly) | catch up once | `backup_policy` / `backup_policy.read` | `backup.run`, `manager.backup` |
| `update_check` | `0 3 * * *` | catch up once | `update_policy` / `update_policy.read` | `update.check` |
| `update_run` | `0 4 * * *` | skip | `update_policy` / `update_policy.read` | `update.run` |
| `prune` | `0 3 * * 0` | skip | `maintenance_policy` / `maintenance_policy.read` | `prune.run` |
| `backup_verification` | `0 5 * * 0` | catch up once | `backup_repository` / `backup_repository.read` | `backup.verify` |

The instance settings hold one editable default expression per kind and a
default time zone (`UTC` until changed): `GET/PATCH
/api/v1/schedule-defaults` (`settings.read` / `settings.manage`, If-Match).
Defaults are **suggestions that prefill new policies only**: every policy
stores its own expression, zone and enabled flag (the per-policy
override), and changing a default never changes an existing policy. There
is no "apply to existing policies" action in v1. All automatic and
destructive policies start disabled (#13, #14): a shipped default is never
silent authorization.

The owning workstream may adjust its kind's row (catch-up policy, policy
type, job kinds) in `BuiltinKinds` when it lands; later kinds are added
there or with `RegisterKind`.

## Durable state

- `schedules` — one row per policy schedule (`kind`, `policy_id` unique):
  name, environment, expression, zone, enabled, `invalid_reason`,
  **cursor** (the last processed instant, or when the schedule was created,
  enabled or edited) and **next run** (persisted, recomputed from the
  cursor).
- `schedule_runs` — one row per processed instant: `scheduled_for`,
  idempotency key `<kind>:<policy>:<instant RFC 3339 UTC>` (unique, as is
  `(schedule_id, scheduled_for)`), outcome, catch-up flag, missed count and
  first missed instant, reason and error class. The newest 100 per schedule
  are kept.
- `schedule_run_jobs` — the jobs a run enqueued, with their final state and
  error class (recorded by a finish hook in the job's terminal transaction,
  so the history survives job retention). While a job exists its live state
  (and blocked reason) is shown.

Run outcomes: `pending` (reserved, being enqueued), `enqueued` (its jobs'
states are the result), `missed`, `skipped` (previous run still active,
or nothing to run), `rejected` (the policy's revalidation refused it),
`failed` (the job could not be enqueued). Runs that did not enqueue a job
are audited as `schedule.run_<outcome>` by the service actor (#30); jobs
are audited by the engine.

## The runner

`Service.Run` loops: one pass (`Tick`), then it sleeps until the earliest
next run — **exactly** that long when it is sooner than one minute
(`MaxSleep`), otherwise one minute — or until `Notify`. The minute bound
catches wall-clock jumps (NTP steps, suspend/resume: timers run on the
monotonic clock) and policy edits made without `Notify`. All time comes
from the injectable clock; tests drive it with `clock.Fake`.

A pass:

1. **Synchronize** each kind that has a source: `Schedules()` is compared
   with the table. New schedules and changes of expression, zone or enabled
   flag reset the cursor to now (the time before an edit or while disabled
   is never "missed"); renames keep it; a policy missing from the list is
   deleted with its history. A failing source leaves its schedules
   untouched. An invalid expression or zone is stored as `invalid_reason`
   and never runs.
2. **Advance** each enabled schedule whose next run is due: all instants in
   (cursor, now] are computed. The latest is **on time** when it is at most
   5 minutes (`Grace`) old. Recorded in one transaction with the new cursor
   and next run:
   - on time → one `pending` run; earlier instants (downtime) → one
     `missed` row summarizing them;
   - late, kind catches up → **one** catch-up run for the latest instant
     (`catchUp`, reason, missed count) and one `missed` row for the rest;
   - late, kind skips → one `missed` row for all of them, no run.

   Never more than one run per schedule per pass: no storm after downtime.
3. **Enqueue** each `pending` run:
   1. jobs already enqueued under `<run key>#<n>` (a crash after enqueueing)
      are linked, never enqueued again;
   2. a stale pending run (older than `Grace`, nothing enqueued: the manager
      stopped in between) becomes `missed` when a later run exists or the
      kind skips missed runs;
   3. **overlap**: a non-terminal job of the policy (`policy_id` and the
      kind's job kinds, manual runs included) → `skipped: the previous run
      is still active (job …)`;
   4. `Validate(policyID)` → `rejected` with the source's reason;
   5. `Jobs(due)` → each request is enqueued with `Principal:
      authz.Service()` (origin `scheduled`, no initiator), the policy ID and
      the idempotency key `<run key>#<index>`; the run becomes `enqueued`.

**Restarts never duplicate:** the instant is recorded (unique) before its
jobs exist, and the jobs carry deterministic idempotency keys, so every
crash point resolves to exactly one run: before the row (the next pass
records it), between row and jobs (pending → enqueued once), between jobs
and link (linked by key). **Clock moved backwards:** a cursor in the future
is reset to now; instants already processed are refused by the unique run
rows, so nothing runs twice and a wrong future cursor cannot stall a
schedule. **Clock moved forward:** handled like downtime.

**Execution belongs to the job engine:** locks (a scheduled prune waits
for a deploy on the same resources), per-host caps, offline agents (the job
waits `blocked`/`agent_offline`, then fails after the kind's offline
deadline — the schedule history shows both), cancellation and crash
recovery. At **dispatch** the engine calls the scheduler's
`ScheduledCheck` for every queued scheduled job, which calls the policy's
`Validate` again: a disabled or deleted policy or a vanished target fails
the job with class `policy_rejected` before anything is sent (#17: policy
state and target scope are revalidated when dispatched).

**Service identity:** scheduled jobs never use the account or session that
created the policy; they continue when that account is disabled or deleted
(`TestScheduleContinuesAfterCreatorIsGone`, with an engine that denies
every user).

## API

- `GET /api/v1/schedule-defaults` (`settings.read`), `PATCH` (`settings.manage`,
  If-Match): `{timeZone, crons: {<kind>: <expr>}}`; invalid values are 422
  with `body.timeZone` / `body.crons.<kind>` details.
- `POST /api/v1/schedules/previews` (any signed-in caller): `{cron,
  timeZone?, kind?, from?, count? (≤ 50)}` → normalized expression, next
  runs (`at` with offset, `utc`, `local`, `dst`, `dstNote`) and notes on DST,
  missed runs (per kind), restarts, overlaps, offline environments and the
  service identity. Nothing is stored. Invalid input is 422 with `body.cron`
  / `body.timeZone` details.
- `GET /api/v1/schedules` (`schedule.read`): the cross-policy view with
  next run and the 10 newest runs (outcome, result, reason, jobs). Filters
  `kind`, `environmentId`, `enabled`; ID order; no total. An entry is listed
  only when the caller holds `schedule.read` where the policy lives
  (environment or instance) **and** the kind's read capability on the
  policy.
- Policy CRUD (#10, #14, #20) owns each policy's expression, zone and
  enabled flag; manual runs go through the policy's own run route (with the
  policy ID on the job, so overlap prevention sees them) and do not change
  the schedule. Job history is `/api/v1/jobs` (filter by `kind` / target).

## For policy workstreams (#10, #14, #20)

```go
sched := app.Manager.Scheduler() // *scheduler.Service

// At startup (before Serve), once per kind:
sched.Register(scheduler.KindPrune, pruneSource) // a scheduler.PolicySource

type PolicySource interface {
	// Every policy of the kind that has a schedule, enabled or not.
	Schedules(ctx context.Context) ([]scheduler.PolicySchedule, error)
	// Revalidate when due and at dispatch; scheduler.Reject(class, reason)
	// refuses the run, any other error is a source failure.
	Validate(ctx context.Context, policyID string) error
	// The run's jobs (1..32, deterministic order); Reject(...) refuses it,
	// none skips it. Principal, PolicyID and IdempotencyKey are set by the
	// scheduler. May be called again for the same due.Key after a crash:
	// keep per-run bookkeeping idempotent on due.Key.
	Jobs(ctx context.Context, due scheduler.Due) ([]jobs.Request, error)
}

type PolicySchedule struct {
	PolicyID, Name, EnvironmentID, Cron, TimeZone string
	Enabled                                       bool
}
```

- **Create forms / requests:** prefill with `cron, tz, _ :=
  sched.Default(ctx, scheduler.KindPrune)`; store the policy's own `cron`,
  `timeZone` and `enabled` (default `false` for automatic/destructive
  policies). Validate input with `api.ValidateSchedule(cron, tz,
  "body.schedule")` (422 with `<prefix>.cron` / `<prefix>.timeZone`) or
  `scheduler.ValidateSpec`.
- **After every policy change** (create, edit, enable, disable, delete)
  call `sched.Notify()` after the commit; changes are also picked up within
  a minute without it.
- **Policy DTOs:** `sched.Status(ctx, kind, policyID, n)` returns the
  schedule (next run, invalid reason) and the n newest runs;
  `sched.NextRun(schedule)` adds the local time and DST annotation.
- **Manual runs:** enqueue with the caller's principal and `PolicyID` set;
  they count for overlap prevention and never touch the schedule.
- Keep the policy's read capability and resource type in the kind's row of
  `BuiltinKinds` accurate: `GET /schedules` filters entries with it.
