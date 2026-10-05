# Authorization (#17)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/authorization.md`. Owner bypass, then the most
specific user rule, then the user's groups in priority order (the first
group with a matching rule decides with its most specific rule), then deny.

- **Declare a capability:** add a `catalog.Capability` to
  `internal/manager/authz/catalog/entries.go` (key `<type>.<action>`, type,
  plain-language label, description, compatible scopes via
  `res(...)`/`instEnv`/`instRes(...)`, `high(...)` for risky actions,
  `adv(...)` for rare ones). Never add generic read/write keys; never rename
  a key. A key is retired only together with a migration that carries
  the group, user and API token rules naming it over to the key that
  replaces it and keeps every decision an explicit rule made: it checks
  the old and new decision at each scope point (instance, every
  environment, every resource) with the evaluator's precedence, each group
  on its own and then each member, and sets a rule at a point where they
  differ (`stack.down` into `stack.stop`,
  `20261005000000_stack_down_into_stop.go`). When the replacing key is
  broader (carrying an allow would grant more than the old key did, a deny
  would block what it allowed), the migration drops the rules instead and
  no decision of the replacing key changes (`stack.update`, which only
  allowed the stack's Pull, into `stack.deploy`,
  `20261006000000_retire_stack_update.go`); a job kind keeps its own key
  and borrows the capability (`Spec.Capability`). The web editor's presets derive from these flags
  (`web/src/lib/features/access/presets.ts`): Viewer takes every
  normal-risk key ending in `.read`, Operator also every normal-risk
  common one (plus `*.logs.read`), so end read-only keys in `.read` and
  mark writes that are risky or rare with `high`/`adv`. Every route, job kind (`jobspec.Spec.Capability`) and bus event
  type needs one (`TestRouteInventory`,
  `TestEveryJobKindHasCatalogCapabilities`,
  `TestEveryEventTypeHasAVisibilityRule`). File keys are per root:
  `stack.files.*`, `volume.files.*`.
- **Check:** one checker per request: `c, p, err := api.CheckerFor(ctx,
  deps.Authorizer)`; `c.Can("container.restart", res).Allowed`. Build
  resources with `authz.Resource{Type: catalog.TypeContainer, ID: name,
  EnvironmentID: env, Parents: []authz.ResourceRef{{Type: "service", ID:
  authz.ServiceID(stackID, svc)}, {Type: "stack", ID: stackID}}}` (nil
  Parents → the type's Locator), `authz.EnvironmentResource(id)`,
  `authz.InEnvironment(type, env)` (creation), `authz.Instance()`.
- **Resource graph:** register `perms.RegisterLocator(type,
  permissions.LocatorFunc(...))` (`app.Manager.Permissions()`) returning
  `permissions.Location{Found, EnvironmentID, Parents}`; call
  `perms.ForgetResource(ctx, ref)` after deleting a resource through
  Docker Manager.
- **Shaping:** `v := authz.ViewOf(c, res)`: `Hidden` → drop from lists/
  counts/streams, 404 on direct access; `Minimal` → only identity/status
  fields (`catalog.ResourceType.Minimal`); `Full` → everything. DTOs carry
  `view` and `actions` (`api.Actions(v)`); actions still check
  `v.Has(key)` (403 when visible but not granted). Events:
  `authz.EventVisible(c, e)`. Jobs: `c.Can("job.read",
  authz.JobResource(j))` (targets or the kind's own capability).
- **Alerts (#159)** have no read key: `authz.AlertVisible` shows an alert
  to whoever sees its source (`environment.system.read` for disks and
  RAID, `environment.metrics.read` for temperature, disk space and
  memory, the environment for offline, `job.read` on the job,
  `update_policy.read` on the policy) and `authz.AlertDismissible` needs
  `alert.dismiss` scoped like that source (the environment, every target
  of the failed job, the policy). A new alert kind gets its rule there
  first. Notifications (finished runs) are shown with `job.read` on their
  job (`authz.NotificationVisible`); the alert thresholds are the owner's
  (`notification_channel.manage`).
- **Jobs:** the engine authorizes `spec.Capabilities(targets, input)` on
  every target at request and again at dispatch; never authorize job work
  by initiator.
- **Tests:** `internal/manager/authz/authztest`: `Only(user, rules...)` /
  `New().Member().Group().User().Token().Locate()`, `Authenticate(h)`,
  `Routes(t, params, prefixes...)`, `Split(calls, caps...)`,
  `Discoverable(...)`, `AssertOnly(t, h, user, allowed, denied)`,
  `AssertAbsent`. Rule shorthand `"allow container.restart
  @container:<env>/web"` (`policy.ParseRule`). Extend
  `authz/policy/testdata/corpus.yaml` for new precedence cases.
- Permission/group changes are owner-only, need step-up, are revisioned,
  audited with diffs and end the affected users' streams
  (`auth.Service.AccessChanged`); session tokens are not rotated.
- Users are in any number of groups (`user_groups`; `domain.User.GroupIDs`
  and `PermissionSubject.Groups` in priority order, highest first); groups
  are ordered by `groups.position` (`store.ReorderGroups`, `PUT
  /group-order`). The owner is in no group. There is no default group: new
  instances start without groups and new accounts join none (deny by
  default until a group or override grants something); never add a group
  or rule that grants new accounts anything implicitly. Precedence cases across groups
  go in the corpus with `groups: [[…], […]]` (highest first);
  `authztest.Policy.Memberships(user, groups...)` sets several.
