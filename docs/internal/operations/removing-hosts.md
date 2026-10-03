# Removing agents and hosts (#34)

One active agent per Docker Engine (#3). An **environment** is that agent
plus its Engine. Removing never stops, removes or edits anything on the
host: containers, volumes, networks and stack directories stay as they are.

## Remove an agent

`DELETE /api/v1/agents/{agentId}` (`agent.remove`) revokes the agent's
credential at once (a live session closes with `4403`; the agent stops
reconnecting and asks for a new enrollment). Its environment stays **active
but detached**: offline, no agent. Enroll a new agent for the same Engine
with an enrollment of intent `reattach:<environmentId>` to attach it again,
or `replace:<agentId>` while the old agent is still active.

## Remove an environment

1. **Preview** — `POST /api/v1/environments/{environmentId}/removal-previews`
   (`environment.remove`) lists every record that depends on the host, each
   with what removal does to it:

   | kind | on archive |
   | --- | --- |
   | `stack`, `managed_container` (saved recreate specification) | kept, hidden with the host |
   | `schedule` | kept; scheduled runs are refused (`environment_archived`) or skip this host until it is re-attached |
   | `backup_repository` (holding its backups), `backup_set` | kept; snapshots stay restorable after a re-attach (#10, #24) |
   | `registry_connection` bound to the host or its stacks, `build_definition` (Git binding) | kept |
   | `permission_rule` scoped to the host (environment-scoped rules and rules on its containers, images, volumes and networks) | **removed**, audited as `environment.permission_rules_remove` with the removed rules |
   | `job` not finished | interrupted: the agent is disconnected and the job ends by the offline rules (#26) |

   Items the caller cannot see are left out (#17); permission rules are
   listed to the owner only. The preview also offers **migrating** the
   host's stacks and volumes first (#35: every stack at once with `POST
   /api/v1/environments/{environmentId}/migration-previews`, `…/migrations`,
   one stack with `POST /api/v1/stacks/{stackId}/migration-previews`,
   `…/migrations`, and the volume equivalents) so they keep running under
   Docker Manager elsewhere.

2. **Archive** — `DELETE /api/v1/environments/{environmentId}` with
   `If-Match` (`environment.remove`, the only removal in v1). The host
   becomes hidden from operations: it is listed only with
   `?status=archived`, edits and new jobs answer `409 environment_archived`,
   its agent's credential is revoked (session closed with `4403`) and the
   permission rules scoped to it are removed in the same transaction (the
   affected users' streams end). History, audit records, stacks, policies,
   backup repositories, sets and snapshots are kept. Rules on stacks and
   policies (Docker Manager IDs) stay with those records.

## Re-attach an archived environment

Enroll an agent for the **same Docker Engine** with an enrollment of intent
`reattach:<environmentId>`, created by the owner (the intent is the owner's
confirmation, #25):

```bash
docker compose exec -T docker-manager docker-manager enrollment create -intent reattach:<environmentId>
printf '%s\n' "$TOKEN" | docker compose exec -T docker-agent docker-agent enroll
```

(or `POST /api/v1/agent-enrollments {"intent": "reattach:<environmentId>"}`).
The environment becomes active again with the same ID and name, its
stacks, policies and backups resume, and scheduled runs start again at
their next time. Re-grant the removed environment-scoped rules if you
still want them. This is also the reconnect path after a disaster recovery
(#24): a restored manager revokes every agent credential and each host is
re-attached this way.

Enrolling an archived environment's Engine with intent `new` is refused
(`environment_archived`); an Engine that now belongs to another
environment cannot be re-attached (`engine_mismatch`).

Tests: `internal/manager/removal` `TestPreviewListsEveryDependentKind`,
`TestEnvironmentRulesRemoval`; `internal/manager/app`
`TestArchiveAndReattachThroughTheManager`; `internal/manager/agents`
`TestArchiveAndReattach`, `TestRemoveAgentDetachesEnvironment`.
