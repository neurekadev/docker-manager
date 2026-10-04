# Web UI

The Docker Manager UI is a SvelteKit single-page app (Svelte 5, TypeScript) built
with `@sveltejs/adapter-static` and embedded into `docker-manager`
(`web/embed.go`). It is an installable PWA. Library choices, licenses and
bundle sizes: [ADR 0002](adr/0002-frontend-libraries.md). Visual design,
tokens and the component library: [design/README.md](design/README.md) (#22;
live gallery at `/design`). Running the manager locally for UI work:
[development.md](development.md#running-locally).

```
web/src/
  app.html                    document template (manifest link, theme-color)
  service-worker.ts           service worker entry (SvelteKit-built)
  routes/+layout.svelte       global styles, QueryClientProvider (session-expiry hook),
                              SW registration, connection/update notices, toasts
  routes/+error.svelte        not-found and router errors outside the shell
  routes/(app)/               signed-in area: auth guard + AppShell (+layout.svelte),
                              the dashboard (+page.svelte), one folder per section,
                              +error.svelte (errors inside the shell); both error
                              pages share $lib/features/common/ErrorPageBody
  routes/(auth)/              public pages: setup, sign-in, enroll, invitation,
                              password-reset (centred layout, AuthHeader and the
                              submit checks of $lib/features/auth)
  routes/design/              the design system gallery (public, sample data)
  lib/design/                 tokens.css, global.css, tile colours and the service colour, demo data
  lib/ui/                     the component library ($lib/ui barrel)
  lib/features/<area>/        feature-local components and logic (resources: Docker
                              objects, refusals, job follow-up; builds; registries;
                              templates: cards, icons, publish and visibility dialogs)
  lib/shell/                  app shell: sidebar, nav filter, environment switcher,
                              top bar, command palette, notices, page title/breadcrumbs
  lib/auth/                   route guard, session lifecycle, WebAuthn, QR, one-time codes
  lib/routes.ts               every in-app URL
  lib/api/schema.d.ts         GENERATED from api/openapi.json
  lib/api/client.ts           typed client, unwrap(), ApiRequestError, schema type aliases
  lib/api/queries.ts          query keys, queryOptions factories, QueryClient
  lib/api/jobs.svelte.ts      JobWatcher (job event stream with a polling fallback)
  lib/api/job-states.ts       ACTIVE_JOB_STATES / TERMINAL_STATES (the one list of each)
  lib/features/<area>/        feature screens' logic and components (updates, maintenance,
                              backups, access, ...); lib/features/common: page layout, facts,
                              QueryView (loading/denied/missing/error), schedules, critical work
  lib/lazy/                   the only entry points to CodeMirror/ECharts/xterm.js (+ themes)
  lib/live/                   live stream client, query-key conventions,
                              liveStatus, critical-work registry (#23)
  lib/pwa/                    SW rules, registration/update flow, connectivity, manifest
  test/                       jsdom setup and test harness components
web/static/                   copied verbatim (icons, favicon.ico, robots.txt)
web/scripts/verify-build.mjs  post-build checks and bundle-size table
```

## Commands

```bash
bash scripts/check.sh lint          # includes Prettier (format:check) and ESLint (lint)
bash scripts/check.sh unit-tests    # includes vitest
bash scripts/check.sh build         # includes the web build and verify-build
npm --prefix web run check          # svelte-check / TypeScript (by hand; not in the gate or CI)
npm --prefix web run dev            # dev server; proxies /api to 127.0.0.1:8080
npm --prefix web run build          # web/build/app (embedded by the next go build)
node web/scripts/verify-build.mjs --markdown   # bundle table for ADR 0002
bash scripts/generate.sh            # regenerate api/openapi.json + schema.d.ts
```

The service worker is not registered under `vite dev`; try PWA behaviour
against a built manager (below).

## Generated client workflow

1. Add or change the Go operation (`internal/manager/api`, see [conventions/api.md](conventions/api.md)).
2. `bash scripts/generate.sh` regenerates `api/openapi.json` and
   `web/src/lib/api/schema.d.ts` (openapi-typescript). Commit both.
   `TestOpenAPISnapshot` (`go test ./...`) fails when `api/openapi.json` is
   stale; `schema.d.ts` is only compared by `bash scripts/generate.sh
   --check`, which no gate runs, so regenerate after every API change.
3. Call it through the typed client. Paths, parameters, bodies and responses
   are checked by TypeScript against the schema:

   ```ts
   import { api, unwrap } from '$lib/api/client';
   const stack = await unwrap(
   	api.GET('/api/v1/stacks/{stackId}', { params: { path: { stackId } }, signal })
   );
   ```

   `unwrap()` returns the data or throws an `ApiRequestError`:
   `status` (null for network failures), `apiError` (the Docker Manager error body;
   switch on `apiError.code`, never on `message`) and `network`.

4. For reads, add a query-key entry and a `queryOptions()` factory in
   `src/lib/api/queries.ts`, then use it in components:

   ```ts
   // queries.ts
   export const queryKeys = { health: ['health'] as const, stacks: ['stacks'] as const };
   export function healthQuery(client: ApiClient = api) {
   	return queryOptions({
   		queryKey: queryKeys.health,
   		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/health', { signal })),
   		staleTime: 30_000
   	});
   }
   ```

   ```svelte
   <script lang="ts">
   	import { createQuery } from '@tanstack/svelte-query';
   	import { healthQuery } from '$lib/api/queries';
   	const health = createQuery(() => healthQuery());
   </script>

   {#if health.data}{health.data.version}{:else if health.isError}{health.error.message}{/if}
   ```

   Pass `signal` so leaving a view cancels its requests. Tests pass a fake
   `fetch` to `createApiClient(fetch, baseUrl)` (see `client.spec.ts`).
   Keys follow the live conventions (`liveKeys` in `src/lib/live/keys.ts`,
   e.g. `['stacks', 'item', id]`), so a live event invalidates lists and
   details together.
5. Writes use `createMutation` and invalidate by key prefix
   (`queryClient.invalidateQueries({ queryKey: queryKeys.stacks })`).
   Mutations are never retried automatically; dangerous retries use the
   API's idempotency keys (#4). Reads stay current through the live
   stream (below): build their query keys with `liveKeys`.
6. A 401 while signed in means the session ended: the QueryClient hook drops
   every cached API response and redirects to sign-in with `?next=`
   (`src/lib/auth/session.ts`).

Rules: the browser talks only to same-origin `/api/v1` (never an agent or
Docker socket); tokens and secrets never go to `localStorage`,
`sessionStorage` or Cache Storage. The sign-in page remembers the **Stay Signed In** choice
in `localStorage` (`docker-manager:stay-signed-in`, `"1"`/`"0"`, a
preference only; `src/lib/features/auth/stay.ts`), and the last switch
between passkey and authenticator code under `docker-manager:verify-with`
(`"passkey"`/`"totp"`; `src/lib/auth/verify.ts`). Signed-in devices are
listed by `SessionsTable` (`src/lib/features/access`) under **Profile →
Sessions** and on a user's page (owner).

## Feature modules and step-up

Screens of one area keep their query factories, pure presentation helpers
(`model.ts`, unit-tested in `model.spec.ts`) and area components in
`src/lib/features/<area>/`; generic pieces stay in `$lib/ui`. Shared page
pieces are in `src/lib/features/common` (`Page`, `QueryView` for the
loading/denied/not-found/error states, `Facts`, `NameCell` and `IconCell`
(a list row's name with its type icon from `resourceIcons.ts`), `Fields`,
`FormFooter`, `ScheduleSummary`, `LinkList`/`LinksEditor` (a stack's or
template's links, rules in `links.ts`), `useUnsaved`/`useCriticalWork`).
Query keys
still follow `liveKeys` (a feature marker after `'list'` keeps cached
shapes apart).

The section lists (containers, images, volumes, networks, stacks, jobs,
schedules) share `$lib/features/resources/ListCard.svelte`: the "All …"
card with the search, the select filters and the switches (`kind:
'switch'`, stored as `"on"`) in its header. Each page builds its
`ListFilter`s from its rows (`filters.ts`; stacks, jobs and schedules:
`filters.ts` of their feature), filters with `applyListFilters` and keeps
the state in a `ListFilters` store (`list-filters.svelte.ts`):
`sessionStorage` under `docker-manager:list-filters:<list>`, so each list
keeps its own search and filters per browser tab when the user leaves and
comes back. The stored value is parsed defensively (anything but short
strings is dropped); without `sessionStorage` it lives in memory. The
jobs list is paged by the server: its state, kind, environment and
policy filters become the `GET /jobs` query (`jobQuery`), the count uses
the first page's `total` when the server sends it ("50 of 1,234 jobs",
`jobsSummary`), the search runs over the loaded jobs, and
`?environment=` from an environment page, `?kind=` and `?policyId=`
(`routes.jobs(kind, { policyId })`, a policy run's "Open Jobs") set their
filters once; the policy filter shows only while set.

The containers list and a stack's services table share their column
order and cells: the image's update state is
`$lib/features/updates/ImageUpdateBadge.svelte` (an icon with the state
in its tooltip; with a policy the user may check it starts that policy's
`update.check` job through `check.svelte.ts` and spins until the job
ends, and while the running list has an `update.check` of that policy
(`checkingPolicies` in `updates/running.ts`; setup-wide checks carry the
setup's ID and do not spin per-target badges); the containers list finds the policy with `policiesByTarget` /
`containerPolicy`, the services table from the image status's
`policyId`), and the networks are `$lib/features/resources/NetworkList.svelte`
(each network linked to its page with the addresses on it). The services
table links each image to its page by ID (`serviceImageId`: a running
container's image, else any container's, else the last deploy's; plain
text when none is known) and lists the volumes after it
(`serviceVolumes`: each volume of the service's containers once, named
before anonymous ones, which read "Anonymous" with their mount path;
linked to the volume page; two lines at most, the second "+N more" with
the rest in its tooltip; bind mounts are not volumes). The networks
list counts attachments from the containers list (network lists do not
report them), which also drives its "Unused" switch.

Dropdowns are `$lib/ui/Select.svelte` (Bits UI Select: a themed
listbox with typeahead; `onchange` receives the value) and `Combobox`;
there is no native `<select>`. Tooltips: `$lib/ui/TooltipLayer.svelte`,
mounted once in the root layout, shows every `title` attribute as a
themed tooltip (on hover after 400 ms and on keyboard focus; the title
moves to `data-dy-title` while shown so the native one never appears,
and the element is described by the tooltip unless its accessible name
is the same text). Info tips (`data-dy-info`: `InfoTip`, `Disclosure`'s
hint) also open on a tap, since touch has no hover; a second tap or a tap
elsewhere hides them. `Tooltip.svelte` stays for
controls that want an explicit trigger (`IconButton`).

The permission editor of #17 (the design's "PermissionTree") is
`$lib/features/access/PermissionEditor.svelte`: a searchable resource tree
(`ResourceTree`, categories in `tree.ts`) beside the actions of the chosen
scope (`ActionMatrix`), in three modes: `group` (No Rule / Allow / Deny),
`user` (Inherit / Allow / Deny with the inherited decision explained) and
`token` (grants limited to what the caller holds, #31). Scopes carry
`environmentId` only for the types named per environment (container,
image, volume, network; `NAMED_PER_ENVIRONMENT` in `tree.ts`, mirroring
the catalog); stacks, services, agents and policies have global IDs, so
their nodes keep the environment on `ScopeNode.environmentId` for display
only, and a rule on an unlisted stack or service shows under its stack's
environment (from the stack list). Rule logic
(scope keys, diffs, inheritance precedence) is in `permissions.ts`;
`RulesSaveBar` lists every change before the revisioned, step-up save.
`ActionMatrix` shows one collapsible section per resource type (open at
first only where the scope has rules, all while filtering), each with
"Allow All" ("Grant All" for tokens) and "Clear" ("Inherit All" for users)
that change the draft at once; the controls sit in one right-aligned
column and "High Risk" is marked once, next to the action. Groups and
tokens can "Start From" a preset at the chosen scope (`presets.ts`:
Viewer = every normal-risk `*.read`, Operator = Viewer plus the common
normal-risk actions and container logs, Admin = everything the scope
offers), derived from the catalog's key, risk and advanced flags, never
from a key list; the select names the preset the scope's rules match, or
Custom. The Groups page lists the groups in priority order: dragging a group's
grip (`Sortable`, `DragHandle`) saves the new order at once
(`saveGroupOrder`: `PUT /group-order` with the list's ETag, refused when the
order changed meanwhile). A group's page lists its members first
(`groupMembers`), adds members with one `PATCH /users/{id}` (`groupIds`,
`withGroup`) per account and removes one from this group only. A user's
page lists their groups in priority order (add, remove), and the
permission editor shows what a user inherits from the first group with a
rule (`inheritedFromGroups`).

The caller's own things and instance administration are separate areas.
**Profile** (`routes/(app)/profile`, opened from the user menu and ⌘K,
not in the sidebar) is personal: the account, password, authenticator
app, passkeys and recovery codes (`/profile`) and the caller's API tokens
(`/profile/tokens`, create at `/profile/tokens/new`); its module is
`$lib/features/profile` (`ProfileHeader`, `profileTabs` in `tabs.ts`,
`TotpSetup`, the passkey and recovery-code queries). **Settings**
(`routes/(app)/settings`, the sidebar's Administration group) is
instance administration only: Overview, every user's API tokens
(`/settings/tokens/all`, owner), sign-in policy, schedule defaults,
notification channels (`/settings/notifications`, owner;
`$lib/features/notifications`), audit log and diagnostics, each tab hidden
without its capability
(`settingsTabs` in `$lib/features/settings/tabs.ts`, `SettingsHeader`).
Both token lists use `$lib/features/access/TokensTable.svelte`. The old
addresses `/settings/security` and `/settings/tokens[/new]` are
`+page.ts` redirects to their Profile pages that keep the query string
(as `/backups/policies` redirects to the Backups overview).

The audit log (`routes/(app)/settings/audit`) is a `ListCard` over the
server-filtered, paged `GET /audit`: Who (actor kinds, and each user for
the owner), Outcome, Category and When in the header, exact action keys,
a resource, the environment and custom dates under "More Filters"; the
search runs over the loaded records. Helpers in
`$lib/features/settings/audit.ts`: `auditActionLabel` (a label map for
lifecycle and identity keys, else the catalog label), `targetText`
(names from the users, groups, environments, stacks, registries and Git
credentials the caller can list; opaque IDs never show), and
`groupAuditRows` (consecutive identical records become one row, "24
times"). The record drawer keeps the action key, raw targets, request
ID, error class and chain position under Advanced.

Stack actions that start jobs go through `$lib/features/stacks/deploy.svelte.ts`
(`startDeploy`) and the page's `JobTray`, which also adopts the stack's
running jobs from the running list (`tray.adopt`, words from
`stackJobCopy` in `adopt.ts`; see "Job progress after reload" below), so
a reload or the `?job=&kind=` handoff of Create stack (dropped from the
URL once read) finds them again: a tracked job's `successFor`
computes the success toast once it ended, from data read again (a deploy
whose `appliedRevision.at` did not move started no container: "Nothing to
deploy"). Deploy is the header's one primary action (a split button that
deploys at once); its menu has "Deploy" and "Pull & Deploy" (one deploy
with `pull: always`; the menu button's accessible label says "newer
images are available" when `updateAvailable`), then after a separator
"Cleanup Orphans & Deploy". Stacks with a `build:` section get a
secondary **Build** split button ("More Build Options"): "Build" and
"Pull & Build" (`POST /stacks/{id}/builds`, `buildStack`, with `pull`
for newer base images; a `stack.build` job that deploys nothing, words
from `buildCopy`) with `stack.build`, then after a separator "Build &
Deploy" (`build: true`) and "Pull, Build & Deploy" (`pull: always` and
`build: true`, which also pulls newer base images) with `stack.deploy`;
its main part runs the first entry it has. "Cleanup Orphans & Deploy", whose
confirmation (`RemoveOrphansDialog`, opened through the stack page
context's `removeOrphans` request) the overview's drift notice ("Remove Old
Containers…") opens too. There is no separate Update button; schedules and
automatic updates stay in the update settings.

Start, Restart and Stop are one split button, `LifecycleButton`
(`$lib/features/common`, pure rules in `lifecycle.ts`), next to Deploy in
the stack header and on a container's page. The main part is **Stop**
(`danger-soft`, Square icon) while anything runs and **Start** (`ok-soft`,
Play icon) while nothing does; soft tones, so Deploy stays the one
primary. A partially running stack (and a `failed` or `deployed` one whose
Engine state is unknown) counts as running: Stop is the default and Start
in the menu starts the rest. The menu lists Start, Restart and Stop in
that order, each only with its capability (`stack.start`/`restart`/`stop`,
`container.*`; hidden, not disabled), the ones that do not apply in the
state turned off (Start while everything runs, Restart and Stop while
nothing does; a container's from `containerActions`). With one held
action it is a plain button; with none for the state it is not shown (a
down, missing or undeployed stack deploys instead; a restore of the stack
hides Start and Restart). Offline, a rename in
progress or a running Start/Restart (`busy`: the main part shows that
action with a spinner) turn the whole button off. Docker Manager's own
stack keeps Restart and Stop visible but off, with the reason as the
main part's tooltip and an `sr-only` text (menu items carry no
description);
Docker Manager's own containers keep them on (the server refuses with its
reason; the page's notice says so up front). Start and Restart run at
once; Stop confirms with its consequences. Row menus (services table,
stack and container lists) keep their own entries in the same order
(Start, Restart, Stop…, Stop in the danger tone; the stack list's Start
also starts the rest of a partially running stack). The container list's
bulk bar stays separate buttons (a selection mixes states).

There is no Take Down in the UI (the `stack.down` operation and capability
stay in the API). The header hides its actions while the migration wizard
is open, and Migrate while the caller sees one environment.

Rename (`stack.rename`) is the pencil right of the stack's name
(`PageHeader`'s `titleAction`, shown with the capability and a loaded
revision; off while offline, for Docker Manager's own stack and while a
rename runs, the reason as its tooltip). It turns the name into a field
in place (`RenameStackInline` through `PageHeader`'s `titleEditor`; the h1
stays for screen readers) labeled "Stack Name" that edits the Compose
project name, also when the heading shows a display name. Enter or the
check button renames at once, without a confirmation; Escape or the cancel
button keeps the name. The name is checked first (`renameNameError`), then
the server's rename preview is asked silently for a refusal
(`renameRefusal`: the Compose file's `name:` fixes the project name, or a
blocker such as a taken name), shown under the field; its warnings are not
shown. The `stack.rename` job goes to the page's tray with `kind:
'stack.rename'`. While a rename of the stack runs (`tray.running
('stack.rename')`, or `activeRename` over the stack's jobs, so it holds
after a reload) every action of the header is off with "Renaming <stack>…"
as the reason: Deploy (main part and menu), the lifecycle button, the
pencil and the overflow entries (the reason heads that menu), and a status
badge says so.

The Revisions tab groups consecutive revisions with the same fingerprint
(`groupRevisions`), never opens a comparison of two equal ones
(`defaultComparison`, `comparisonFor`) and, while the files on disk differ
from the deployed revision, shows that diff at once with "Deploy These
Changes" and "Restore Deployed Revision". The Activity tab hides update
checks by default (`visibleJobs`), folds a job's audit records into one row
(`auditRows` in `$lib/features/stacks/activity.ts`) and reads the audit
log 50 records at a time ("Load More").

An environment's stacks move together through **Migrate Environment**
(`routes.environmentMigrate`, `/environments/<id>/migrate`): the entry of
the environment page's "More Actions" menu (shown with a second
environment and a stack there the caller may migrate) and the archive
dialog's "Migrate N Stacks First". The wizard is
`$lib/features/environments/EnvironmentMigrationWizard.svelte` (view
model `environment-migration.ts`, requests `migration-actions.ts`, the
run's record in `queries.ts`, keyed under its job,
`liveKeys.item('jobs', id, 'environment-migration')`, so the job's events
refresh it; the environment's latest migrations,
`environmentMigrationsQuery`, keyed under the jobs list,
`liveKeys.list('jobs', 'environment-migrations', envId)`, so every job
event refreshes them: the run's own, its stack migrations' and the
removals'). Destination: the other active environments (offline ones
off) and the stacks, all ticked; Docker Manager's own stack (found from
its protected containers and the check's `skipped`) and stacks without
`stack.migrate` are off with the reason, and nothing unticked sends no
stack list. Check (Next runs it; "Check Again"): the problems of the
whole migration, then each stack's under its name, data, free space, the
longest downtime with its basis, the order (one row per group, its
stacks in move order with what each waits for), networks created first,
what is not moved and why, warnings, and each stack's details (volumes,
images, warnings, access changes). Confirm, then Move: the
`environment.migrate` job's progress and each stack's outcome from the
record, one toast (the stack that did not move, with "Open Job" on its
stack migration), each stack's state ("Moved" with "Old Copy Kept" or
"Old Copy Removed", "Did Not Move", "Not Started"), "Migrate the Rest"
(back to Check, for the run's stacks still on the source), "Remove Old
Copies From <source>" (type-to-confirm; the moved stacks' copies of this
run and of earlier ones, `pendingCopies`; one source removal per stack,
followed as tracked jobs, `oldCopyRemovalMatch` in `ActiveJobs`, so they
survive a reload; one `bulkSummary` toast when the ones started together
ended; the result reads `sourceRemoved` from the record), "Start a New
Migration" (the first step, while the source has stacks to migrate)
and, when every stack moved, a link to archive the source. The wizard
opens on the latest run's result once it ended (no toast, no
notification) while it left something to do: old copies to remove or
its stacks still on the source (`restoredMigration`, decided once when
the list first answers). Next's tooltip says why it is off
(`StepWizard` `disabledReason`); "Migrate the Rest" and "Start a New
Migration" move the focus to the step's heading. The environment page
shows `EnvironmentMigrationNotice.svelte` while old copies wait on it
("3 stacks moved to NAS", "Their old copies are still on this server.",
"Review the Migration" to the migrate page; `oldCopiesNotice`; only for
callers with a `stack.migrate` grant: the list answers 403 to others and
lists only the stacks the caller sees). The check's findings
(everything below the headline and "Check Again") are
`EnvironmentMigrationCheck.svelte`, shared with the manager move's Check
step. Both migration wizards list findings with
`$lib/features/stacks/MigrationFindings.svelte`. Tests:
`environment-migration.spec.ts`, `EnvironmentMigrationWizard.test.ts`,
`EnvironmentMigrationNotice.test.ts`.

Moving Docker Manager to a new server
([manager-move.md](architecture/manager-move.md)) lives in
`$lib/features/managermove`: pure `model.ts` with `model.spec.ts` (the
move's states, the wizard's steps, checklist, reminders and progress, the
status page's steps and notices, Move Complete's items, all in words);
requests in `queries.ts` (`managerMoveQuery` resolves the 404 of "no
move" to `null`; `createMove`, `createSetupFiles`, `startMoveRun`,
`cancelMove` and `acknowledgeConfirmation` go through `withStepUp`;
`moveStatusQuery` is public). The move's queries are keyed
`liveKeys.managerMove(...)` (`['manager', 'item', 'move', ...]`) and follow
the live stream (topic `manager`): kind `manager_move` refreshes the
owner's move (and the form's defaults), kind `manager_move_lock` every
session (`liveKeys.session`, the banner's lock). Nothing on the old or new
manager polls the move; only the new server's status page polls
(`WAIT_POLL_MS`, 3 s: no sign-in there, so no stream).

- **Settings, Move to a New Server** (`routes.managerMove`,
  `/settings/move`, an owner-only Settings tab; the palette finds Settings
  by "migrate manager" or "new server"): `ManagerMoveWizard.svelte`, a
  `StepWizard` of three steps. *New Server*: "This Server's Address"
  (prefilled from `GET /manager/move/defaults`), "New Server's Address",
  optional "Name for the New Server", then "Create Setup Files"
  (step-up) shows the returned `compose.yaml` and `.env` once under "Set
  Up the New Server" (copy and download buttons through
  `InstallCommand`'s `filename`; the answer lives only in the component,
  never in a query, URL or storage; its heading takes the focus), the
  same as one paste under "Or Paste This on the New Server"
  (`setupScript`: a `set -e` subshell that creates `docker-manager/`,
  writes both files from quoted here-documents whose delimiter is no line
  of the file (`heredocDelimiter`), makes `.env` mode 600 before the
  secrets go in and runs `docker compose up -d`) and the checklist "New
  server's agent connected" / "New Docker Manager is waiting"; Next waits
  for both (`disabledReason` says why). Files that are no longer on the
  page (a reload) or whose enrollment token expired
  (`setupFilesReason`, `setupFilesNotice`) are replaced with "Create New
  Setup Files" (`ConfirmDialog` with `setupFilesConsequences`, step-up,
  `POST /manager/move/setup-files`; `agentEnrolled` answers say the
  agent stays connected, `AGENT_KEPT`). *Check*: the environment migration's
  check from the environment next to Docker Manager to the new server's
  (`EnvironmentMigrationCheck`), or "Only Docker Manager moves" when
  there is none or it has no stacks, plus "Before You Start" (the proxy
  reminders and the status address). *Move*: "Move Everything"
  (step-up), then the progress read from the move alone (`runView`:
  stacks moved of all, the current stack, "Handing over Docker
  Manager"); a stopped run shows its reason and recovery with "Try
  Again"; a ready move whose new manager stopped asking says so
  (`NOT_ASKING`). "Cancel the Move" (`ConfirmDialog`,
  `cancelConsequences`) sits in the wizard's footer until the handoff.
  The wizard opens where the move stands (`stepOf`), so a reload or
  coming back resumes; a step that changes by itself (the move went on,
  or ended) moves the focus to its heading; the `manager.move` job is
  tracked (`runs.add`, `managerMoveJobMatch`) and a change of the running
  list reads the move at once. After the handoff the moved panel says
  where to point DNS and where to follow the move (`movedPanel`), with
  "Resume on This Server" (`DestructiveConfirm` typing the instance name)
  until the new manager confirms. Resuming, or cancelling once agents
  heard the new address (`restartsWhenEnded`), restarts Docker Manager:
  the wizard shows "Restarting Docker Manager…", waits for it
  (`waitForRestart` in `restart.ts`: `GET /api/v1/health` until it went
  down, then until it answers again with backoff, 3 minutes at most; then
  "Docker Manager does not answer yet." with Reload) and reloads the
  page.
- **The new server's status page** (`routes.moveStatus`, `/moving`,
  public, `(auth)` layout): `WaitingStatus.svelte`, built from `GET
  /api/v1/move/status` alone (polled every 3 s, no session or cookie, so
  it works over plain http): the steps with the current one highlighted
  (`waitSteps`) and one notice (`waitNotice`: done with where to point
  DNS, a problem with the server's recovery, or "press Move Everything
  on the old server"). A manager in waiting mode answers every other
  route with 503 `manager_move_waiting`, so the root layout wraps every
  page in `MoveGate.svelte`: it reads the status once before any sign-in
  or setup routing and sends every page to `/moving` while
  `isWaitingPhase` (not `none`, not `complete`: after the move sign-in
  works as usual); no answer (offline, an older manager) lets the app
  start as always. The root layout starts the live stream only from
  `MoveGate`'s `onready` (known, not waiting), so a waiting manager gets
  no stream requests it would answer with 503.
- **Move Complete** (the new manager, owner): `MoveCompleteCard.svelte`
  on the dashboard (and on the Settings tab) while `moveCompleteDone` is
  false: the old manager's confirmation (live: each attempt announces the
  move; "Mark as Done" with a warning once a confirmation failed), agents
  that did not get the new address with their one-line fix, the moved
  stacks' stopped copies on the old server (removed from the environment
  migration's record with `removeOldCopies`, type-to-confirm, one summary
  toast) and "Open <old environment> to Archive It" (its page).

A locked manager shows a persistent banner in the shell (`MoveBanner`
in `AppShell`, `moveBanner`): the owner's shell reads the move, everyone
the session's lock (both refreshed by the live stream, never polled); a
change refused before either knew it is heard from the first request
answered 409 `manager_moved` (`onApiFailure` in
`$lib/api/client.ts` hears every error `unwrap` throws;
`managerMoved.refused` in `moved.svelte.ts` holds it for the page load).
Only the lock states (`draining`, `handed_off`, `confirmed`) show it:
the apps moving (`open`, `moving`, `ready`) lock nothing.
`errorView` words `manager_moved` the same everywhere
(`KNOWN_ERRORS` in `$lib/ui/errors.ts`). Tests: `model.spec.ts`,
`restart.spec.ts`, `ManagerMoveWizard.test.ts`, `WaitingStatus.test.ts`,
`MoveCompleteCard.test.ts`; the live keys in `$lib/live/keys.spec.ts`.

Changes the manager guards with recent authentication answer
`403 step_up_required`; wrap the call in `withStepUp(() => …)` from
`$lib/auth/stepup.svelte`: the signed-in layout's `StepUpDialog` asks for
one factor once and the call is retried; dismissing it throws
`StepUpCancelledError`. The factor follows `$lib/auth/verify.ts` (#186),
shared with the sign-in page's second step: a passkey first (its browser
prompt starts as the dialog opens), else the authenticator code alone,
else the password. "Use a Passkey Instead", "Use Authenticator Code
Instead" and "Use Your Password Instead" switch to any other factor the
account has (the password is a fallback for every account with one); the
browser remembers a switch between passkey and code, never the password.

## Job progress after reload

Every progress bar survives a reload and leaving and coming back: a view
never keeps a job ID only in component or module state. It restores what
it shows from **the running list**, `activeJobsQuery()` (`GET
/jobs?state=<ACTIVE_JOB_STATES>&limit=200`, key `liveKeys.list('jobs',
'active')`, one request per tab however many views read it). Job events
refresh it (the live client invalidates `['jobs', 'list', …]`, at most
twice a second); it polls every 10 s only while the stream is down
(`pollWhileDown`). (The live `job` event also names the job's targets,
policy and progress, for clients that match events without refetching;
the web refetches the list.)

- **Matching** (`$lib/features/jobs/active.ts`, pure): `matchJob(job,
  { targets, kinds, kindPrefix, excludeKinds, policyId, environmentId })`.
  Targets match by type and ID or ID prefix (file jobs:
  `path:/<scopeKind>/<scopeId>/…`); a target's environment is its own,
  else the job's (names repeat across environments); `policyId: null`
  means jobs run for no policy (a one-off prune). The server offers the
  same filters (`kind` as a comma list, `target` + `targetEnvironmentId`)
  for views that list older jobs.
- **Tracking** (`useTrackedJobs(() => match)` in `tracked.svelte.ts`):
  the jobs the view started (`add(job, title)` with the POST answer, shown
  at once) ∪ the matching running jobs ∪ the jobs it showed that have
  ended since (kept with their outcome until dismissed or the view goes
  away; `trackedEntries`). A new match (another object in the same
  layout) starts over.
- **Showing** (`ActiveJobs.svelte`): one `JobProgress` per job, titled by
  the view (`titleOf`) or `jobTitle`; Dismiss on ended jobs; `onfinish`
  once per job for the view's toast and refresh. At most
  `MAX_JOB_STREAMS` (3) running jobs per view follow their own event
  stream (browsers open six connections per host over HTTP/1.1, and the
  live stream takes one); the others are compact `JobRow`s fed by the
  running list, and switch to `JobProgress` when they end (its stream
  replays the end and closes). The stack page's `JobTrayView` applies the
  same cap (`streamedIds`) and, unlike `ActiveJobs`, drops a job when it
  ends: the toast reports the outcome (a failure's toast stays until
  closed, with the recovery advice and "Open Job").
- **Builds** (`$lib/ui/build-progress.ts`): `JobWatcher.output` keeps a
  job's progress messages in order (the newest 500). While the last one is
  BuildKit's (`<image>: [<stage> 2/5] RUN make: started`, or a chunk of a
  step's output after a line break), `JobProgress` shows the step in words
  ("silo-web · Step 2/5: RUN make") and the bar follows the steps of the
  image being built; it never shows raw output (`progressText` also keeps
  `JobRow` and the stack's Activity tab to a message's first line).
  **Show Output** (once a build step or output arrived) opens the Build
  Output dialog: one per tab (`buildOutput` in `build-output.svelte.ts`,
  `BuildOutputDialog` in the signed-in layout), so it stays when the tray
  drops the job; it follows the job's own event stream (replayed from the
  start) and writes the output into a read-only xterm.js terminal
  (`OutputFormatter`: a "=> step" heading per step, CACHED and ERROR
  lines). The Builds page's log (`BuildLog`) lists the same progress
  messages with the job's log and warning lines.
- **Top bar**: `RunningJobs` (`$lib/shell`) shows "N running" from the
  same list, linking to `/jobs?state=active` (the jobs list's "In
  Progress" filter); hidden while nothing runs and for restricted users.

What each view matches (the pure helpers are spec-tested next to them):

| view | running jobs shown |
| --- | --- |
| stack page (tray, all tabs) | target `stack:<id>`, every kind but the background ones, `update.check` and `backup.run` (`stackTrayMatch`; the top bar counts them); on `/migrate` also not `stack.migrate` |
| migration wizard | a running `stack.migrate` of the stack reopens it at the move step (`migration-resume.ts`); the wizard shows it instead of the tray and hands it back when left while it runs |
| environment migration wizard | a running `environment.migrate` from the environment reopens it at the move step (`environmentMigrationMatch` in `environment-migration.ts`); the stacks it moves say "Migrating" in the stacks list; an ended one reopens on its result from its record (`restoredMigration`); the removals of old copies on the environment (`oldCopyRemovalMatch`) show under the result |
| manager move wizard | a running `manager.move` (`managerMoveJobMatch`) reads the move again; the Move step shows its progress from the move (`runView`), which also survives a reload |
| stacks list | one match for the list (`stackListMatch`), the newest job per stack as a status word in the row (`StackJobStatus`) |
| import dialog | `stack.import` of the environment, matched to projects by their stack target |
| container, network pages | target by name in the environment (`object-jobs.ts`) |
| image page | the image ID, each tag in the forms a pull may have used (`nginx` = `library/nginx` = `docker.io/library/nginx`) and each digest |
| volume page | target by name; on Files and Migrate not the tab's own kinds (the tab shows them) |
| volume migrate page | `volume.migrate` from this environment: progress and result instead of the wizard |
| images, containers lists, new container | `image.pull`, `container.create` of the selected environment |
| prune buttons | `prune.run` without a policy in the button's environments ("Pruning…"; the dialog opens on it) |
| maintenance page | every job of the maintenance setup (one bar per environment) |
| backup overview and policy pages | not generic job cards: the running backups and retentions of `GET /backup-activity` as one steady line each in "Running Now" (`RunningBackups`); a job leaving that list reports its outcome once (`onJobsFinished`); the activity also feeds the runs table |
| backup, repository pages | verifications of the repository (`features/backups/jobs.ts`) |
| restore dialog and wizard | a restore of the backup's stack or volumes opens on its progress |
| Updates, preview dialog | every `update.check`/`update.run` of the selected environment; the preview dialog opens on a running `update.run`; succeeded update jobs clear after 4 s |
| file manager | see "Files, logs and terminals" |

Uploads are the exception: they are browser requests, not jobs, so a
reload or closing the tab cancels them (the browser asks first; see
"Files, logs and terminals").

Tests: `active.spec.ts` (matching, the tracked union, the stream cap),
`active.test.ts` (`ActiveJobs`), `running-jobs.test.ts`, `tray.test.ts`
(the stack layout's tray after a reload, the Create stack handoff and
ended jobs leaving the tray for a toast).

## PWA

- **Manifest**: `src/lib/pwa/manifest.ts` (written to
  `/manifest.webmanifest` by `@vite-pwa/sveltekit`), linked from
  `app.html`. `id`, `start_url` and `scope` are `/`, `display` is
  `standalone`. Colours are the design tokens (#22): `THEME_COLOR` is
  `--surface-shell`, `BACKGROUND_COLOR` `--surface-canvas`; keep
  `app.html`'s `theme-color` equal to `THEME_COLOR` (unit-tested).
- **Icons** (#22): the app icon is the Docker Manager logo (the whale
  carrying containers); the shell's lockup (`src/lib/shell/Logo.svelte`)
  shows the 64 px icon. Source: `web/scripts/logo.png` (1024 px, the logo
  on a transparent square). The `any` icons and `favicon.ico` are the logo
  edge to edge; the maskable and Apple touch icons keep a transparent
  background so the Android launcher and iOS supply the tile behind the
  logo (#214, #220), padded to keep the logo inside the 80 % safe circle.
  Regenerate the PNGs after changing it:

  ```bash
  cd web
  npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets.config.mjs
  mv scripts/favicon.ico static/favicon.ico
  mv scripts/pwa-*.png scripts/maskable-icon-512x512.png scripts/apple-touch-icon-180x180.png static/icons/
  ```

- **Service worker** (`src/service-worker.ts`, rules in
  `src/lib/pwa/sw-core.ts`, unit tests in `sw-core.spec.ts`): precaches only
  content-hashed build files, the manifest, icons and the SPA shell;
  `/api/*`, `/agent/*`, non-GET and cross-origin requests are never handled
  or stored; navigations are network-first with the precached shell as the
  offline fallback. Details and rationale: ADR 0002.
- **Updates**: a new build installs in the background and waits. The
  *App Update* notice offers *Reload to Update* (applies it and reloads once)
  or *Later*; nothing reloads on its own. While critical work is registered
  (`criticalWork` from `$lib/live`: an unsaved editor buffer, a live
  terminal, an in-progress restore or upload) the button is disabled, the
  notice says what is still open and `applyUpdate()` refuses, also when the
  new worker took control meanwhile: register it for as long as a reload
  would destroy it:

  ```ts
  import { criticalWork } from '$lib/live';
  const release = criticalWork.register('unsaved-edit', path); // on first edit
  release(); // after save or discard (idempotent)
  ```

  `register` and its release do not track reactive reads, so an `$effect`
  may return `criticalWork.register(...)` as its cleanup while a form is
  dirty.
- **Offline**: the *Connection Status* notice shows when the browser is
  offline or the manager is unreachable (network failure or a bare
  502/503/504 from the proxy). Queries pause while offline and refetch on
  reconnect; nothing is queued.
- **Serving** (`internal/manager/server/spa.go`): deep links fall back to
  `index.html`; `/_app/immutable/*` is `immutable`; `index.html`,
  `/service-worker.js`, `/manifest.webmanifest` and icons are `no-cache`;
  missing build-only paths are plain 404s, never the HTML shell.

### Trying the PWA locally

Service workers need a secure context; `http://localhost` counts:

```bash
npm --prefix web run build
go build -o /tmp/docker-manager ./cmd/docker-manager
DOCKER_MANAGER_PUBLIC_URL=http://localhost:8080 DOCKER_MANAGER_LISTEN_ADDR=127.0.0.1:8080 \
  DOCKER_MANAGER_DATA_DIR=/tmp/docker-manager-data /tmp/docker-manager
```

Then check the manifest, service worker, deep-link reloads and the offline
shell by hand in the browser's developer tools. Behaviour behind an HTTPS
proxy and installation on devices are not verified by automated tests.

## Live data (#23)

The root layout starts one live client per tab (`startLive(queryClient)`,
`src/lib/live`, once `MoveGate` knows the manager does not wait for a
move): a single `EventSource` on `/api/v1/live/stream`
([streams.md](api/streams.md#live-invalidation-stream-23)) whose events
invalidate the affected Svelte Query keys. Views do not subscribe to
anything themselves; they only have to

1. **key their queries by the conventions** in `src/lib/live/keys.ts`
   (build them with `liveKeys`):

   | data | key |
   | --- | --- |
   | a list of a topic | `liveKeys.list('stacks', filters)` → `['stacks', 'list', filters]` (refreshed at most twice a second) |
   | an instance-wide resource | `liveKeys.item('stacks', stackId, 'revisions')` → `['stacks', 'item', id, …]` (stacks, jobs, environments, agents, policies, backups, registries, settings, permissions) |
   | a Docker object | `liveKeys.item('containers', envId, name, 'logs')` (containers, images, volumes, networks are named per environment) |
   | charts | `liveKeys.metrics(envId, …)` (new stored samples, every 10 s; the same `metrics` event also refreshes `['overview']`, the dashboard's latest usage; at most every second) |
   | current CPU and memory | `liveKeys.metrics(envId, 'containers-latest')` (`latestContainerMetricsQuery`), `liveKeys.metrics(envId, 'capacity')` and `['overview']`: also refreshed by `live_metrics` events, about every second while the stream is open (the manager asks the agents only then); read current figures from these, never from a chart's last point |
   | a stack's containers | `liveKeys.stackServices(stackId)` (refreshed on container events) |
   | scoped files | `liveKeys.files({kind: 'stack', id: stackId}, 'list' \| 'stat' \| 'content', path)`; volumes use `id: '<envId>/<volume>'` |
   | the caller's permissions | `liveKeys.myPermissions` |
   | the move to a new server | `liveKeys.managerMove(...)` → `['manager', 'item', 'move', …]` (topic `manager`, kind `manager_move`; owner) |
   | the session (its move lock) | `liveKeys.session` (`queryKeys.session`; kind `manager_move_lock`, every signed-in user) |

2. **declare open file views** so their changes arrive and volumes stay
   watched: `liveClient()?.setScopes({ stackIds: [id], volumes: ['e1/pgdata'] })`
   (the client reconnects from its cursor; nothing is lost);
3. **keep editor buffers**: a refetched `content` query with a different
   `etag` is an external change: keep the unsaved buffer, show the
   conflict and save only with `If-Match` of the loaded ETag (the API
   answers `412` with the current ETag; #15). Never overwrite the buffer
   from the query.

What the client does: a non-resumed `hello` invalidates every query (fresh
snapshot); reconnects resume from the last applied cursor with exponential
backoff and jitter (1–30 s); duplicate or older ids are ignored; `reset`
invalidates everything (or one environment's keys); `permissions.changed`
drops every cached query no view shows and refetches the open ones at once
(their denied or not-found result replaces the old data; `clear()` would
leave mounted views showing theirs), refetches `/me/permissions` and
reconnects; `close
session_expired` stops until `liveClient()?.reconnectNow()` (after signing
in); the browser's `offline` event drops the stream at once and `online`
reconnects from the cursor; three failed connections within a minute switch
to polling (details every 10 s, lists and metrics every 30 s) until the
stream is back. Queries the stream keeps current (the current CPU and
memory, the overview, charts) poll on their own only while it is not live:
`refetchInterval: pollWhileDown(ms)` (`$lib/live`) rather than a fixed
interval.

**`liveStatus`** (`src/lib/live/status.svelte.ts`) is the interface for the
shell (#22): reactive `state` (`idle`, `connecting`, `live`,
`reconnecting`, `polling`, `unauthenticated`, `stopped`), `since`,
`lastEventAt`, `failures`, `environments` (`{ [envId]: 'online' |
'offline' }` from agent events), `tooManyStreams` (the manager refused the
stream with 429: EventSource hides the status, so after a failure the
client opens the URL once with fetch, reads the status and aborts; the
shell's banner then asks the person to close tabs) and `stale` (anything
but `live`: data may be behind; keep showing it, say so). Only the live
client writes it.

Tests: `client.spec.ts` (cursor resume, gap and environment resets,
dedupe, revocation clearing, polling, throttling), `keys.spec.ts`
(invalidation map, critical work). Live updates in open screens across
real browser sessions are not verified by automated tests.

## Files, logs and terminals (#15, #8)

Feature code lives in `src/lib/features/{files,logs,terminal}`; the routes
only wire resources to it:

| screen | route | component |
| --- | --- | --- |
| stack files (Files tab) | `(app)/stacks/[stackId]/files` | `FileManager` (+ `LogDock`, the logs as a bottom drawer) |
| volume files | `(app)/volumes/[environmentId]/[volumeId]/files` | `FileManager` (same component, volume scope) |
| stack logs (Logs tab, `?service=` selects one service), container logs | `…/stacks/[stackId]/logs[?service=]`, `(app)/containers/[environmentId]/[containerId]/logs` | `LogPanel` → `LogViewer` |
| logs in their own window | `(popout)/popout/logs?stack=` / `?environment=&container=` | `LogPanel` without the app shell |
| stack terminal (service picker; `?container=` preselects and connects), container terminal | `…/stacks/[stackId]/terminal[?container=]`, `(app)/containers/[environmentId]/[containerId]/terminal` | `TerminalPanel` (`autoConnect` only from such a link) |

- **Height:** the file manager, logs and terminals fill the window below
  their own top without a page scroll (`fillViewport` in `files/fill.ts`,
  keeping `<main>`'s bottom padding; the file manager card always takes
  that whole height). It re-measures on window resizes and when content
  above it appears, disappears or changes size (a job tray, a notice, the
  shell's banners above `<main>`).
- **Files:** `FilesApi` (`files/api.ts`) calls the typed client for either
  root; listings and contents are keyed `liveKeys.files(...)`; the
  `EditorSession` keeps buffers, ETags and conflicts (never replacing
  unsaved text); selection, keyboard and conflict grouping are pure modules
  with Node tests. The editor's Format (YAML, JSON) is a `SplitButton`:
  Minify (JSON only, `minifiable` in `language.ts`, `minifyJson` in
  `$lib/lazy`; simply disabled for YAML) and Beautify (the same as Format); the
  result replaces the text through the editor handle (one undo step,
  the tab turns unsaved), text that does not parse keeps the document
  and shows an error toast. A click that opens a file does not select it;
  permissions and owners are a details view (off by default); the
  toolbar's Upload Files, Upload Folder, New File and New Folder are small
  icon buttons named by their tooltips; below
  1024 px list and editor are `Tabs`. In a stack, saving a Compose source
  validates the definition on disk in the stack's own project directory
  (`POST /stacks/{stackId}/validations`, only with
  `stack.definition.write` in the stack's `actions`; silent when it
  fails) and offers Deploy (the stack page's job tray) while
  `undeployedChanges` is set. `POST /stacks/validations` is the create
  dialog's (a submitted definition, `stack.create`). File operations
  (copy, move, delete, archive, extract, permissions) are jobs:
  `OperationsPanel` shows the root's from the running list
  (`fileJobMatch` in `files/jobs.ts`: `files.*` jobs with the root's
  `stack:<id>`/`volume:<name>` target in its environment,
  `template.files.*` with `template:<id>`), so they come back after a
  reload, and Cancel posts `/jobs/{id}/cancellations` for any of them.
  Uploads are browser requests: their queues live in `uploads.svelte.ts`,
  one per root (`uploadQueue(key)`, `releaseUploadQueue`), so they keep
  running while the user is elsewhere in the app; while one uploads it
  registers `criticalWork` and a `beforeunload` prompt (a reload or
  closing the tab cancels it; no `beforeNavigate` guard, since in-app
  navigation does not).
  Details: [api/files.md](api/files.md#ui-22-23).
- **Logs:** `LogFeed` follows each container's SSE stream with its cursor
  (Follow off/on resumes with `since`, repeats are skipped) and merges them
  by time. Over HTTP/1.1 the browser allows six connections per host for all
  tabs, so a viewer streams one container and polls the others
  (`GET …/logs?since=`, every 3 s); over HTTP/2 or HTTP/3 it streams up to
  twelve. Every service has the same colour (`SERVICE_COLOR`); the prefix
  names the service. The feed strips terminal escape codes
  from each line and gives it a level read from its text (`detectLevel` in
  `logs/level.ts`: `level=`/`"level":` keys and pino's numbers, `[error]`,
  upper-case words such as `WARN`/`ERR`/`LOG:`, glog's `E0925`, `panic:`,
  `ValueError:`; else "Other"); an indented line (`continues`) takes the
  previous level of its container. The search, Services (a `MultiSelect`
  of the stack's services, shown with more than one; `?service=` starts
  with that one, a service that starts later shows unless hidden) and
  Levels filter the buffered lines in that order (`filterLines` in
  `logs/format.ts`); each count applies the other filters' choices, a
  service's also while it is hidden, so the search runs over every
  service's lines. The search (shows
  only matching lines as you type; Match Case; plain text with
  `plainSearch`, regular expressions in a worker, `RegexSearch` in
  `logs/regex-search.svelte.ts` with `regex.worker.ts`: each line is
  searched once, in chunks of at most 1,000 lines; a chunk that runs past
  2 s, counted from the worker's `ready` so a cold start never counts,
  such as a pattern that backtracks for minutes, terminates the worker and
  shows "Too Slow to Search" instead of freezing the page; a pattern that
  throws in the worker, or a worker error, shows "Search Unavailable"; both
  states last until the pattern changes); Levels is a `MultiSelect` of the
  levels and the output streams. The line's left edge marks errors (red),
  warnings (amber) and standard error without a level (grey). Wrapped lines are rendered without the
  fixed-height window.
- **Terminals:** `ExecTerminal` creates the exec session for the chosen
  shell (Detect Automatically, Bash, sh, Zsh; the agent finds its path in the
  container and the session reports the command it started), opens the
  WebSocket with the ticket in the subprotocol, frames stdin/stdout,
  sends `resize` when `TerminalView` (`fit`) changes size, maps close codes
  to messages (4422: "This container has no Bash — choose another shell.")
  and warns after 25 idle minutes.

## Lazy-loaded libraries

CodeMirror, ECharts and xterm.js are reachable only through
`src/lib/lazy/index.ts`:

```ts
import { mountYamlEditor } from '$lib/lazy';
const editor = await mountYamlEditor(element, text, { label: 'compose.yaml', onChange });
// editor.text(), editor.setText(), editor.focus(), editor.destroy()
```

Prefer the `$lib/ui` wrappers `CodeEditor`, `Sparkline` and `TerminalView`;
every mount applies Docker Manager's theme (`codemirror-theme.ts`,
`echarts-theme.ts`, `TERMINAL_THEME`). The code editor binds Tab and
Shift-Tab to indent and outdent (`indentWithTab`); it is still no keyboard
trap: Escape, then Tab within two seconds, moves focus out (CodeMirror's
tab focus mode, also toggled with Ctrl-m).

`verify-build.mjs` fails the web gate if one of them is statically imported
by any entry chunk. Bits UI and Lucide are imported directly (Lucide per
icon: `import X from '@lucide/svelte/icons/x'`).

## Tests

- `npm --prefix web test` (vitest) runs two projects: `unit` (`*.spec.ts`,
  Node: client/Svelte Query integration, service-worker rules, update flow,
  connectivity, manifest, tokens and contrast, hues, guards, stores,
  JobWatcher, WebAuthn/QR helpers) and `components` (`*.test.ts`, jsdom +
  `@testing-library/svelte`: roles, labels, keyboard, focus trap and return;
  setup in `src/test/setup.ts`).
- `web/scripts/verify-build.mjs`: lazy chunks, precache list, manifest and
  icon sizes after the build.
- Go: `internal/manager/server` (`TestPWAAssets`, deep links, caching),
  `web` (`TestAssetsHaveIndex` checks PWA files in a real build).
- There are no browser or end-to-end tests (the Playwright suite was
  removed on 2026-09-25): the service worker in a real browser, the
  proxied origin, accessibility (axe), offline behaviour and full user
  flows are checked by hand only.
