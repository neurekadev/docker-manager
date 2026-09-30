# Docker Manager design system (#22)

The web UI's visual language and component library. The source is the #22
mockup (stack detail "Silo", dark, wide desktop; attached to the issue only,
never committed) and the design decisions in #25 Q8: **dark-only v1**, no
"PRO" badge, and the narrow-layout rules below. Everything here is code in
`web/src/lib/design` (tokens, hues) and `web/src/lib/ui` (components); the
live gallery is **`/design`** in any running Docker Manager (public, sample data
only).

- [Principles](#principles)
- [Tokens](#tokens)
- [Service colour](#service-colour)
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
  actions, green/red/amber only for status (and the lifecycle button's soft
  Start and Stop, which change what runs). Colours belong to types, never
  to single items (see [Service colour](#service-colour)).
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
| `--surface-canvas` | `#0a0f15` | page background, gaps between cards |
| `--surface-shell` | `#0d131b` | sidebar and top bar (and the PWA theme colour) |
| `--surface-panel` | `#121a24` | cards and panels (a clear step above the canvas) |
| `--surface-raised` | `#19222e` | table header, inputs, secondary buttons, menus |
| `--surface-search` | `#172029` | the top bar's search field |
| `--surface-hover` | `#1f2935` | hovered or selected row, skeleton base |
| `--surface-selected` | `#112745` | active nav item, current tab (equals `--accent-soft`) |
| `--surface-selected-strong` | `#162a51` | selected file row |
| `--border-subtle` | `#1f2a38` | card and panel edges |
| `--border-strong` | `#2b3747` | inputs, dividers, floating layers, meter tracks, skeleton peak |

### Text

| token | value | contrast on panel | use |
| --- | --- | --- | --- |
| `--text-strong` | `#f2f4f7` | 15.9:1 | titles, KPI values, primary cells |
| `--text-default` | `#c8d3e2` | 11.6:1 | body and table text |
| `--text-muted` | `#8392a8` | 5.5:1 (4.7:1 on `--surface-hover`) | labels, secondary lines, table headers, placeholders, timestamps |
| `--text-faint` | `#596476` | 2.9:1 | decorative only (separators, disabled) — never text that must be read |

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
| `--offline` / `--offline-soft` | `#8392a8` / `#1f2935` | offline environments (always with the word "Offline") |

### Category tiles

`--tile-<color>-bg` / `--tile-<color>-fg` for `blue`, `cyan`, `indigo`,
`green`, `violet`, `teal`, `rose` and `slate`: the colour of each resource
type's tile (`RESOURCE_ICONS`, [Row icons](#row-icons)), of KPI cards
(`cyan` CPU, `indigo` memory, `green` uptime, `violet` deploys) and the
service colour. `TILE_HEX` in `hue.ts` mirrors them for canvases.

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

Editor, terminal and chart colours are `--code-*` tokens (`--code-bg`
`#0f161f`, a step below the panel; `--code-active-line` `#16202b`),
mirrored in `web/src/lib/lazy/palette.ts` for CodeMirror, xterm.js and
ECharts.

**Motion:** the one attention motion is the live-change pulse: add
`data-changed` to a changed cell or row for one render (Table does it for
keys in its `changed` prop). `prefers-reduced-motion` turns it into a static
marker and zeroes all durations.

## Service colour

Every service has the **same tile and the same colour**
(`RESOURCE_ICONS.service`, `SERVICE_COLOR`, blue): in the services table,
the ⌘K hits, and also where the output of several services is interleaved
(the stack's merged logs, chart series, filter chips). There the service's
name tells them apart, never a colour. Services (and stacks) have no icon
or colour of their own.

The one exception is a chart of every container of an environment
(`MultiSeriesChart`, the environment page's Docker CPU, memory, network and
disk I/O): dozens of stacked lines can only be followed by colour, so each
container gets its own in Beszel's order: on each chart the containers are
ranked by their total over the range and get `rankColor(rank, n)` from
`$lib/design/hue` (hues spread evenly from red, the largest first; the
stacking stays in name order). The tooltip and name filter name them. The
host's Temperature chart colours its sensors the same way, ranked by their
maximum over the range (`rankByPeak`), the hottest first.

```ts
import { SERVICE_HEX } from '$lib/design/hue';

<IconTile {...resourceIcon('service')} size="sm" />          // services table
<span style="color: {SERVICE_HEX}">silo-db</span>            // log prefix
series.color = SERVICE_HEX;                                  // ECharts
```

- No hashed or per-item colours anywhere: a colour always stands for a
  type or a status.

## Type

Inter Variable for UI text, JetBrains Mono Variable for code, logs, digests,
IDs, paths and terminals; both self-hosted (`@fontsource-variable/*`, OFL-1.1,
ADR 0002) so the offline PWA shell renders correctly.

| role | size / line height | weight | token |
| --- | --- | --- | --- |
| page title | 28 / 34 | 600 | `--text-title` |
| page title below 768 px, boot screen | 22 / 28 | 600 | `--text-title-sm` |
| KPI value (compact cards: 18 / 24) | 20 / 28 | 600, tabular | `--text-kpi` (`--text-kpi-sm`) |
| section title (card titles, always) | 16 / 24 | 600 | `--text-section` |
| subsection: a heading inside a card | 14 / 20 | 600 | `--text-subsection` |
| control | 14 / 20 | 500 | `--text-control` |
| body and tables (the dense default) | 13 / 20 | 400 | `--text-body` |
| caption, meta | 12 / 16 | 400 | `--text-caption` |
| mono | 12.5 / 20 | 400 (`.mono`) | |

Fields keep these sizes on touch screens too. iOS zooms the page into a
focused field with text under 16 px; on iOS only, `$lib/shell/viewport`
adds `maximum-scale=1` to the viewport, which stops that zoom while iOS
still allows pinch zoom. Other browsers keep the plain viewport (they would
lose pinch zoom and never zoom on focus). Do not enlarge fields to fix it.

Numbers in tables, KPIs and logs use `.num` (`tabular-nums`). Sentence case
everywhere; no all-caps labels, no tracked-out eyebrows. A `Card`'s title is
always the section size; a heading inside a card body is an `h3` with the
global class `subsection-title` (`<h3 class="subsection-title">Environment
variables</h3>`). Components use the size tokens, never one-off pixel sizes.

## Layout and responsive rules

```
┌────────────┬──────────────────────────────────────────────────────┐
│ Docker Manager   │ ☰  homelab / Stacks / Silo   … [Search… ⌘K]  🔔 (U) │ 52 px
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

Pages use the full content width: side-by-side cards (`Columns`) keep
their own height (aligned to the top; a one-line card is never stretched
to its neighbour's table), lists are full-width tables, and a KPI row or
card grid fills a row rather than one card sitting half-width beside empty
space. The document is the page's scroll container and reserves the
scrollbar's space (`scrollbar-gutter: stable`), so pages do not shift
sideways between short and long ones. Create and edit forms of stacks and policies (backups, updates,
maintenance, the maintenance defaults) are dialogs over the list or detail
page, laid out in columns (`Dialog size="xl"`), opened by a query
parameter (`?create=1`, `?edit=1`, `?defaults=1`; `urlDialog` in
`$lib/features/common`) so links and routes (`routes.updatePolicyNew()`)
open them. Only long single-purpose flows (repository setup, restore,
token creation) stay pages (`Page narrow`, 1120 px) with a plain
`PageHeader` (no section tabs); when they run long, their buttons sit in
a sticky bar at the bottom of the window (`FormFooter sticky`, with an
optional `summary` status line).

Touch targets are at least 40 px on coarse pointers, and every right-click
action (ContextMenu) has a visible alternative (the row's overflow Menu).
The file manager (#15) shows list and editor as `Tabs` ("Files",
"Editor") below 1024 px, never both squeezed.

The sidebar groups its sections (`NAV_GROUPS` in `nav.ts`): the overview
has no label, then "Docker", "Automation" and "Administration" as small
sentence-case labels; the rail shows thin dividers instead. Items are
36 px tall (40 px in the phone drawer and on coarse pointers) so every
section fits a 900 px high window.

## Components

Import from `$lib/ui` (one barrel). Snippet props (`trigger`, `children`,
`actions`, `cell`, …) are Svelte 5 snippets. Everything below is shown in
`/design` and covered by `*.test.ts` next to it.

### Actions

| component | key props | notes |
| --- | --- | --- |
| `Button` | `variant: primary \| secondary \| ghost \| danger \| danger-soft \| ok-soft`, `size: sm \| md`, `icon`, `iconEnd`, `loading`, `href`, `block`, `ref` | Labels name the result ("Deploy", "Save changes"). `loading` keeps the label, sets `aria-busy`, disables. `href` renders a link. `danger-soft` (red on `--danger-soft`) and `ok-soft` (green on `--ok-soft`) are for Stop and Start only. |
| `IconButton` | **`label` (required)**, `icon`, `variant: ghost \| secondary \| danger-soft`, `size`, `pressed`, `badge`, `tooltip`, `tooltipSide`, `href`, `external` | `label` is the accessible name and the tooltip; spread menu/popover trigger props onto it. No tooltip while its popup is open. With `href` it renders a link with the button's look (row actions such as "Open silo-web"; `external` opens a new tab with `noopener`). |
| `SplitButton` | `label`, `icon`, `onclick`, `items: MenuEntry[]`, **`menuLabel`**, `variant: primary \| secondary \| ok-soft \| danger-soft`, `size: sm \| md`, `loading`, `disabled`, `menuDisabled` (defaults to `disabled`), `title` (the main part's tooltip, e.g. why it is off) | The stack header's Deploy; the file editor's Format (`sm`, `secondary`, "More format options"); the lifecycle button (`ok-soft` Start, `danger-soft` Stop: the chevron shares the tone). |
| `Menu` | `items: MenuEntry[]`, `trigger` snippet `(props)`, `label`, `align`, `side`, `open` | Bits UI DropdownMenu: keyboard, typeahead, focus return. |
| `ContextMenu` | `items`, `label`, `children` snippet `(props)` | Right-click / long-press; always duplicate its items in a visible Menu. |
| `CopyButton` | `value`, `what` ("request ID"), `text` | Announces "Copied …". |

**Start, Restart and Stop** of a stack or container are one split button,
`LifecycleButton` (`$lib/features/common`; `running`, `actions:
{ start?, restart?, stop? }` each `{ run, disabled?, reason? }`, `busy`,
`disabled`, `reason`). The main part is **Stop** (`danger-soft`, Square)
while anything runs, a partially running stack included, and **Start**
(`ok-soft`, Play) while nothing does; the menu ("More start and stop
options") lists Start, Restart (RotateCw) and Stop in that order, only the
held ones, those that do not apply in the state turned off (a reason
becomes the item's description and the main part's tooltip). With one held
action it is a plain `Button`. Soft tones keep a page's one primary (Deploy)
the only blue button. Row menus list the same actions in the same order.

`MenuEntry` = `{ label, icon?, onSelect?, href?, tone?: 'danger', disabled?, description?, shortcut? }`
\| `{ separator: true }` \| `{ heading }`. `description` is a muted line under the
label and the item's accessible description (the label stays its name); a
disabled item uses it to say why it is off (only its label and icon dim).

### Display

| component | key props | notes |
| --- | --- | --- |
| `Badge` | `tone: neutral \| accent \| ok \| warn \| danger \| info \| offline`, `dot`, `pulse` | |
| `Chip` | `label`, `selected` (toggle: `aria-pressed`), `onclick`, `href`, `count`, `size: sm \| md`, `icon`, `hue`, `title`, `disabled` | A pill (`--radius-full`) for tags and filters: a link with `href`, a (toggle) button with `onclick` or `selected`, else a static tag. `hue` adds a colour swatch (e.g. `SERVICE_HEX`), a ring while the toggle is off. 40 px tall on coarse pointers. |
| `StatusBadge` | `status` (API state), `kind: resource \| job`, `label` | Dot **and** text; vocabulary in `status.ts` (`statusInfo`). Job `partial` reads "Partly failed". |
| `Card` | `title`, `level`, `subtitle`, `actions`, `padding: none \| md`, `id`, `stretchActions` | Tables use `padding="none"`. The header always wraps: actions that do not fit go below the title. `stretchActions`: the actions take the free width of the header (`ListCard`'s search and filters). Body padding is 16 px below 768 px. The title stays 16 px at either `level`; headings inside use `.subsection-title`. |
| `KpiCard` | `label`, `value`, `unit`, `secondary`, `icon`, `color`, `tone`, `sparkline` / `bar` snippets, `changed`, `href`, `onclick` | Every figure links to its list: with `href` the label is a link whose hit area covers the card (`onclick` runs first, e.g. to preset the list's filters; links in a snippet `secondary` stay clickable). Row of KPI cards: `KpiRow` (`$lib/features/common`; `repeat(auto-fit, minmax(210px, 1fr))`, equal heights, two per row below 768 px with an odd last card spanning the row, so five cards never leave an orphan). Label, value and a text `secondary` stay on one line each (ellipsis, the full text as tooltip). `tone` dots share one style (colour plus its soft ring). The card is a size container: at 230 px or less it switches to the compact layout (36 px tile, 18 px value, 12 px label), so the stack overview's five cards (status, CPU, memory, uptime, last deploy) fit one row on 1440 px screens and phones show two per row. |
| `IconTile` | `icon`, `color: TileColor`, `size: xs \| sm \| md \| lg` | Decorative (the adjacent text names the thing). `xs` (24 px, 14 px glyph) is the row icon of lists ([Row icons](#row-icons)). |
| `Meter` | `value`, `max`, `label`, `valueText`, `warnAt`, `dangerAt` | `role="meter"`. The empty track is `--border-strong`, visible on cards. |
| `Uptime` | `since` (ISO start; absent: "—"), `prefix` | Live duration ticking once a second (`formatUptime`: "5m 03s", "3h 12m 08s", "4d 3h 12m"), tabular numerals, `<time>` with the absolute start as title. Other live values read the shared `clock.now` (one interval, only while a component reads it). |
| `PageHeader` | `title` (h1), `description`, `icon`, `color`, `meta: MetaItem[]`, `status` / `actions` / `below` / `titleAction` / `titleEditor` snippets, `truncate` | Icon-led meta items with thin dividers (not middle dots). `below` is a row under the meta row: a stack's or template's links (`LinkList`, `$lib/features/common`), passed only when there are any. `titleAction` sits right after the title (a stack's rename pencil, an `IconButton size="sm"`); `titleEditor` replaces the visible title while it is edited in place (the h1 stays, `sr-only`; the stack's inline rename). `truncate` keeps a long title (an image reference, a volume name) on one line with an ellipsis and the full title as tooltip, the status beside it. `MetaItem.title` is the full value on hover, `MetaItem.copy` adds a copy button (the stack's host path). The icon tile marks one object (a stack, container, environment, policy, job): section pages (Containers, Jobs, …) and create forms have none. |
| `Table` | `rows`, `columns: Column<T>[]`, `rowKey`, **`label`**, `sort` (bindable), `manualSort` + `onsort`, `selectable` + `selected` (bindable) + `rowLabel`, `changed`, `maxHeight`, `virtualizeAfter` (500), `rowHeight`, `layout`, `empty` | Sortable headers with `aria-sort`, sticky header inside `maxHeight`, stacked cards < 768 px (`Column.stack`: title, status, meta, actions, head, hidden), windowed rendering past 500 rows (`virtualWindow`, `aria-rowcount`/`aria-rowindex`). Without rows and without `empty` it shows one row "Nothing here yet." The scroll box is `position: relative` (hidden `.sr-only` texts in cells cannot widen the page) and clips the last row's hover to a card's rounded corners. |
| `Tabs` | `items: TabItem[]`, `value` (bindable), **`label`**, `panel` snippet `(id)` | In-page tabs (Bits UI). |
| `TabNav` | `items: TabLink[]`, `current` (path), **`label`**, `after` snippet | Route tabs (stack detail); the URL is the state. Below 768 px the tabs scroll sideways with the current one kept in view, an edge fades out where more tabs are cut off, and `after` gets its own line. |
| `DiffView` | `title` (file), `before`, `after`, `beforeLabel`, `afterLabel`, `context` | Unified line diff (`diff.ts`, Myers) with old/new line numbers, `+`/`−` markers and screen-reader "Added:"/"Removed:" (never colour alone); unchanged regions collapse behind "Show N unchanged lines". |
| `Breadcrumbs` | `items: Crumb[]` | The shell renders them from `usePage`. |
| `Skeleton`, `Spinner`, `Kbd` | | Loading regions set `aria-busy`. |

`Column<T>` = `{ id, header, cell?: Snippet<[T]>, sortValue?, align?, width?, maxWidth?, truncate?, title?, pin?, numeric?, mono?, hideHeader?, stack? }`.

- `maxWidth: '280px'` caps the content; longer text wraps, or with
  `truncate: true` stays on one line with an ellipsis. The full text is the
  cell's tooltip: `title: (row) => row.image`, or the plain value for
  columns without a `cell` snippet. Use it for names, images and paths that
  would otherwise widen the table.
- `pin: 'end'` keeps the column at the right edge while the table scrolls
  sideways, on the row's background (hover and selection included): use it
  for the last (actions) column of wide lists.
- `stack: 'head'` puts the column at the end of a stacked card's first
  line (the row's "⋯" menu), so it does not take a line of its own.

### Row icons

Every resource list (stacks, containers, images, volumes, networks,
builds and saved builds, registries and Git credentials, template
sources, backup policies, runs, repositories and snapshots, update and
maintenance policies, jobs, schedules, environments, users, groups,
invitations, API tokens, passkeys, notification channels) starts each row's name with the type's
icon, so a list is recognisable at a glance:

```svelte
<NameCell icon="volume" name={v.name} href={routes.volume(env, v.name)} />
<IconCell icon="container"><div class="name-cell">…</div></IconCell>
<IconCell icon={environmentIcon(e.online)}>…</IconCell>
```

- One map, `RESOURCE_ICONS` (`$lib/features/common/resourceIcons.ts`),
  gives each type one Lucide icon and one tile colour. The object's page
  header tile, its empty states, its ⌘K hits and the sidebar entry of a
  section named after it (Containers, Jobs, …; Registries shows the
  registry connection's key; Backups and Access keep their own section
  icons) read the same map, so a type looks the same everywhere.
  Registry connections and API tokens share the key icon and differ by
  colour (slate, violet); they never meet in one list.
- The row icon is an `IconTile size="xs"` (24 px, 14 px glyph) in a fixed
  24 px slot, centred on the name block, 12 px before the name. The
  colour is the type's as on its page header, never chosen per row; the
  only variations are the header's own (an offline environment is slate,
  Docker Manager's own containers violet).
- Stacks show `StackIcon size="xs"`: the image of the template the stack
  was created from, else the blue stack tile (stacks have no icon of
  their own); schedules show the icon of the policy they run
  (`scheduleResource`).
- Decorative (`aria-hidden`): the name stays the link, the stretched row
  link and the accessible label; marks and badges stay beside the name.
  Nested tables of a detail page (a policy's runs, revisions) have none;
  a stack's services table shows the service tile on every row.

### Forms

All fields render label, description and error through `Field` (the control
gets `id`, `aria-describedby`, `aria-invalid`). Required controls carry
`required`; optional ones say "Optional." in their description.

| component | notes |
| --- | --- |
| `TextField` | `mono` for identifiers and paths; `bind:value`. |
| `PasswordField` | Reveal toggle ("Show password"/"Hide password", `aria-pressed`); `autocomplete: current-password \| new-password`; `revealed` (bindable) starts it in plain text, e.g. a stored secret the user just asked to see ("Show address"). |
| `TextArea`, `Select` (Bits UI listbox in the input's look: chevron trigger, check on the chosen option, typeahead; `onchange(value)`; an option's optional `icon` shows before its label in the list and the trigger, e.g. the notification service picker), `Combobox` (Bits UI, filtered, `options: SelectOption[]`) | No native `<select>` anywhere. |
| `SuggestField` | Free text with a themed suggestion listbox (`suggestions: string[]`, combobox pattern: arrows, Enter, Escape, pointer); for values that may be new (a volume name). No `<datalist>`. |
| `Checkbox` | Native; `indeterminate`; `hideLabel` for row selection. |
| `Switch` | `role="switch"`; for settings that apply immediately. |
| `RadioGroup` | Native radios in a fieldset. |
| `TriState` | Inherit / Allow / Deny (#17 user overrides; `variant="rule"`: No rule / Allow / Deny) with the effective decision and its source explained. The chosen segment is filled and outlined in its colour (ok for Allow, danger for Deny, neutral otherwise); segments share one width so controls line up. `highRisk` marks Allow (the permission editor marks risk next to the action instead). |
| `CronField` | `label` (the fieldset's legend), `bind:cron`, `bind:timeZone`, `kind`. A "Repeats" select (Hourly at a minute, Daily at a time, Weekly on a day at a time, Custom) writes the cron expression; the raw field shows only for Custom, and an expression the presets cannot edit opens as Custom. IANA time zone; next runs and DST notes from `POST /api/v1/schedules/previews` (the one parser, #13), debounced; server validation shown inline. Helpers in `cron.ts`: `describeCron`, `parseCronPreset`, `buildCron`. |

Map server validation errors with `fieldError(err, 'body.name')`.

### Overlays

| component | notes |
| --- | --- |
| `Dialog` | `open` (bindable), `title`, `description`, `size`, `footer` snippet, `trigger` snippet, `dismissible`, `alert` (Bits UI AlertDialog: `role="alertdialog"`). Focus trapped, Escape closes, focus returns to the opener; full screen < 768 px. Sizes: `sm` 480 px (confirmations, one-field prompts), `md` 640 px (short forms), `lg` 880 px (forms in two columns, previews), `xl` 1160 px (policy and stack editors laid out in columns). |
| `ConfirmDialog` | `message`, `consequences[]`, **`confirmLabel`** (the action, never "OK"), `tone`, async `onconfirm` (progress on the button, failures shown inline, stays open), `size` (`sm`; `md`/`lg` when it shows a preview). |
| `DestructiveConfirm` | Type-to-confirm (`confirmText`, usually the resource name), `consequences[]`, `affected: AffectedResource[]`, optional `extra` snippet (e.g. the archive dialog's "migrate stacks first" offer, the stack rename's name field and preview), `canConfirm` (another condition besides the typed text, e.g. a preview without blockers) and `size`. The name to type is shown by `TypeToConfirm`. |
| `TypeToConfirm` | The typed confirmation (`text`, bindable `value`): the exact text as a single-line code block (long names scroll, never wrap) with a "Copy name" button, then the input (accessible name "Type <text> to confirm"). Every dialog that asks for a typed name uses it (through `DestructiveConfirm` or directly, e.g. restores). |
| `Drawer` | Side or bottom sheet (`side`, `size`, `hideTitle`); the narrow navigation, detail panes, the log drawer. |
| `Popover` | Non-modal (`label`, `trigger` snippet): notices, environment switcher. |
| `Tooltip` | `text`, `trigger` snippet `(props)`. Supplements names; never the only name. |
| `InfoTip` | `text`. An (i) beside a label or control that explains it: the text is its tooltip and its accessible name (focusable). Inside a `<summary>` use `Disclosure`'s `hint` instead (a `title`, never a control in a summary). |
| `TooltipLayer` | Mounted once in the root layout: every `title` attribute shows as the same themed tooltip (`.dy-tooltip`, `global.css`) after 400 ms of hover or on keyboard focus, above the element (below when there is no room), multi-line titles keep their lines. Use plain `title` for hints; never a native tooltip. |

### Feedback and states

| component | notes |
| --- | --- |
| `toast.success / error / info / warn(title, { body, action, timeout })` + `<Toaster />` (root layout) | Errors stay until dismissed; polite and assertive regions. |
| `Notice` | Inline or `bar` banner: `tone`, `title`, `live`, `actions` (e.g. the external-change conflict: Compare, Reload from disk, Save as…, Overwrite). |
| `EmptyState` | Invites action: title, description, `actions`. |
| `ErrorState` | From the API error shape. `title` (optional) says what failed, then the message says what happened; without a title the message leads. The `code` and the request ID (with copy) wait behind a small "Details" toggle. Retry when retryable or a network failure (wraps below the text on phones). `bare`: no border, background or margin, for use inside a `Card`; `compact`: smaller padding. |
| `DeniedState` | The Restricted user's state (brief copy). |
| `OfflineEnvironment` | "homelab is offline", since when, what it means ("Actions on homelab are unavailable until it reconnects."). The shell shows it for the selected environment, except on that environment's own page (which shows it itself). |
| `JobProgress` | `jobId` (follows `/api/v1/jobs/{id}/events/stream` via `JobWatcher`, polling fallback) or a `watcher`; `inline` or `panel`; per-item results, partial failure summary, recovery advice, completion announced; `onfinish`; `summary={false}` leaves out the title/state line and the error (the job page shows them once itself). |
| `SecretReveal` | One-time secrets: copy, download, fingerprint, "I stored it" gate; dropped from the page after Continue. |
| `StepWizard` | Numbered steps, `onnext` validation (throw or return false), focus to the step heading, `onfinish`; `disabledReason` is Next's tooltip while `canAdvance` is false (why it is off); `canGoBack={false}` hides Back once the wizard started a job. `oncancel` (+ `cancelLabel`) adds Cancel to every step; `stepsClickable` turns visited steps into buttons (back at once, forward after the current step's `onnext` passes); `minHeight` keeps the step body from jumping between steps (wizards in dialogs). |

### Lazy surfaces

`CodeEditor` (CodeMirror + `codemirror-theme.ts`), `Sparkline` (ECharts,
nulls as gaps), `TerminalView` (xterm.js + `TERMINAL_THEME`). They mount the
libraries through `$lib/lazy` only (checked by `verify-build.mjs`).
`mountLineChart(el, name, points, unit)` returns `{ update(series), resize() }`
for charts; series colours come from `TILE_HEX` or `SERVICE_HEX`.

`TimeSeriesChart` (`title`, `timestamps`, `lines: ChartLine[]`, `unit`:
`percent | bytes | bytes_per_second | load | count | celsius`, `from`/`to`, `yMax`,
`detail`, `headline`) draws metric responses as they come from the API: nulls stay
breaks, runs of missing samples are shaded **and** listed as text under
the chart ("No samples since 12:40": offline intervals, #5), several
lines get a text legend with their latest values, the figure is labelled
with the latest value for assistive technology. `headline={false}` leaves out the
value after the title when the legend already shows every line's value
(network received and sent). A line with `dashed` is drawn and keyed
dashed: a reference next to a solid line (backup storage before
compression next to what is stored), so the two differ by more than
colour. Pure helpers in
`$lib/ui/timeseries.ts` (`gapIntervals`, `latestValue`, `formatValue`);
the ECharts option is `timeSeriesOption` in `$lib/lazy` (unit-tested).
Charts and sparklines apply data that arrives while ECharts is still
loading.

`MultiSeriesChart` (`title`, `timestamps`, `items: SeriesItem[]` with
`name`, `color`, `values` and optional `parts`, `unit`, `shown`,
`from`/`to`, `detail`) draws many items of one type as stacked areas
without a legend, like Beszel: `items` in stacking order, the first on top
(the environment's charts pass them ranked by usage, largest first, so the
bands form an ordered gradient), monotone curves, 40 % fills, 1 px lines
and a dot per band at the pointer. The headline is the total of the shown
items' newest bucket; hovering lists every shown item with a value there,
largest first, in its colour, with its parts ("12 KB/s in, 3 KB/s out"),
under their total; lists longer than 20 rows wrap into columns (values
right-aligned per column) and the tooltip sits beside the pointer inside
the window (`besidePointer`). Items `shown` leaves out (a name filter) stay
in their place, greyed out, and are left out of the tooltip and the total.
On phones and touch screens (`(max-width: 640px), (pointer: coarse)`)
there is no floating tooltip, which could not fit the screen: a tap moves
the pointer (`onPointer`, `hideTooltip` of `timeSeriesOption`) and the same
list shows under the chart, scrollable, with a close button.
Values that do not add up (temperatures) pass `stacked={false}`: plain
1.75 px lines side by side without fills; the headline and the text
summary name the largest shown value of the newest bucket (`maxAt`)
instead of a total, and neither the tooltip nor the phone list has a
total (the largest still first).
The `/design` gallery shows it with twenty demo containers and, side by
side, five demo temperature sensors.
Pure helpers in `$lib/ui/multiseries.ts` (`tooltipRows`, `tooltipHtml`,
`totalAt`, `maxAt`); `timeSeriesOption` takes `stacked`, `muted` lines and a
`tooltip` callback for it.

### Formatting

Every measured value (sizes, rates, percentages, load, CPUs, ratios,
seconds shown as a decimal, in KPIs, tables, meters, chart axes and
tooltips, progress text, backups, prune results) is shown with **up to two
decimal places, trailing zeros dropped**, and only through these shared
formatters, never with `toFixed`, `Math.round` or a hand-made unit
(#147): `formatNumber` (2, 1.5, 1.25, 0.07; half rounds away from zero,
1.005 → 1.01), `formatBytes` (binary units: 512 B, 1.5 KB, 312.46 MB,
123.45 GB, 2 GB; whole bytes below 1 KB), `formatPercent` (0.07%, 12.34%,
100%), `formatValue(v, unit)` for metric units (`bytes_per_second`
"1.25 MB/s", `load` "0.5", `celsius` "48.5 °C"; `count` stays whole), `Meter` (its percentage),
`ratioText` in backups ("2.01x", "2x"), `formatTemperature` ("38 °C",
"41.5 °C"). Counts stay whole numbers.

Durations and times are unit pairs, not decimals: `formatDuration` (two
units, one style: "1 s", "3 min 20 s", "17 h 9 min", "3 d 4 h"),
`formatHours` (long spans of hours with years: "3 y 41 d", a disk's
power-on time),
`parseGoDuration` / `formatGoDuration` (a Go duration string such as
`17h9m0s` from the API, read as seconds or as "17 h 9 min"), `formatUptime`
(live uptimes), `secondsSince(iso, nowMs)`, `formatRelative(iso, now)`,
`formatDateTime(iso, zone)` (one absolute format everywhere, "Sep 27, 2026,
16:54", 24 h; "—" for absent or invalid values), `shortId`. A relative time
carries the absolute one as its tooltip: `title={formatDateTime(iso)}`.

`describeCron(expr, timeZone?)` (`$lib/ui`, from `cron.ts`) reads a cron
expression in words: "Every 15 minutes", "Hourly at :05", "Daily at 03:00",
"Weekdays at 07:30", "Weekly on Monday and Thursday at 04:00", "Monthly on
day 1 at 02:00"; the zone follows in words only when it is not the
viewer's ("Daily at 03:00 (UTC)"); other shapes stay the raw expression.
Show schedules in words with the expression as tooltip
(`ScheduleSummary` in `$lib/features/common` does).

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
   pages have the object's tile (`{...resourceIcon(kind)}`; stacks: the
   template's image when they were created from one), create pages repeat the button that opens them as
   their title ("Create update policy", "Build image"). Loading: `Skeleton` in an `aria-busy` region.
   Failure: `ErrorState` with `onretry={() => query.refetch()}`. Nothing
   yet: `EmptyState` with the action. Forbidden: hide the control (the
   server answers 403/404 anyway).
4. Lists filter by `environmentSelection.id` (null = all environments).
   A section list (containers, images, volumes, networks, stacks) is one
   `ListCard` (`$lib/features/resources`): the card title "All
   containers" with the count ("3 of 40 containers"), the search, then the
   filters built into its header, and "Clear filters" while any is set;
   no matches show `NoMatches` with the same action. Filters are
   `ListFilter` definitions in a pure module (`filters.ts`, stacks:
   `$lib/features/stacks/filters.ts`): one per attribute the list shows
   (status, stack, environment while all are shown, Docker Manager
   system, usage, driver, …), never counts, sizes or dates. Filters built
   from the rows (drivers, projects) hide while they offer one choice. The
   state is a `ListFilters` store kept per list and browser tab.
5. Build the UI, run the manager and look at it at 1440×900 and 390×844
   ([Tests and screenshot review](#tests-and-screenshot-review)).

## Data, permissions and live updates

- **Data:** the typed client and Svelte Query only (`docs/internal/web.md`). Every
  resource gets a key in `queryKeys` (first element = the resource name,
  e.g. `['stacks', 'detail', id]`) and a `queryOptions` factory in
  `src/lib/api/queries.ts`. Mutations invalidate by prefix and never retry.
- **Live updates (#23):** views never read the stream. Key queries with
  `liveKeys` (`src/lib/live/keys.ts`; `docs/internal/web.md`, "Live data") and the
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
  selected environment ID per user and the sidebar rail preference). The
  search and filters of each list are UI state kept in `sessionStorage`
  (`docker-manager:list-filters:<list>`, per list and browser tab), as are
  the paths of the recently visited pages for the palette's "Recent"
  (`docker-manager:recent-pages`; their titles stay in memory).

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
  (`docs/internal/development.md`, "Running locally"; Docker screens need a
  connected agent), then look at your page at 1440×900 and 390×844 and
  compare against the mockup on #22 and this document. Keep screenshots
  out of the repository.
