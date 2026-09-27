# Audit (#30)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

- Guide: `docs/internal/architecture/audit.md`. Package `internal/manager/audit`.
- HTTP operations and job lifecycles (`job.queued/started/cancel_requested/
  finished`) are recorded by construction; never record them by hand.
- Other events (sign-in failures outside a route, lockouts, agent
  enrollment/rotation/revocation on `/agent/v1`, registry/Git credential
  use, owner-recovery CLI, scheduled work) call
  `audit.Record(ctx, domain.AuditEvent{Action: "agent.enroll", Actor:
  audit.AgentActor(id), Targets: ..., Outcome: ...})`; inside a DB
  transaction use `(*audit.Log).RecordTx(ctx, tx, ev)` (never `Record`: the
  single SQLite connection would deadlock). Scheduled work uses
  `audit.ServiceActor()`.
- Never pass secret values, tokens, file contents, `.env` values or error
  messages; the redaction layer is a safety net, not a licence. Record error
  classes (stable codes), not messages.
- Detail keys containing secret words (`token`, `secret`, `key`, ...) are
  redacted unless they end in a metadata suffix (`Id`, `Ids`, `Name`,
  `Count`, `Type`, `At`, ...): name counts `apiTokenCount`, not
  `apiTokensRevoked` (`audit.SensitiveKey`).
- The trail is append-only (DB triggers); there is no update/delete API.
  Diagnostics (#34) call `(*audit.Log).Verify(ctx)`.
- `audit.read`/`audit.export` are instance-scoped, high-risk catalog
  capabilities (no group holds them until the owner grants them).
