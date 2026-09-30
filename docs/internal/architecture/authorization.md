# Authorization (#17)

Docker Manager grants **named actions** (capabilities) at a **scope**. There are no
generic read/write/execute grants: `container.restart` does not imply start,
stop, logs, terminal, files or backups. The decision is made on the server for
every route, list item, count, stream event, job request and job dispatch; hidden
UI controls are never authorization.

| Package | Role |
| --- | --- |
| `internal/manager/authz/catalog` | The versioned capability catalog (keys, resource types, labels, scopes, risk, owner-only). |
| `internal/manager/authz/policy` | The deterministic evaluator (`Evaluate`), rule validation, `Checker` (resource graph, reachability), rule shorthand. |
| `internal/manager/authz` | Principals, `Resource`, `Authorizer`, per-request `Checker` (`For`), shaping (`ViewOf`), job targets (`JobResource`, `EvaluateJob`), event filtering (`EventVisible`). |
| `internal/manager/permissions` | The permission service: rule storage, the `Authorizer` of the manager, Locators, `ForgetResource`, owner-only management, effective permissions and previews. |
| `internal/manager/authz/authztest` | Test kit for feature workstreams (in-memory policy with the real evaluator, route matrix assertions). |
| `internal/manager/api/permissions.go` | The `/api/v1` contract: catalog, `me/permissions`, groups, rule documents, effective permissions, previews. |

ADR 0003 records why this is a small Go evaluator and not Casbin.

## Model

