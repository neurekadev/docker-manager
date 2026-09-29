# Digest-driven updates (#20)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/updates.md`. Manager `internal/manager/updates`
(`app.Manager.Updates()`; eligibility rules `updates/eligible`, shared with
the stack image status); agent `update.run` executor in
`internal/agent/stacks/update.go`; lifecycle `lifecycle.Update`/`Confirm`;
payloads `internal/protocol/updates.go`.

- One policy per target (stack or Docker Manager-managed standalone container);
  check and run schedules (#13 kinds `update_check`/`update_run`) start
  disabled. Never add an automatic path that pulls or recreates without an
  enabled policy or an explicit user run.
- Compare **host-platform manifest digests** (`registries.Service.Check`),
  never tag text or creation time; an index change alone is no update.
  The candidate's creation time (`publishedAt`, `registries.Service.Created`)
  is display only: read once per new digest, and its absence never fails a
  check.
- Target records of environment policies are named after their target
  ("Automatic updates for zerobyte"), never after an ID, and the
  reconciliation keeps names, activity and schedules in step. Inactive
  records keep their history but are not listed by `GET /update-policies`;
  the policy's targets report why (`inactiveReason`).
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
