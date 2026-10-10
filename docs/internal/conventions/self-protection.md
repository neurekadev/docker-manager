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
  while it runs and never stops it). A stack export (#313) refuses it too
  (it would stop the manager and hold its data): at the check, the start and
  the job's `prepare`.
- The web lists' bulk selections (`$lib/features/resources/bulk.ts`) leave
  protected objects out before any request and list them with the reason
  in the confirmation and the summary; each remaining object goes through
  its own request, so the server's refusal still applies.
- `protect.Guard.Identify` decides from the container list alone and never
  inspects a container (#307): the Engine can block an inspection for
  minutes while it removes a container, and every listing identifies.
- No override flag: only the co-located manager's restart takes
  `confirm: true`.
