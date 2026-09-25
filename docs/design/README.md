# DockYard design system (#22)

The web UI's visual language and component library. The source is the #22
mockup (stack detail "Silo", dark, wide desktop; attached to the issue only,
never committed) and the design decisions in #25 Q8: **dark-only v1**, no
"PRO" badge, and the narrow-layout rules below. Everything here is code in
`web/src/lib/design` (tokens, hues) and `web/src/lib/ui` (components); the
live gallery is **`/design`** in any running DockYard (public, sample data
only).

- [Principles](#principles)
- [Tokens](#tokens)
- [Service hue identity](#service-hue-identity)
- [Type](#type)
- [Layout and responsive rules](#layout-and-responsive-rules)
- [Components](#components)
- [Adding a page](#adding-a-page)
- [Data, permissions and live updates](#data-permissions-and-live-updates)
- [Copy rules](#copy-rules)
- [Accessibility floor](#accessibility-floor)
- [Tests and screenshot review](#tests-and-screenshot-review)

## Principles

- **Show what runs where and whether it is healthy, then let the user act
  safely.** Dense, exact numbers; destructive actions always confirmed with
  their consequences listed.
- **Quiet chrome, one memorable thing.** Cool blue-black surfaces separated
  by 1 px borders and surface steps (no shadows on cards), blue for primary
  actions, green/red/amber only for status. The single expressive system is
  the service hue (below).
- **Tokens, not values.** Components read CSS custom properties from
  `tokens.css`; a new value used twice becomes a token first. A light theme
  would be a second `:root` block, never a component change.
- **No one-off page CSS for things the library has.** If a page needs a
  variant twice, it belongs in `$lib/ui`.

## Tokens

`web/src/lib/design/tokens.css` (imported once by `global.css` in the root
layout). `design.spec.ts` pins the sampled values and checks contrast.

### Surfaces (cool blue-black, not neutral grey)

| token | value | use |
| --- | --- | --- |
| `--surface-canvas` | `#0b1016` | page background, gaps between cards |
| `--surface-shell` | `#0e141d` | sidebar and top bar (and the PWA theme colour) |
| `--surface-panel` | `#10161d` | cards and panels |
| `--surface-raised` | `#151b24` | table header, inputs, secondary buttons, menus |
| `--surface-search` | `#141a22` | the top bar's search field |
| `--surface-hover` | `#1a212b` | hovered or selected row |
| `--surface-selected` | `#112745` | active nav item, current tab |
| `--surface-selected-strong` | `#162a51` | selected file row |
| `--border-subtle` | `#1c2430` | card and panel edges |
| `--border-strong` | `#263041` | inputs, dividers, floating layers |

### Text

| token | value | contrast on panel | use |
| --- | --- | --- | --- |
| `--text-strong` | `#f2f4f7` | 16.5:1 | titles, KPI values, primary cells |
| `--text-default` | `#c8d3e2` | 12:1 | body and table text |
| `--text-muted` | `#8392a8` | 5.75:1 | labels, secondary lines, table headers, placeholders, timestamps |
| `--text-faint` | `#596476` | 3.0:1 | decorative only (separators, disabled) — never text that must be read |

### Accent and status

| token | value | use |
| --- | --- | --- |
| `--accent` / `--accent-hover` | `#2566fd` / `#3d78ff` | primary buttons |
| `--accent-text` | `#52a3f7` | links, port links, hashes, active nav, focus ring |
| `--accent-soft` | `#112745` | active/selected backgrounds |
| `--ok` / `--ok-soft` / `--ok-border` | `#4cf683` / `#0f2a1f` / `#1d4a33` | running, healthy |
| `--danger` / `--danger-soft` / `--danger-border` | `#fd6b66` / `#3f2029` / `#5a2a33` | destructive, failed |
| `--warn` / `--warn-soft` / `--warn-border` | `#f5b544` / `#33280f` / `#4d3c14` | undeployed changes, update available, degraded, partial |
| `--info` / `--info-soft` | `#2bb0f6` / `#0f2533` | CPU series, informational notices |
| `--offline` / `--offline-soft` | `#8392a8` / `#1a212b` | offline environments (always with the word "Offline") |

### Category tiles

`--tile-<color>-bg` / `--tile-<color>-fg` for `blue` (stacks, web,
services), `cyan` (CPU), `indigo` (memory), `green` (uptime, healthy),
`violet` (deploys, revisions), `teal` (databases), `rose` (cache), `slate`
(workers, unknown). `TILE_HEX` in `hue.ts` mirrors them for canvases.

### Shape, spacing, elevation, motion, focus

| token | value | use |
| --- | --- | --- |
| `--radius-sm` / `-md` / `-lg` | 6 / 8 / 12 px | badges and inputs / buttons and tiles / cards and panels |
| `--space-1 … --space-12` | 4 px steps | card padding 16–20 px, 16 px gaps between cards |
| `--shadow-float` | `0 12px 32px rgb(0 0 0 / .45)` | menus, popovers, dialogs, toasts only |
| `--duration-fast` / `-open` / `-pulse` | 120 / 180 / 600 ms | hover and press / menus, drawers, dialogs / the live-change pulse |
| `--focus-ring` | 2 px `--accent-text`, 2 px offset | every interactive element (`:focus-visible`) |
| `--sidebar-width` / `--sidebar-rail` / `--topbar-height` | 224 / 64 / 52 px | shell |
| `--control-height` / `-sm` / `--touch-target` | 36 / 30 / 40 px | controls; 40 px minimum on coarse pointers |
| `--z-sticky … --z-tooltip` | 10 … 80 | stacking order |

Editor, terminal and chart colours are `--code-*` tokens, mirrored in
`web/src/lib/lazy/palette.ts` for CodeMirror, xterm.js and ECharts.

**Motion:** the one attention motion is the live-change pulse: add
`data-changed` to a changed cell or row for one render (Table does it for
keys in its `changed` prop). `prefers-reduced-motion` turns it into a static
marker and zeroes all durations.

## Service hue identity

The memorable thing: **every service keeps one colour everywhere** — its
icon tile in the services table, its name in the log viewer, its chart
series and its filter chip — so "silo-db" can be followed across Overview,
Logs and Metrics without reading.

```ts
import { serviceIdentity, serviceHue, serviceSeriesColor, TILE_HEX } from '$lib/design/hue';
import { serviceIcon } from '$lib/design/icons';

const id = serviceIdentity({ stackId, name: 'silo-db', image: 'postgres:16', icon: meta?.icon });
// → { icon: 'database', color: 'teal' }
<IconTile icon={serviceIcon(id.icon)} color={id.color} size="sm" />
<span style="color: {TILE_HEX[id.color].fg}">silo-db</span>        // log prefix
series.color = serviceSeriesColor(stackId, 'silo-db');              // ECharts
```

- The hue is FNV-1a of `stackId/serviceName` onto the eight tile colours:
  stable across sessions and browsers.
- An explicit icon in the service's display metadata (#7) keeps its
  **category** colour (`SERVICE_ICON_CATEGORY`); otherwise the icon comes
  from the image heuristic (`postgres` → database, `redis` → layers,
  `nginx`/`web` → globe, `worker` → cog, …) and the colour from the hue.
- Only names in `SERVICE_ICONS` are valid icon overrides.

## Type

Inter Variable for UI text, JetBrains Mono Variable for code, logs, digests,
IDs, paths and terminals; both self-hosted (`@fontsource-variable/*`, OFL-1.1,
ADR 0002) so the offline PWA shell renders correctly.

| role | size / line height | weight |
| --- | --- | --- |
| page title | 28 / 34 | 600 |
| KPI value | 20 / 28 | 600, tabular |
| section title | 16 / 24 | 600 |
| control | 14 / 20 | 500 |
| body and tables (the dense default) | 13 / 20 | 400 |
| caption, meta | 12 / 16 | 400 |
| mono | 12.5 / 20 | 400 (`.mono`) |

Numbers in tables, KPIs and logs use `.num` (`tabular-nums`). Sentence case
everywhere; no all-caps labels, no tracked-out eyebrows.

## Layout and responsive rules

```
┌────────────┬──────────────────────────────────────────────────────┐
│ DockYard   │ ☰  homelab / Stacks / Silo   … [Search… ⌘K]  🔔 (U) │ 52 px
│ [Env ▾]    ├──────────────────────────────────────────────────────┤
│ nav        │ page content, 24 px side padding                     │
└────────────┴──────────────────────────────────────────────────────┘
  224 px
```

| width | shell | tables | dialogs |
| --- | --- | --- | --- |
| ≥ 1280 px | full sidebar (collapsible to the rail, remembered in `localStorage` as a UI preference) | table | centred |
| 1024–1279 px | 64 px icon rail with tooltips; environment switcher as an icon button | table | centred |
| 768–1023 px | off-canvas navigation drawer; breadcrumbs and search icon stay | table | centred |
| < 768 px | drawer | **stacked row cards** (title and status first, key metrics as label/value pairs, actions last) | full screen |

Touch targets are at least 40 px on coarse pointers, and every right-click
action (ContextMenu) has a visible alternative (the row's overflow Menu).
The file manager (#15) shows tree and editor as switchable panes below
1024 px, never both squeezed.

## Components

Import from `$lib/ui` (one barrel). Snippet props (`trigger`, `children`,
`actions`, `cell`, …) are Svelte 5 snippets. Everything below is shown in
`/design` and covered by `*.test.ts` next to it.

### Actions

| component | key props | notes |
| --- | --- | --- |
| `Button` | `variant: primary \| secondary \| ghost \| danger \| danger-soft`, `size: sm \| md`, `icon`, `iconEnd`, `loading`, `href`, `block`, `ref` | Labels name the result ("Deploy", "Save changes"). `loading` keeps the label, sets `aria-busy`, disables. `href` renders a link. |
| `IconButton` | **`label` (required)**, `icon`, `variant: ghost \| secondary \| danger-soft`, `size`, `pressed`, `badge`, `tooltip`, `tooltipSide`, `href`, `external` | `label` is the accessible name and the tooltip; spread menu/popover trigger props onto it. No tooltip while its popup is open. With `href` it renders a link with the button's look (row actions such as "Open silo-web"; `external` opens a new tab with `noopener`). |
| `SplitButton` | `label`, `icon`, `onclick`, `items: MenuEntry[]`, **`menuLabel`**, `variant`, `loading` | The stack header's Deploy. |
| `Menu` | `items: MenuEntry[]`, `trigger` snippet `(props)`, `label`, `align`, `side`, `open` | Bits UI DropdownMenu: keyboard, typeahead, focus return. |
| `ContextMenu` | `items`, `label`, `children` snippet `(props)` | Right-click / long-press; always duplicate its items in a visible Menu. |
| `CopyButton` | `value`, `what` ("request ID"), `text` | Announces "Copied …". |

`MenuEntry` = `{ label, icon?, onSelect?, href?, tone?: 'danger', disabled?, shortcut? }`
\| `{ separator: true }` \| `{ heading }`.

### Display

| component | key props | notes |
| --- | --- | --- |
| `Badge` | `tone: neutral \| accent \| ok \| warn \| danger \| info \| offline`, `dot`, `pulse` | |
| `StatusBadge` | `status` (API state), `kind: resource \| job`, `label` | Dot **and** text; vocabulary in `status.ts` (`statusInfo`). Job `partial` reads "Partly failed". |
| `Card` | `title`, `level`, `subtitle`, `actions`, `padding: none \| md`, `id` | Tables use `padding="none"`. |
| `KpiCard` | `label`, `value`, `unit`, `secondary`, `icon`, `color`, `tone`, `sparkline` / `bar` snippets, `changed` | Row of KPI cards: `KpiRow` (`repeat(auto-fit, minmax(210px, 1fr))`, two per row below 768 px). The card is a size container: at 230 px or less it switches to the compact layout (36 px tile, 18 px value that may wrap, 12 px label), so the stack overview keeps the mockup's six cards in one row from about 1120 px of content (1440 px screens) and phones show two per row. |
| `IconTile` | `icon`, `color: TileColor`, `size: sm \| md \| lg` | Decorative (the adjacent text names the thing). |
| `Meter` | `value`, `max`, `label`, `valueText`, `warnAt`, `dangerAt` | `role="meter"`. |
| `PageHeader` | `title` (h1), `description`, `icon`, `color`, `meta: MetaItem[]`, `status` / `actions` snippets | Icon-led meta items with thin dividers (not middle dots). `MetaItem.title` is the full value on hover, `MetaItem.copy` adds a copy button (the stack's host path). The icon tile marks one object (a stack, container, environment, policy, job): section pages (Containers, Jobs, …) and create forms have none. |
| `Table` | `rows`, `columns: Column<T>[]`, `rowKey`, **`label`**, `sort` (bindable), `manualSort` + `onsort`, `selectable` + `selected` (bindable) + `rowLabel`, `changed`, `maxHeight`, `virtualizeAfter` (500), `rowHeight`, `layout`, `empty` | Sortable headers with `aria-sort`, sticky header inside `maxHeight`, stacked cards < 768 px (`Column.stack`: title, status, meta, actions, hidden), windowed rendering past 500 rows (`virtualWindow`, `aria-rowcount`/`aria-rowindex`). |
| `Tabs` | `items: TabItem[]`, `value` (bindable), **`label`**, `panel` snippet `(id)` | In-page tabs (Bits UI). |
| `TabNav` | `items: TabLink[]`, `current` (path), **`label`**, `after` snippet | Route tabs (stack detail); the URL is the state. Below 768 px the tabs scroll sideways with the current one kept in view, and `after` gets its own line. |
| `DiffView` | `title` (file), `before`, `after`, `beforeLabel`, `afterLabel`, `context` | Unified line diff (`diff.ts`, Myers) with old/new line numbers, `+`/`−` markers and screen-reader "Added:"/"Removed:" (never colour alone); unchanged regions collapse behind "Show N unchanged lines". |
| `Breadcrumbs` | `items: Crumb[]` | The shell renders them from `usePage`. |
| `Skeleton`, `Spinner`, `Kbd` | | Loading regions set `aria-busy`. |

`Column<T>` = `{ id, header, cell?: Snippet<[T]>, sortValue?, align?, width?, numeric?, mono?, hideHeader?, stack? }`.

### Forms

All fields render label, description and error through `Field` (the control
gets `id`, `aria-describedby`, `aria-invalid`). Required controls carry
`required`; optional ones say "Optional." in their description.

| component | notes |
| --- | --- |
| `TextField` | `mono` for identifiers and paths; `bind:value`. |
| `PasswordField` | Reveal toggle ("Show password"/"Hide password", `aria-pressed`); `autocomplete: current-password \| new-password`. |
| `TextArea`, `Select` (native), `Combobox` (Bits UI, filtered, `options: SelectOption[]`) | |
| `Checkbox` | Native; `indeterminate`; `hideLabel` for row selection. |
| `Switch` | `role="switch"`; for settings that apply immediately. |
| `RadioGroup` | Native radios in a fieldset. |
| `TriState` | Inherit / Allow / Deny (#17 user overrides) with the effective decision and its source explained; `highRisk` marks Allow. |
| `CronField` | Cron + IANA time zone; next runs and DST notes from `POST /api/v1/schedules/previews` (the one parser, #13), debounced; server validation shown inline. |

Map server validation errors with `fieldError(err, 'body.name')`.

### Overlays

| component | notes |
| --- | --- |
| `Dialog` | `open` (bindable), `title`, `description`, `size`, `footer` snippet, `trigger` snippet, `dismissible`, `alert` (Bits UI AlertDialog: `role="alertdialog"`). Focus trapped, Escape closes, focus returns to the opener; full screen < 768 px. |
| `ConfirmDialog` | `message`, `consequences[]`, **`confirmLabel`** (the action, never "OK"), `tone`, async `onconfirm` (progress on the button, failures shown inline, stays open). |
| `DestructiveConfirm` | Type-to-confirm (`confirmText`, usually the resource name), `consequences[]`, `affected: AffectedResource[]`, optional `extra` snippet (e.g. the archive dialog's "migrate stacks first" offer). |
| `Drawer` | Side or bottom sheet (`side`, `size`, `hideTitle`); the narrow navigation, detail panes, the log drawer. |
| `Popover` | Non-modal (`label`, `trigger` snippet): notices, environment switcher. |
| `Tooltip` | `text`, `trigger` snippet `(props)`. Supplements names; never the only name. |

### Feedback and states

| component | notes |
| --- | --- |
| `toast.success / error / info / warn(title, { body, action, timeout })` + `<Toaster />` (root layout) | Errors stay until dismissed; polite and assertive regions. |
| `Notice` | Inline or `bar` banner: `tone`, `title`, `live`, `actions` (e.g. the external-change conflict: Compare, Reload from disk, Save as…, Overwrite). |
| `EmptyState` | Invites action: title, description, `actions`. |
| `ErrorState` | From the API error shape: message, `code`, request ID with copy, Retry when retryable or a network failure. |
| `DeniedState` | The Restricted user's state (brief copy). |
| `OfflineEnvironment` | "homelab is offline", since when, what it means. |
| `JobProgress` | `jobId` (follows `/api/v1/jobs/{id}/events/stream` via `JobWatcher`, polling fallback) or a `watcher`; `inline` or `panel`; per-item results, partial failure summary, recovery advice, completion announced; `onfinish`. |
| `SecretReveal` | One-time secrets: copy, download, fingerprint, "I stored it" gate; dropped from the page after Continue. |
| `StepWizard` | Numbered steps, `onnext` validation (throw or return false), focus to the step heading, `onfinish`; `canGoBack={false}` hides Back once the wizard started a job. |

### Lazy surfaces

`CodeEditor` (CodeMirror + `codemirror-theme.ts`), `Sparkline` (ECharts,
nulls as gaps), `TerminalView` (xterm.js + `TERMINAL_THEME`). They mount the
libraries through `$lib/lazy` only (checked by `verify-build.mjs`).
`mountLineChart(el, name, points, unit)` returns `{ update(series), resize() }`
for charts; series colours come from `serviceSeriesColor`.

`TimeSeriesChart` (`title`, `timestamps`, `lines: ChartLine[]`, `unit`:
`percent | bytes | bytes_per_second | load | count`, `from`/`to`, `yMax`,
`detail`) draws metric responses as they come from the API: nulls stay
breaks, runs of missing samples are shaded **and** listed as text under
the chart ("No samples since 12:40": offline intervals, #5), several
lines get a text legend with their latest values, the figure is labelled
with the latest value for assistive technology. Pure helpers in
`$lib/ui/timeseries.ts` (`gapIntervals`, `latestValue`, `formatValue`);
the ECharts option is `timeSeriesOption` in `$lib/lazy` (unit-tested).
Charts and sparklines apply data that arrives while ECharts is still
loading.

### Formatting

`formatBytes` (312 MB, 1.8 GB), `formatPercent` (12.4%), `formatDuration`,
`formatRelative(iso, now)`, `formatDateTime(iso, zone)`, `shortId`.

## Adding a page

1. Create `web/src/routes/(app)/<section>/+page.svelte` (signed-in pages)
   or a public page under `(auth)`. Replace the section's
   `SectionPlaceholder` when you build it. URLs come from `$lib/routes.ts`
   (`routes.stack(id, 'files')`, `routes.container(env, id)`); add new ones
   there.
2. Register title and breadcrumbs:

   ```ts
   import { usePage } from '$lib/shell/page.svelte';
   usePage(() => ({ title: stack.name, crumbs: [{ label: 'Stacks', href: routes.stacks() }, { label: stack.name }], environmentScoped: true }));
   ```

   `environmentScoped` prepends the selected environment (the switcher).
3. Build the page from `$lib/ui`: `PageHeader` (h1) → `TabNav` or KPI row →
   `Card`s with `Table`s. Headers: section pages have no icon tile, object
   pages have the object's tile (stacks: the blue stack tile unless the
   user chose an icon), create pages repeat the button that opens them as
   their title ("Create update policy", "Build image"). Loading: `Skeleton` in an `aria-busy` region.
   Failure: `ErrorState` with `onretry={() => query.refetch()}`. Nothing
   yet: `EmptyState` with the action. Forbidden: hide the control (the
   server answers 403/404 anyway).
4. Lists filter by `environmentSelection.id` (null = all environments).
5. Build the UI, run the manager and look at it at 1440×900 and 390×844
   ([Tests and screenshot review](#tests-and-screenshot-review)).

## Data, permissions and live updates

- **Data:** the typed client and Svelte Query only (`docs/web.md`). Every
  resource gets a key in `queryKeys` (first element = the resource name,
  e.g. `['stacks', 'detail', id]`) and a `queryOptions` factory in
  `src/lib/api/queries.ts`. Mutations invalidate by prefix and never retry.
- **Live updates (#23):** views never read the stream. Key queries with
  `liveKeys` (`src/lib/live/keys.ts`; `docs/web.md`, "Live data") and the
  tab's live client refreshes them; register unsaved work with
  `criticalWork`. The client reports its state in `liveStatus`, which
  drives the top-bar indicator and the offline banner after 5 s down
  (`src/lib/shell/live-banner.ts`).
- **Permissions (#17):** render what the API says the user may do: resource
  DTOs carry `view` and `actions`; show an action only when
  `actions.includes('stack.deploy')`. Navigation uses `GET /me/permissions`
  (`accessOf`, `visibleNav`, `hasAny`, `isRestricted` in
  `src/lib/shell/nav.ts`). Hidden, not disabled. A Restricted user sees
  `DeniedState` on the dashboard.
- **Sessions:** any 401 while signed in drops every cached API response
  and goes to sign-in with the current path (`src/lib/auth/session.ts`);
  never persist API data in `localStorage` (the only stored values are the
  selected environment ID per user and the sidebar rail preference).

## Copy rules

- Buttons name the result; the toast repeats it: Deploy → "Deployed Silo",
  Save → "Saved compose.yaml".
- Errors say what happened and what to do next, without apology: "Silo can't
  be deployed because homelab is offline. It will reconnect automatically;
  try again when it's back."
- Destructive dialogs list exactly what happens: "Removes 3 containers.
  Volumes and files are kept."
- Empty states invite action: "No stacks on homelab yet. Create a stack or
  import an existing Compose project."
- Sentence case; plain verbs; no "Submit", "OK" or "Oops"; name things as
  the user sees them ("environment", not "agent session").

## Accessibility floor

WCAG AA contrast (checked in `src/lib/design/design.spec.ts`); status never by colour
alone; every control keyboard-operable with a visible focus ring; icon-only
buttons always labelled (`IconButton.label`); dialogs trap focus and return
it; live regions announce job completion, copies and connection changes;
`prefers-reduced-motion` respected; a skip link to `#main`.

## Tests and screenshot review

- `*.spec.ts` (Node): logic — tokens and contrast, hues, table helpers,
  guards, nav filter, stores, JobWatcher, WebAuthn and QR helpers.
- `*.test.ts` (jsdom + `@testing-library/svelte`): components — roles,
  labels, keyboard, focus trap and return. Harness components live in
  `web/src/test/`. Use `userEvent.setup({ pointerEventsCheck: 0 })` with
  Bits UI overlays.
- These unit and component tests are the only automated UI tests. The
  Playwright suite (flows, axe-core accessibility scans of every main
  route, keyboard-only walkthroughs, live convergence across sessions) was
  removed on 2026-09-25: page-level accessibility, real-browser behaviour
  and live updates in open screens are not verified by automated tests
  any more; check them by hand (keyboard only, a screen reader, reduced
  motion, the browser's accessibility audit) when you change a page.
- Screenshot review: `npm --prefix web run build`, run the manager
  (`docs/development.md`, "Running locally"; Docker screens need a
  connected agent), then look at your page at 1440×900 and 390×844 and
  compare against the mockup on #22 and this document. Keep screenshots
  out of the repository.
