# Web UI (#22)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guides: `docs/internal/design/README.md` (tokens, components, copy, a11y),
`docs/internal/web.md` (client, PWA); live gallery `/design`. Dark-only v1.

- **Components:** build pages only from `$lib/ui` (one barrel) and tokens
  (`$lib/design/tokens.css`); no raw hex, no one-off copies of a component.
  `IconButton` needs `label`; dropdowns are `Select` (a themed Bits UI
  listbox, never a native `<select>`), `Combobox`, `MultiSelect` (several
  choices as switch rows with counts, e.g. the log viewer's Services and Levels) or
  `SuggestField` (free text with suggestions, never a `<datalist>`); every `title`
  attribute shows as a themed tooltip (`TooltipLayer` in the root layout),
  so never build a tooltip by hand; an (i) that explains something is
  `InfoTip` (or `Disclosure`'s `hint` in a summary): an info tip that a
  tap opens too, since phones have no hover; titles and labels take it
  through `info` (`Card`, `PageHeader`, the Field-based controls,
  `Switch`, `Checkbox`, `FieldGroup`), never as a hand-built span;
  optional fields set `optional` (a muted "Optional" after the label),
  never "Optional." in their description; confirmations use `ConfirmDialog` /
  `DestructiveConfirm` (consequences listed in plain words, type-to-confirm
  for high impact), only for damaging actions (stop, take down, delete,
  remove; start, restart and deploy run at once); Start, Restart and Stop
  of one stack or container are one `LifecycleButton`
  (`$lib/features/common`: Stop while anything runs, Start otherwise),
  never separate buttons, and row menus list them in the order Start,
  Restart, Stop; status is `StatusBadge` (dot + text); tags and filter pills
  are `Chip`; lists of short values (template tags, network aliases) are
  entered with `TagInput` (removable chips; Space, Enter, a comma or Tab
  ends one, a paste splits), never as comma-separated text; a list whose
  order people choose is reordered by dragging a grip handle with the
  shared helper (`Sortable` + `DragHandle`: pointer events, so touch
  works; ArrowUp/ArrowDown and Home/End on the handle as the keyboard
  alternative, announced politely), never with HTML5 drag and drop or a
  hand-made drag; schedules show in words (`describeCron`, `ScheduleSummary`)
  with the cron expression as tooltip; long names, images and paths in
  tables use `Column.maxWidth` + `truncate`, wide lists pin their actions
  column (`pin: 'end'`); headings inside a card are
  `h3.subsection-title`. Colours belong to types, never to single items:
  every service uses `SERVICE_COLOR`/`SERVICE_HEX` (`$lib/design/hue`),
  also in merged logs, chart series and filters (they name the
  service); no hashed or per-item colours, except a chart of every
  container of an environment (`MultiSeriesChart`: Beszel's order, on each
  chart the containers ranked by their total over the range get
  `rankColor(rank, n)`, the largest the first colour) and the host's
  Temperature chart (the sensors ranked by their maximum over the range,
  `rankByPeak` in `$lib/features/environments/temperatures.ts`); host
  metrics have one colour per metric, Beszel's (`METRIC_COLORS` in
  `$lib/design/hue`, also on sparklines of the same metric).
  Heavy libraries only through `$lib/lazy` or `CodeEditor`/`Sparkline`/`TerminalView`.
- **Resource icons:** one icon and tile colour per resource type in
  `RESOURCE_ICONS` (`$lib/features/common/resourceIcons.ts`); the object's
  page header tile, its empty states, its ⌘K hits and the sidebar entry
  of a section named after it read it (`{...resourceIcon('volume')}`), so never import a type's Lucide
  icon or pick its colour by hand; a new resource type gets an entry
  first. Every resource list starts each row's name with that icon:
  `NameCell icon="<kind>"`, or `IconCell` around a custom name cell (an
  `IconTile size="xs"`: 24 px tile, 14 px glyph, in a fixed slot, centred
  on the name block). The colour is the type's, as on its page header,
  never per row: the only variations are the ones its page header makes
  (an offline environment is slate, `environmentIcon`; Docker Manager's own
  containers violet). Stacks and services have no icon of their own:
  every service shows the `service` tile, stacks `StackIcon size="xs"`
  (the image of the template the stack was created from, else the stack
  tile); registry connections the key (`KeyRound`, slate); notification channels
  the bell (`Bell`, cyan); alerts the siren (`Siren`, rose); a host's
  disks the hard drive (`disk`: `HardDrive`, slate) and its RAID arrays
  the stacked drives (`raidArray`: `Server`, indigo), on the System tab's
  Disk health and RAID rows and as the icons of the disk health and RAID
  alert kinds (card titles never carry a tile); the Notifications
  nav item the inbox (`notification`: `Inbox`, cyan), notification rows the
  icon of the policy kind that ran them; schedules the
  icon of the policy they run (`scheduleResource`). The icon is
  decorative (`aria-hidden`): the name stays the link and the row's
  accessible label, and the tile never replaces a status or mark.
- **Links** of stacks and templates: show them with `LinkList`
  (`$lib/features/common`: external links in a new tab with `rel="noopener
  noreferrer"`, the label or else the host, in `PageHeader`'s `below`
  row) and edit them with `LinksEditor` (rows from `linkRows`, reordered
  by their grip handle and saved in that order as `cleanLinks`; `links.ts`
  checks the server's rules inline and `serverLinkProblems` places the
  server's field errors on the rows, which follow a row when it moves).
  Never build a link list or a link check by hand.
- **Pages:** signed-in pages in `web/src/routes/(app)/<section>/` (replace
  the `SectionPlaceholder`), public ones in `(auth)`. URLs only from
  `$lib/routes.ts`. Call `usePage({ title, crumbs, environmentScoped })`;
  lists filter by `environmentSelection.id` (null = all). When the caller
  sees exactly one environment (`singleEnvironment()` in
  `$lib/features/common/environments.svelte`, pure `onlyOneEnvironment` in
  `data.ts`), lists hide their Environment column and filter and forms
  their environment picker. Feature view
  models and components live in `$lib/features/<area>/` (pure `*.ts` with
  `*.spec.ts`); only generic pieces go to `$lib/ui` (stacks:
  `$lib/features/stacks/` with `stackKeys`, `model.ts`, `actions.ts`).
  The caller's own account and API tokens are the **Profile**
  (`/profile`, `$lib/features/profile`, from the user menu); **Settings**
  holds instance administration only, so never add a personal page or tab
  there. A page that moves keeps its old address as a `+page.ts`
  redirect that keeps the query string (`/settings/security` → `/profile`).
  The environment route parameter is `[environmentId]`
  (`routes/(app)/environments/[environmentId]`);
  Docker object pages below it must use the same name.
  Every figure links to its list (`KpiCard href`, counts as links that
  select the environment first); a list with filters opens filtered by
  presetting its `ListFilters` before the link is followed
  (`presetFilters` in `$lib/features/dashboard/AttentionStrip.svelte`,
  the dashboard's "Needs Attention"). A whole card or table row opens its
  object through its name link stretched over it (`::after`, the row or
  card `position: relative`, links and buttons inside above it).
  Router errors inside the signed-in area render in the shell
  (`routes/(app)/+error.svelte`), others in `routes/+error.svelte`; both
  use `ErrorPageBody` ($lib/features/common): one heading, Reload (not
  for 404), Back, Go to the Dashboard.
- **Public pages** (`(auth)`): the title is `AuthHeader`
  (`$lib/features/auth`); submit buttons stay enabled (a browser's
  autofill may not report values before the user interacts): controls
  keep `required`, the form is `novalidate`, reads its values from the
  submitted `FormData` and shows what is missing next to the field
  (`requiredErrors`, `submitted` in `$lib/features/auth/validate.ts`).
  Anonymous visitors see the version, never the build commit.
- **Numbers:** every measured value (sizes, rates, percentages, load,
  CPUs, ratios, decimal seconds) goes through the shared formatters
  (`formatNumber`, `formatBytes`, `formatPercent`, `formatValue` in
  `$lib/ui`): up to two decimal places, trailing zeros dropped ("1.5 GB",
  "12.34%", "2 GB"), counts whole; never `toFixed`, `Math.round` or a
  hand-made unit before display (#147; `docs/internal/design/README.md`,
  "Formatting").
- **Charts:** every chart is drawn like Beszel's (`timeSeriesOption` in
  `$lib/lazy`: monotone curves, areas as 1 px lines over `AREA_FILL` or the
  line's `fill`, plain lines 1.5 px); never style a series by hand.
  `TimeSeriesChart` for metric responses (nulls are breaks, gaps shaded
  and listed as text, `stacked` for parts of a whole: Memory),
  `MultiSeriesChart` for many items of one type (stacked, a tooltip naming every item, a `shown` filter greying out
  the rest: the environment's per-container charts, `ContainerCharts`
  after the host charts; `stacked={false}` for values that do not add up,
  plain lines headed by the largest value: the host's Temperature chart,
  last of the host charts and shown only when a sensor has a reading),
  `Sparkline` in KPI cards; metric queries
  keyed with `liveKeys.metrics(envId, …)`. 204 responses: `unwrapEmpty`.
- **Data:** typed client + Svelte Query; a `queryOptions` factory per
  resource in `src/lib/api/queries.ts` keyed with `liveKeys` ([live-sync.md](live-sync.md)) so live events refresh it; mutations invalidate by prefix, never
  retry; views never read the stream. Jobs: `JobProgress` / `JobWatcher`;
  every progress UI restores from the running list (`activeJobsQuery`,
  `useTrackedJobs` + `matchJob`, `ActiveJobs`; docs/internal/web.md, "Job
  progress after reload"), so it survives a reload and coming back: never
  keep a job ID only in component or module state (add the job you started
  with `tracked.add`), and never copy the active states
  (`ACTIVE_JOB_STATES` in `$lib/api/job-states.ts`);
  job rows lead with the target's name (`jobHeadline(job, { nameOf,
  fallback })` in `$lib/features/jobs/labels.ts`: stack IDs resolve with
  `stackNames(stacks)`, opaque IDs are never shown). Job kinds are named
  by `jobKindLabel` (`JOB_KIND_LABELS`, Title Case: "Deploy Stack"); a
  sentence (the bell's job notices, the job page's summary) uses
  `jobKindPhrase`, the same label in sentence case ("Deploy stack"). The runs of a policy
  (an update check starts one job per target) show one line each
  (`groupRuns`, `runSummary` in `runs.ts`: "20 checks, all succeeded",
  "2 of 20 checks failed"; `RunsTable`), linking to the failed job or to
  the policy's jobs (`routes.jobs(kind, { policyId })`); a policy page
  loads its runs with `recentJobsQuery(n, { policyId })`; the job page
  states the job once (`JobProgress summary={false}`), a failure as a
  headline in words (`jobErrorHeadline`) over the engine's message, and
  "Try Again" (`jobRetry`): a retry through `POST /jobs/{jobId}/retries`
  when the job is `retryable` (opens the new job; a retry shows "Retry
  Of"), else the page of the originating action (`jobAgain`).
  Feature screens may keep their factories in `$lib/features/<area>/queries.ts`
  (docs/internal/web.md, "Feature modules"); step-up-guarded calls go through
  `withStepUp` (`$lib/auth/stepup.svelte`). Confirming identity (the
  step-up dialog, the sign-in second step) asks for one factor chosen by
  `$lib/auth/verify.ts`: passkey (its prompt started at once), then
  authenticator code, then password, with a switch to any other factor
  the account has; never two at once.
  Never store API data in `localStorage`/Cache Storage.
- **Permissions:** show actions from DTO `actions`/`view`; navigation from
  `/me/permissions` (`$lib/shell/nav.ts`); hide, don't disable; Restricted
  users get `DeniedState`. The server still decides.
- **Copy:** buttons name the result and the toast repeats it ("Deployed
  Silo"); errors say what happened and what to do, no apology; empty states
  invite action; no all-caps labels. Keep it lean (#233; design guide,
  "Copy rules"): no description that restates a title, label, column or
  placeholder; occasional explanations go behind `info`; what prevents
  data loss, secret exposure or a misread result stays visible. **Names
  and labels are Title Case** (#219): navigation, page titles and crumbs, card, section and dialog
  titles, tabs, column headers, field, checkbox, switch and option labels,
  menu items, buttons, badges and status labels, empty-state titles, filter
  labels, job kind labels and short label maps ("Image Updates", "Last
  Used", "Add Connection", "What to Send"). Capitalize every word except
  articles (a, an, the), coordinating conjunctions (and, but, or, nor) and
  short prepositions (as, at, by, for, in, of, on, per, to, via, with),
  unless the word is first or last; each part of a hyphenated word
  ("Sign-In"). Product names, acronyms and literal values keep their own
  casing (Docker Manager, GHCR, RAID, SMART, API, URL, `docker-compose.yml`,
  `pull: always`, image references, cron expressions). Sentences stay in
  sentence case: descriptions, help text, hints, placeholders, tooltips
  that are sentences, toasts, errors, confirmations, empty-state bodies and
  anything ending in ".", "!" or "?". The manager's notification labels
  follow the same rule (`alerts/message.go`).
- **Tests:** unit tests only: `*.spec.ts` (Node logic), `*.test.ts` (jsdom
  components with `@testing-library/svelte`: roles, labels, keyboard,
  focus). Components reading queries: `web/src/test/QueryHarness.svelte`
  with a stubbed `fetch`. Every `routes.*` builder needs a page
  (`src/lib/routes.spec.ts`). There are no browser, accessibility (axe) or
  end-to-end suites; do not add them without an explicit request.
- **Run and look:** `npm --prefix web run build`, then run the manager
  (`go run ./cmd/docker-manager`, `docs/internal/development.md`); it needs no
  Docker, but environments need an agent on a Docker Engine. Review at
  1440×900 and 390×844 against #22 (screenshots outside the repo; never
  commit the mockup or screenshots of it).
- **Resource pages (#6, #19, #33):** `$lib/features/resources`:
  `useEnvironmentScope()` (selected or all environments, create rights),
  cross-environment lists (`acrossEnvironments` in `$lib/api/multi-env.ts`;
  offline environments are reported, not hidden), `trackJob` (toast, notice
  and refresh when a mutation's job ends), `refusal`/`jobFailure` (#32 and
  other refusals with the server's reason), `RemovalDialog` (the server's
  removal preview). Credential changes go through `withStepUp`
  (`$lib/auth/stepup.svelte`). `unwrap` resolves a 204 to `undefined`.
  Section lists (containers, images, volumes, networks, stacks, jobs,
  schedules) are one `ListCard` ("All Containers", count, search, selects
  and switches in the header, "Clear Filters", `NoMatches`); their filters
  are `ListFilter`s in pure, spec-tested modules and their state a
  `ListFilters` store (per list and browser tab in `sessionStorage`, UI
  state only). Keep filters few: text attributes (names, images, digests,
  labels as `key=value`, networks, addresses) belong in the search; offer
  selects only for status-like attributes (status, stack, driver, kind,
  environment while all are shown) and switches, off by default, for
  yes/no narrowing ("Unused", "Managed", "Updates"). Column order follows
  what people scan: identity (name with its image), status, grouping
  (stack, environment), live figures (CPU, memory, uptime), wiring
  (volumes, networks, ports), then sizes and dates. An image's update state is the
  `ImageUpdateBadge` icon next to the image (it checks the covering
  policy again, spinning while the job runs), never its own column;
  networks show with `NetworkList` (linked, with their addresses).
  Rows stay one line high: marks sit beside the name (Docker Manager's own
  objects: `ProtectionMark`; detail pages: `ProtectionBadge` with a plain
  `label`), the stack is its own column, a column that repeats one value
  on every row (every volume "local") is hidden (`sameEverywhere`), and
  untagged images fold into a section at the end (`splitUntagged`).
  Phones show the name, status and one key figure (other columns
  `stack: 'hidden'`; the row menu and other icon-only row actions
  `stack: 'head'`; the status, a start time or badges beside the
  name `stack: 'status'`, which moves below a long name). Rows are selectable:
  `ContainerBulk` / `ObjectBulk` show the selection bar (`BulkBar`) and
  confirm with what runs and what is left out and why (`BulkConfirm`;
  pure plans and the summary in `bulk.ts`: Docker Manager's own objects,
  stack-managed containers and objects in use are reported, never
  dropped silently); `runBulk` (`bulk-run.ts`) sends one request per
  object through the single-object helpers (`runContainerAction`,
  `object-actions.ts`) and shows one summary toast. A container's header
  shows, after its `LifecycleButton`, **Pause** (running) or **Unpause**
  (paused) and **Settings** (cog; restart policy and limits) as buttons,
  never in the menu; on phones Settings shows only its cog
  (`Button iconOnPhones`) so the header stays one row. Detail pages: removal
  is the last entry of the header's "More Actions" menu after a separator
  and absent for Docker Manager's own objects (the notice says why); no
  removal-preview card on the page (the removal dialog shows the
  server's preview); labels through `LabelsCard` (system labels such as
  `com.docker.compose.*` and Docker Manager's `docker-manager.*` and legacy
  `dev.neureka.docker-manager.*` folded, `isSystemLabel`; the user-set
  `docker-manager.*.exclude` labels stay visible); technical detail (command, entrypoint,
  health check command, IDs) behind "Advanced"; restart policies and
  health in words (`restartPolicyLabel`, `healthLabel`); metrics with
  `TimeSeriesChart`; tabs of one object keep one breadcrumb trail
  (section / environment / name / tab).
- **Search:** `GET /api/v1/search` (`internal/manager/api/search.go`) feeds
  the ⌘K palette; new searchable resource types go there, filtered with the
  resource's own `ViewOf` and identity/status fields only. Without a query
  the palette shows "Recent" (visited paths per tab in `sessionStorage`,
  titles in memory only: `$lib/shell/recent.svelte.ts`), "Actions" the
  caller may start (`PALETTE_ACTIONS` in `palette.ts`, hidden without the
  capability) and the pages (`palettePages`: the visible sidebar pages
  plus `ACCOUNT_ITEMS`, the Profile; `NavItem.keywords` let a query such
  as "password" find the page that holds it).
- **Policy pages** (updates, maintenance, backups): `PageHeader` with the
  policy name, one status sentence as description and the actions
  (primary "Run Now" or "Preview Updates", then "Check Now"/"Preview",
  "Edit" (the only edit entry), the rest in the ⋯ menu, destructive
  last); then `KpiRow` (last run, next run, coverage, one value of the
  policy's own); then the cards "What It Covers", "Schedule" and "Recent
  Runs". Edit forms are one dialog with Cancel and Save Changes; their
  cron field shows only while the schedule is on. Links to a policy go to
  the policy itself (`policyHref(kind, policyId)`, `policyPage(kind,
  policyId)`), never only to its section. Update targets show by name
  (`TargetName`: a container is looked up by name or Engine ID, never
  shown by ID); inactive targets are "Excluded" or "No Longer Found" as
  the manager's `inactiveReason` says (never guessed from the exclusion
  lists), and every count on Updates counts the covered
  targets the policy pages count ("6 images in 5 stacks"). A newer image
  shows when it was published when the registry says so (`publishedText`:
  "published 3 days ago", the date as tooltip).
- **Notices and the bell** (`$lib/shell/notices.svelte.ts`,
  `NoticesBell.svelte`, #159): the bell lists the firing, undismissed
  alerts the caller sees (the active alerts query) and this tab's notices
  of the user's own manual jobs; every item links somewhere
  (`noticeHref`). The badge counts the items not dismissed and stays until
  each is dismissed (closing the popover changes nothing). Dismiss (×) and
  "Dismiss All" dismiss an alert for everyone when its `actions` hold
  `alert.dismiss`, else for this browser only; job notices are dismissed
  for this browser. Browser-local dismissals are keys only
  (`job:<id>`, `alert:<id>:<escalation>`, so an alert that gets worse, at
  any severity, shows again) in
  `localStorage` `docker-manager:dismissed-notices` (at most 200; UI
  state, never API data). Offline environments and available updates are
  alerts now: never compute them in the browser again. Policies are named
  as users know them with `policyLabel` (the manager's `targetName`, the
  stack or container).
- **Notifications page** (`routes/(app)/notifications`, nav
  "Notifications"): `Tabs` in the URL (`?tab=alerts`), **Notifications**
  first (default), then **Alerts** (there is no `/alerts` page:
  `routes.alerts()` returns `/notifications?tab=alerts`);
  `routes.notificationHistory()` is the first tab (`routes.notifications()`
  stays Settings → Notifications).
- **Notifications tab** (`$lib/features/notification-history`): one
  `ListCard` of finished runs grouped by day ("Today", "Yesterday", then
  the date; `groupByDay` in the viewer's time zone), filters kind,
  outcome, environment; the outcome is a badge ("Done", "Warning",
  "Failed"); the title links to the job, the detail and the fields
  follow (inline ones in one line, the others, such as a prune's
  breakdown, on their own). Queries are keyed
  `['notifications', 'list', …]`, refreshed by topic `alerts` kind
  `notification` only.
- **Alerts tab** (`$lib/features/alerts`): one `ListCard` (state
  Active/Dismissed/Resolved, kind, environment; `alertFilters`, presets
  through `alertsPreset` for links from "Needs Attention" and the System
  tab's `AlertMark`); severity is a `StatusBadge` ("Critical", "Warning",
  "Info"); rows lead with the alert's title linking to its `link`, then
  the detail and a line with the kind's icon and its inline fields;
  Dismiss only with `alert.dismiss` in `actions`. Queries are keyed
  `liveKeys.alerts(...)` (topic `alerts`).
- **Alert Thresholds** (`ThresholdsCard`, Settings → Notifications, owner
  only): the defaults in one grid (`ThresholdGrid`, validated by
  `thresholds.ts` like the server: whole numbers, 0 off, warning below
  critical), overrides in a table with `OverrideDialog`; every save PUTs
  the whole settings with If-Match.
- **File picker:** choosing files or folders from a listing (a backup's
  contents today) goes through the shared `FilePicker`
  (`$lib/features/common/FilePicker.svelte`, pure logic in
  `filePicker.ts`): a dialog like a desktop "Open" dialog with the places
  on the left, crumbs, a filter and the folder's entries, never above a
  place; one file (a listbox: arrows, Enter, Backspace) or `multiple`
  (tri-state ticks, a ticked folder chosen whole). A feature supplies its
  places and a `PickerSource` (one query per folder, absolute paths);
  never build another tree or inline browser to pick paths.
- **Files, logs, terminals** (`docs/internal/web.md`): reuse
  `$lib/features/files/FileManager.svelte` (stack, volume or template
  scope),
  `$lib/features/logs/LogPanel.svelte` (stack or container; `LogDock` as a
  bottom drawer) and `$lib/features/terminal/TerminalPanel.svelte`; link to
  them with `routes.stack(id, 'files' | 'logs' | 'terminal')`,
  `routes.volumeFiles`, `routes.containerLogs`, `routes.containerTerminal`.
  Log viewers stream at most one container over HTTP/1.1 (six connections
  per host for all tabs) and poll the rest.
