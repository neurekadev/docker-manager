# Authorization (#17)

Binding conventions (split out of AGENTS.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/authorization.md`. Owner bypass, then the most
specific user rule, then the most specific group rule, then deny.

- **Declare a capability:** add a `catalog.Capability` to
  `internal/manager/authz/catalog/entries.go` (key `<type>.<action>`, type,
  plain-language label, description, compatible scopes via
  `res(...)`/`instEnv`/`instRes(...)`, `high(...)` for risky actions,
  `adv(...)` for rare ones). Never add generic read/write keys; never rename
  a key. The web editor's presets derive from these flags
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
- The owner is in a group only because `users.group_id` is `NOT NULL`:
  group member counts exclude the owner, and the owner never blocks a group
  deletion (`store.DeleteGroup` moves it to the default group in the same
  transaction). Never make group rules or counts depend on the owner.