- **Groups.** Every user is in exactly one group; exactly one group is the
  **default** for new users (schema invariant, #16). The initial default is
  **Restricted** with no rules. Groups can be renamed (also the default),
  created (no rules), made default, and deleted when they are neither the
  default nor have members.
- **The owner's group.** The owner's account is in a group too
  (`users.group_id` is `NOT NULL`; setup puts it in the default group), but
  group rules never apply to it (owner bypass). So `memberCount` never
  counts the owner, and the owner never blocks deleting a group: when the
  owner is in the group being deleted, the account moves to the default
  group in the same transaction. Moving the owner with `PATCH
  /users/{userId}` stays possible and changes nothing about its access.
- **Group rules** allow or deny one capability at one scope. No matching rule
  means deny.
- **User rules** are overrides: allow or deny per capability and scope; a
  capability/scope without a user rule **inherits** the group. Removing the
  rule (an empty list resets everything) restores inheritance.
- **Scopes:** `instance` (all resources of the capability's type, including
  future ones), `environment` (all such resources in one environment) or
  `resource` (one resource of a type the capability supports). Docker-named
  resources (container, image, volume, network) are identified by
  environment + name; stacks, services (`<stackId>/<service>`), agents,
  policies, repositories and definitions by global IDs.
- **Duplicates are ambiguous** and rejected: one rule per subject, capability
  and scope (validation and the table's primary key), whatever the effects.
- The **owner** is a protected principal: allowed everything, never subject
  to rules (rules for the owner are refused with `owner_protected`). Owner-only
  surfaces (users, groups, invitations, security policy, registry/Git
  credential administration, token administration of others, manager backup
  and restore) are `OwnerOnly` catalog entries and `owner` routes; they are
  never grantable.

## Evaluation (`policy.Evaluate`)

1. Inactive (disabled or deleted) account → deny.
2. API token (#31) → the token scope must cover the request (token scope ∩
   effective permissions; the owner's tokens too). Tokens:
   [api-tokens.md](api-tokens.md).
3. Owner → allow.
4. Unknown or owner-only capability → deny.
5. The most specific matching **user** rule decides.
6. Otherwise the most specific matching **group** rule decides.
7. Otherwise deny.

Specificity within a tier: the exact resource > its parents (container >
service > stack) > environment > instance. A user rule beats every group rule,
even a more specific one. The decision carries the deciding rule and a
plain-language reason (previews, effective permissions).

- **Wildcards and future keys:** an instance or environment rule applies to
  resources created later, but rules name exactly one key, so a capability
  added to the catalog later is denied until granted.
- **Dynamic scope:** a rule scoped to a stack or service applies to that
  stack's current and future service containers **only for the capabilities
  it names** (`container.logs.read @stack:S`); `stack.read` alone opens no
  container details, logs or files.
- **Moved resources (#35):** stack (and service) rules follow the stack: its
  environment changes, its ID does not. Environment rules do not follow.
  Exact container/volume rules do not follow a migration (the target's
  resources are new). The migration preview shows the change:
  `permissions.Service.MoveImpact` evaluates every stack and container
  capability before and after the move for each user (the owner sees every
  affected user, others their own change; [migrations](migrations.md)).
- **Deleted resources:** rules on a resource that no longer exists match
  nothing. Feature services call `permissions.Service.ForgetResource` after
  deleting a resource through Docker Manager so a later resource with the same
  name in the same environment does not inherit the old exact rules. A
  container leaving its stack loses the stack's rules (its parents change).
- **Files:** each root type has its own keys (`stack.files.*`,
  `volume.files.*`), so a stack grant never opens volumes. Reading or writing
  the Compose definition (compose.yaml, override files, `.env`) through the
  file manager additionally needs `stack.definition.read` / `.write`
  (enforced by #15). `PATCH …/files/metadata` needs `<root>.files.chmod`
  and/or `<root>.files.chown` per requested change (selector
  `<root>.files.{change}`; job kind `files.metadata` selects them from the
  input keys `chmod` / `chown`).

The decision corpus (`internal/manager/authz/policy/testdata/corpus.yaml`,
`TestDecisionCorpus`) and the exhaustive capability × scope × override matrix
(`TestEveryCapabilityScopeAndOverride`) pin these rules. Through the
routes, the grant-scenario matrices in `internal/manager/api`
(`docker_test.go`, `authz_matrix_test.go`) run every Docker (#6), stack
(#7, #33) and container log/terminal (#8) route of an environment for
Restricted, metrics-only, restart-only (by container and by stack),
`stack.read` vs `stack.definition.read`, logs-only and exec-only users:
each opens exactly its capability's routes plus the minimal views it
reaches (a capability inside a stack shows the stack minimally), and
everything else answers 403/404 or an empty page. Logs and terminals are
never opened by metrics, restart, details or stack grants.

## Resource graph (Locators)

`authz.Resource{Type, ID, EnvironmentID, Parents}` is what a capability is
checked on. Handlers that know a resource's parents pass them (a container's
service and stack from its Compose labels). Otherwise the permission service
asks the **Locator** registered for the type:

```go
perms.RegisterLocator(catalog.TypeContainer, permissions.LocatorFunc(
	func(ctx context.Context, ref authz.ResourceRef) (permissions.Location, error) {
		c, ok := inventory.Container(ref.EnvironmentID, ref.ID)
		if !ok {
			return permissions.Location{}, nil // deleted: Found false
		}
		return permissions.Location{Found: true, EnvironmentID: ref.EnvironmentID, Parents: []authz.ResourceRef{
			{Type: catalog.TypeService, ID: authz.ServiceID(c.StackID, c.Service)},
			{Type: catalog.TypeStack, ID: c.StackID},
		}}, nil
	}))
```

Built in: services are located through their stack (`<stackId>/<service>`),
Docker-named resources live in their own environment. The manager registers
the agent Locator; #6 registers containers, volumes and networks
(`resources.Service.Locator`: members of a Docker Manager stack's Compose project
get their service and stack as parents; images and other objects use the
built-in rule), #7 stacks.
Locators must be cheap and must not call the Authorizer.

## Response shaping (minimal discovery)

The ability to run an action exposes only the fields needed to find the
resource and run it. For every resource route:

1. One checker per request: `c, p, err := api.CheckerFor(ctx, deps.Authorizer)`
   (or `authz.For`). Never call the Authorizer per list item.
2. `v := authz.ViewOf(c, resource)`:
   - `Full` — the type's read capability (`container.details.read`,
     `environment.read`, `agent.read`, …): the whole DTO.
   - `Minimal` — any other capability on the resource, or a grant on something
     inside it (a container grant makes its environment and stack reachable):
     only the minimal fields of `catalog.ResourceType.Minimal` (identity and
     status).
   - `Hidden` — dropped from lists, searches, counts and streams; `404` on
     direct access.
3. Every resource DTO carries `view` (`minimal`/`full`) and `actions`
   (`api.Actions(v)`: the granted capabilities) so the UI shows exactly those
   actions. The revision/ETag is exposed in a minimal view only when an edit
   action is granted.
4. Actions check their own capability (`v.Has(key)`), `403` when the resource
   is visible but the action is not granted.
5. Events: `authz.EventVisible(c, e)` for every bus event (file invalidations
   need the root's `files.read`; unknown event types reach only the owner).
   Every bus event type has a rule (`TestEveryEventTypeHasAVisibilityRule`).
6. Jobs: `job.read`/`job.cancel` are evaluated on **every target** of the job
   (`authz.JobResource`), or granted by holding the job kind's own
   capabilities on every target — a restart-only user follows and can cancel
   restarts of that container. Never by initiator.
8. Alerts (#159, [alerts.md](alerts.md)) have no read capability: an alert
   is visible with its source's permission (`authz.AlertVisible`:
   `environment.system.read` for disks and RAID, the environment for
   offline, `job.read` on the failed job, `update_policy.read` on the
   policy), in lists, gets and `alert.updated` events alike. Dismissing
   needs `alert.dismiss` (normal risk, so the Operator preset has it)
   scoped like the source: the environment, every target of the job, or the
   policy (`authz.AlertDismissible`).

7. Search (`GET /api/v1/search`, the UI's ⌘K palette, #22): each hit is
   filtered with the same `ViewOf` as its own list route (environments,
   stacks per stack, services by the stack's full view or a grant on the
   service, containers/images/volumes/networks with their parents) and
   carries identity and status only, whatever the view. Docker objects are
   searched only in environments the caller sees; environments that could
   not be searched are reported in `gaps` (never hidden ones).

Examples: metrics-only (`environment.metrics.read @env:E`) lists `E` with
`view: minimal`, `actions: ["environment.metrics.read"]` and no Engine ID,
revision or timestamps; system information, edits, agents and jobs are
refused. Restart-only (`container.restart @container:E/web`) lists `E`
minimally with no actions, shows restart jobs of `web` only, and allows the
restart job; start/stop/logs/terminal are denied.

## Enforcement points

- **Routes:** `api.Register` declares the capability and scope (OpenAPI
  `x-docker-manager-capability`); handlers check with the checker. Every capability
  key of the route inventory must exist in the catalog (`TestRouteInventory`).
- **Jobs (#26):** the engine authorizes the kind's capabilities
  (`jobspec.Spec.Capabilities`: plain, per-root file keys, input-selected
  keys) on every target at request (`authz.TargetResources`: file paths are
  covered by their root) and **again at dispatch** for queued manual and
  API-token jobs (lost grant → `failed`/`authorization_revoked`). Scheduled
  jobs run as the manager service identity. Running jobs finish or recover.
  Every job kind's capabilities are in the catalog
  (`TestEveryJobKindHasCatalogCapabilities`).
- **Agents:** the manager sends only named, authorized operations; the agent
  never evaluates permissions.

## Changes and invalidation

Every group or permission change (create, rename, delete, default selection,
group rules, user rules, group move via `PATCH /users/{userId}`):

- is owner-only and needs a **recent step-up** (`403 step_up_required`);
- is compare-and-set on a revision (group ETag for name/delete; the
  permission document's own ETag for rules; `412` with the current ETag when
  stale, `428` without `If-Match`);
- is audited with a before/after diff (`details.diff`, plus `rulesAdded` /
  `rulesRemoved` in shorthand; group moves record `details.event:
  user.group_change` and the group diff);
- ends the **affected users'** in-flight requests and open streams (SSE,
  WebSocket) through the identity hub and forgets their stored idempotent
  responses (`auth.Service.AccessChanged`). Clients reconnect and are
  filtered by the new rules. Rules are read on every check, so the next
  request already sees the change. The owner is never affected.

Decision: grant changes do **not** rotate the affected users' session tokens.
Rotation from another user's request would kill the old token at once and
fail the user's parallel requests with `401`, and it adds nothing against
fixation (tokens are renewed at sign-in, step-up, enrollment and credential
changes); no session state caches permissions.

Deleting a group requires it to be empty (`409 group_not_empty`): Docker Manager
never moves users implicitly, so deleting a group never changes anyone's
access. The owner's account does not count; if it is in the group, it moves
to the default group (the audit record of the deletion names the owner as a
target and holds `ownerMovedToGroupId`). Selecting a default group that grants access answers with a
`warning`.

## API

| Route | Who |
| --- | --- |
| `GET /api/v1/permission-catalog` | any signed-in principal |
| `GET /api/v1/me/permissions` | any signed-in principal: effective entries + visible environments (empty for Restricted: show an empty state) |
| `GET/POST /api/v1/groups`, `GET/PATCH/DELETE /api/v1/groups/{groupId}`, `POST …/default-selection` | owner |
| `GET/PUT /api/v1/groups/{groupId}/permissions`, `GET/PUT /api/v1/users/{userId}/permissions` | owner |
| `GET /api/v1/users/{userId}/effective-permissions` | owner |
| `POST /api/v1/permission-previews` | owner: view-as a user or a group's member with unsaved rules, a group move or an API token scope; explains checks with the deciding rule. No session is impersonated. |
| `PATCH /api/v1/users/{userId}` (`groupId`) | owner: moves the user to exactly one group |

## Adding capabilities (feature workstreams)

1. Add the entry to `internal/manager/authz/catalog/entries.go` (key, type,
   label, description, compatible scopes, risk, `Advanced`; `Since` = the
   new `catalog.Version`, bumped when keys are added after a release).
2. Use it in `api.Register` and the route inventory; job kinds use it as
   `jobspec.Spec.Capability`.
3. Register a Locator for new resource types; call `ForgetResource` after
   deleting a resource.
4. Shape responses (`ViewOf`, `view`/`actions`, minimal fields).
5. Test with `authztest` (below) and extend the corpus when the precedence
   has a new case.

## Testing with authztest

```go
pol := authztest.Only("rita", "allow container.restart @container:env-1/web")
mux := http.NewServeMux()
api.New(mux, api.Deps{Authorizer: pol, /* services */})
h := authztest.Authenticate(mux) // X-Authztest-User → principal
calls := authztest.Routes(t, map[string]string{"environmentId": "env-1", "containerId": "web"},
	"/api/v1/environments/{environmentId}/containers")
allowed, denied := authztest.Split(calls, "container.restart")
allowed, denied = authztest.Discoverable(allowed, denied, "list-containers", "get-container")
authztest.AssertOnly(t, h, "rita", allowed, denied)
authztest.AssertAbsent(t, "minimal container", body, "IMAGE_ID", "ENV_VALUE")
```

`Policy` also supports `Member`, `Group`, `User` (overrides), `Owner`,
`Token` (#31 scopes), `Disable` and `Locate` (resource graph). Rules use the
shorthand `"<allow|deny> <capability> @<all | env:<id> | <type>:<id> | <type>:<env>/<name>>"`.
