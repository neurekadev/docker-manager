# Scheduled policies (#13)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/scheduler.md`. One parser (`internal/cron`: five
fields, explicit IANA zone, DST gap → first instant after it, repeated
time → first occurrence) and one runner (`internal/manager/scheduler`,
`app.Manager.Scheduler()`); never parse cron or run timers for user
schedules elsewhere.

- **Policy owners (#10, #14, #20)** `Register(kind, src)` a
  `scheduler.PolicySource` (`Schedules` lists every policy's saved
  cron/zone/enabled; `Validate` revalidates when due and at dispatch,
  `scheduler.Reject(class, reason)` refuses; `Jobs(due)` returns the run's
  `jobs.Request`s — the scheduler sets the service principal, policy ID and
  idempotency key `<due.Key>#<n>`). Call `Notify()` after policy changes.
- New policies take `Default(ctx, kind)` (editable instance defaults) and
  store their own cron, zone and enabled flag (automatic/destructive ones
  start disabled). Validate with `api.ValidateSchedule(cron, tz,
  "body.schedule")`. Policy DTOs use `Status(ctx, kind, id, n)` and
  `NextRun(schedule)`.
- Scheduled jobs run as `authz.Service()` (origin `scheduled`); manual runs
  of a policy set `jobs.Request.PolicyID` so overlap prevention sees them.
- Schedule kinds live in `scheduler.BuiltinKinds` (default expression,
  catch-up policy, policy type + read capability for `GET /schedules`,
  job kinds for overlap); keep your kind's row accurate. `Label` is the
  Title Case name the UI shows ("Image Update Checks"); `Noun` is its
  sentence-case form for run reasons ("Image update checks"), needed
  whenever it differs from `Label`.
