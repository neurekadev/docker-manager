# Self-protection (#32)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/self-protection.md`. Docker Manager's own containers,
images, volumes, networks and Compose project are protected for everyone
(owner and API tokens included): `internal/protection` decides
(`Check`, `Excluded`, `Filter`), `internal/agent/protect` identifies (agent),
`resources.Service.ContainerProtection` / `ProjectProtection` /
`ProtectedContainers` answer on the manager.

- Every destructive feature checks both sides: the manager before a job
  exists, the agent executor again right before acting
  (`protection.Refusal` is a `jobexec.ClassedError`).
- Bulk features (prune #14, updates #20, backup/restore shutdown plans #10,
  bulk selections and migrations #35) drop protected objects with
  `protection.Filter` and show the reason; stack down/stop/restart/remove
  (#7) refuse Docker Manager's own project (deploys hand the agent's own
  service to the self-update helper; an import by copy copies the project
  while it runs and never stops it).
- No override flag: only the co-located manager's restart takes
  `confirm: true`.
