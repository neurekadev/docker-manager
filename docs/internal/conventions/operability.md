# Operability (#34)

Binding conventions (split out of AGENTS.md). Read this file when your change touches this area.

Guides: `docs/internal/operations/upgrades.md`, `docs/internal/operations/removing-hosts.md`,
`docs/internal/operations/diagnostics.md`. Removal preview `internal/manager/removal`;
metrics and support bundle `internal/manager/diagnostics`; archive hook and
wiring `internal/manager/app/operability.go`.

- **Version window:** `protocol.CheckAgentVersion` refuses (4426/426),
  `protocol.AgentCompatibility` flags stored agents for the API
  (`compatibility`, `upgradeInstructions`). New envelope/payload fields
  reach an agent only after it announces a capabilities feature (see
  `protocol.FeatureRequestID`): receivers reject unknown fields, so an N-1
  agent must never see a field it predates.
- **Environment removal:** records that belong to an environment must show
  up in `removal.Preview` (add a `domain.Dependent*` kind) and survive an
  archive; policy sources refuse scheduled runs for archived environments
  (`scheduler.Reject("environment_archived", …)`, backups skip them);
  the job engine refuses user/API-token jobs there
  (`domain.ErrEnvironmentArchived` → 409). Never touch the host on archive.
- **Request IDs:** the job engine stores the request's ID on the job and
  command frames carry it; agent code logs through
  `logging.FromContext(ctx)` in request/stream handlers and job steps to get
  `request_id` for free.
- **Diagnostics:** anything new in the support bundle is an allowlisted
  field, never a struct dump; the canary test
  (`TestSupportBundleHasNoSecrets`) must keep passing. Metrics labels are
  enumerations only. Capabilities: `system.metrics.read` (delegable,
  endpoint off unless `DOCKER_MANAGER_METRICS_ENABLED`), `system.support_bundle`
  (owner only).
- **Rollback:** `docker-manager snapshots list|restore` restores a
  pre-migration snapshot (`store.RestoreSnapshot`); downgrades stay
  unsupported.
