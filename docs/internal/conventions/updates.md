# Digest-driven updates (#20, #240)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/updates.md`. Manager `internal/manager/updates`
(`app.Manager.Updates()`; eligibility rules `updates/eligible`, shared with
the stack image status); agent `update.run` executor in
`internal/agent/stacks/update.go`; lifecycle `lifecycle.Update`/`Confirm`;
payloads `internal/protocol/updates.go`.

- One updates setup for the instance (`update_settings`, one row): it
  covers every active environment except `excludeEnvironments`, also ones
  added later. Never add per-environment update configuration; the
  settings need instance grants (`update_policy.manage` is
  instance-only), and setup-wide checks, previews and runs are refused
  while a rule denies them in a covered environment.
- Available updates raise the record's `updates` alert from the
  `update.check` finish hook (`internal/manager/alerts`, #159): keep
  `CandidateAvailable` the one "available" predicate (the UI's
  `summary.available`) and the check input's `policyId`. Every finished
  `update.run` records an `updates` notification from its result output
  (`UpdateRunOutput.Services`, never the input's container
  specification): keep the outcome names stable. Both link the Updates
  page and name the setup ("Automatic Updates", from the record's
  `ParentID`), never the record (it has no page).
- One record per target (stack or Docker Manager-managed standalone
  container); the setup's check and run schedules (#13 kinds
  `update_check`/`update_run`) start disabled. Never add an automatic path
  that pulls or recreates without an enabled schedule or an explicit user
  run.
- Compare **host-platform manifest digests** (`registries.Service.Check`),
  never tag text or creation time; an index change alone is no update.
  The candidate's creation time (`publishedAt`, `registries.Service.Created`)
  is display only: read once per new digest, and its absence never fails a
  check.
- Target records are named after their target ("Automatic updates for
  zerobyte"), never after an ID, and the reconciliation keeps names,
  activity and schedules in step. Inactive records keep their history but
  are not listed by `GET /update-policies`; the setup's targets report why
  (`inactiveReason`).
  Only candidates whose last check succeeded are run (no repeated pulls on
  401/403/429).
- Never write Compose/override/env files: the run asserts the applied
  revision's hash before, during and after (`source_changed`) and refuses
  undeployed edits (`update_source_drift`).
- No automatic rollback: failures after containers were touched set
  `UpdateRunOutput.Quarantine`; the finish hook quarantines the digest and
  audits `update.quarantine`.
- Stack members keep their prior state through `lifecycle.Update` (stopped
  services stay stopped, `restart: true` dependents restart); a prune
  policy (#14) should treat images left unused by an update as ordinary
  candidates.
