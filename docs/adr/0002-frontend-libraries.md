# ADR 0002: Frontend libraries and PWA shell

- Status: accepted
- Date: 2026-09-24
- Issues: #11 (web foundation), #22 (design system, later), #23 (PWA and live sync); builds on ADR 0001 (items 7, 8, 12)

## Context

#11 fixes the libraries the web UI is built from and proves the minimal
PWA shell inside the Go manager. It deliberately selects **no** visual
design: theme, tokens, layout and components come from the mockup in #22.
Everything here is plumbing that #22 and the feature issues build on.

## Decisions

### Dependencies

All new versions are pinned exactly in `web/package.json` (lockfile
committed). Shipped code is in `dependencies`; build tools are
`devDependencies`. Every shipped package passes `scripts/license-check.sh`.

| Package | Version | License | Role | Loading |
| --- | --- | --- | --- | --- |
| `svelte`, `@sveltejs/kit` | 5.57.1 / 2.70.3 (lockfile; ADR 0001) | MIT | framework, SPA router | initial |
| `openapi-fetch` (+ `openapi-typescript`, dev) | 0.17.0 / 7.13.0 | MIT | generated typed `/api/v1` client (ADR 0001) | initial |
| `@tanstack/svelte-query` | 6.2.4 (query-core 5.103.2) | MIT | server state, caching, retries, invalidation. v6 is the Svelte 5 (runes) line: `createQuery(() => options)` | initial |
| `@lucide/svelte` | 1.48.0 | ISC | icons; import per icon (`@lucide/svelte/icons/<name>`) so only used icons ship | initial (per icon) |
| `bits-ui` | 2.19.3 (+ peer `@internationalized/date` 3.12.4, Apache-2.0; `@floating-ui/*`, `runed`, `svelte-toolbelt`, `tabbable`: MIT) | MIT | headless accessible primitives (context menu, dialogs, menus) behind DockYard's own styling (#22) | route-split: only routes that use it |
| `codemirror` + `@codemirror/lang-yaml` | 6.0.2 / 6.1.3 (`@codemirror/*`, `@lezer/*`, `crelt`, `style-mod`, `w3c-keyname`: MIT) | MIT | stack/volume file editor (#7, #15) | lazy (`import()`) |
| `echarts` | 6.1.0 (+ `zrender` 6.1.0 BSD-3-Clause, `tslib` 0BSD) | Apache-2.0 | host/container time series (#5) | lazy (`import()`), tree-shaken |
| `@xterm/xterm` | 6.0.0 | MIT | container exec terminal (#19) | lazy (`import()`, incl. its CSS) |
| `@vite-pwa/sveltekit` (dev) | 1.1.0 (vite-plugin-pwa 1.3.0, workbox-build 7.4.1) | MIT | **build time only**: writes `manifest.webmanifest`, injects the precache list into the service worker | not shipped |

Not added:

- **`@tanstack/svelte-virtual`**: deferred. Add it (MIT) only when profiling
  a real directory or list view (#15) shows a need, and record the
  measurement here.
- **Workbox runtime, `workbox-window`, `virtual:pwa-register`**: not used.
  The service worker is hand-written (below) and registration is ~100 lines
  of our own code, so no Workbox code ships.
- **Themed component kits and CSS frameworks**: out of scope until #22.

### `@vite-pwa/sveltekit`: compatible, used in `injectManifest` mode

Evaluated against the pinned SvelteKit 2.70.3, Vite 8.3.1 (Rolldown) and
Svelte 5.57.1: the peer ranges match (`@sveltejs/kit ^2`, `vite ^8`) and the
build works. It is a build-time plugin only:

- `strategies: 'injectManifest'`: SvelteKit itself compiles
  `src/service-worker.ts` (so `$service-worker` and `$lib` imports work);
  the plugin then replaces `self.__WB_MANIFEST` with the precache list
  (URL + revision for unhashed files).
- `kit.spa: true` adds the SPA shell `index.html` to the precache;
  `globPatterns` restrict the rest to `_app/immutable/**` (content-hashed),
  `manifest.webmanifest`, `icons/*` and `favicon.ico`.
- `generateSW` was rejected: it would ship Workbox runtime code and turn the
  routing rules into configuration instead of unit-tested code.
- Cost: ~300 dev-only packages (workbox-build pulls Babel and Rollup);
  nothing reaches the bundle. If a future SvelteKit/Vite bump breaks the
  plugin, the fallback is a plain SvelteKit service worker: the worker
  already is one; only the precache list would come from `$service-worker`
  (`build`, `files`) instead of `self.__WB_MANIFEST`.

### Service-worker rules (`web/src/lib/pwa/sw-core.ts`)

1. Non-GET, cross-origin, `/api/*` and `/agent/*` requests are not handled
   (no `respondWith`): they go straight to the network and nothing is
   stored. API data, credentials, files, logs, terminals and job streams
   never touch Cache Storage.
2. Navigations are network-first. The precached `index.html` (the offline
   shell, marked `X-DockYard-Shell: offline`) is used only when the network
   fails or the proxy answers 502/503/504.
3. Precached paths are served from this build's cache
   (`dockyard-precache-<kit version>`).
4. Nothing is written to a cache outside `install`; `activate` deletes older
   `dockyard-precache-*` caches only.
5. A new build installs and **waits**. The page shows "A new version of
   DockYard is available" with *Reload to update* and *Later*; only the
   click sends `SKIP_WAITING` and reloads once the new worker controls the
   page. Nothing reloads automatically, so unsaved edits, terminals and
   restores are never interrupted (#23).

All immutable chunks are precached, including the lazy libraries. This
costs a one-time background download per build (see below) but keeps an
already-open tab working after an upgrade: its lazily imported chunks are
still served from its own precache although the manager now serves a newer
build. Unchanged content-hashed files are copied from the previous cache
instead of being downloaded again.

The Go server serves `/service-worker.js`, `/manifest.webmanifest` and
icons with `Cache-Control: no-cache` and exact content types, never sends
`Service-Worker-Allowed` (the root-level worker's default scope is already
`/`), and answers a missing build-only path with a plain 404 instead of the
HTML shell (`TestPWAAssets`, `TestPWABuildOnlyPathsNeverFallBackToHTML`).

### Bundle size (measured)

`node web/scripts/verify-build.mjs --markdown` after `npm run build`
(2026-09-24). Per-library rows split each chunk's size by its modules'
rendered length; "initial" is the static import closure of the SvelteKit
entry, the root layout and the `/` page.

| bundle | chunks | raw KiB | gzip KiB |
| --- | ---: | ---: | ---: |
| initial load of / (JS) | 11 | 135.1 | 49.9 |
| initial load of / (CSS) | 0 | 0.0 | 0.0 |
| codemirror (lazy) | 3 | 418.1 | 135.7 |
| echarts (lazy) | 1 | 481.8 | 161.2 |
| xterm (lazy) | 1 | 323.4 | 80.2 |
| bits-ui (route-split) | 1 | 95.4 | 29.6 |
| svelte-query (in initial load) | 3 | 33.1 | 10.9 |
| lucide (in initial load, 2 icons) | 3 | 4.4 | 2.0 |
| openapi-fetch (in initial load) | 1 | 6.4 | 2.0 |
| svelte + kit (in initial load) | 5 | 81.4 | 30.9 |
| all JS chunks | 19 | 1461.0 | 459.2 |
| service-worker precache | 29 files | 1473.4 | |

Implications:

- The first paint needs ~50 KiB gzip of JS; the editor, charts and terminal
  (~380 KiB gzip together) load only on the views that need them.
- ECharts must be imported through `src/lib/lazy/echarts.ts` with named
  imports: a dynamic `import('echarts/charts')` namespace defeats
  tree-shaking (measured 1072 KiB raw instead of 482 KiB).
- The manager serves assets uncompressed today (the Caddy examples do not
  enable `encode`), so the precache download is ~1.5 MiB raw per new build.
  Follow-up for #27: gzip/brotli for static assets.

### Lazy-loading plan

- Heavy libraries are only reachable through `import()` in
  `src/lib/lazy/index.ts` (`mountYamlEditor`, `mountLineChart`,
  `mountTerminal`). Views import these helpers, never the libraries.
  `verify-build.mjs` (part of `scripts/web-check.sh`) fails if a CodeMirror,
  ECharts or xterm.js module lands in a chunk that any entry imports
  statically.
- More CodeMirror languages (`.env`, JSON, Dockerfile, ...) are added as
  separate dynamic imports keyed by file type (#15).
- Route-level code splitting (SvelteKit nodes) covers mid-size libraries
  such as Bits UI. The #11 `/lazy-proof` page was replaced by the design
  gallery `/design` (#22), which keeps the same lazy-loading controls for
  `e2e/tests/pwa.spec.ts`.

### Generated client and Svelte Query

`openapi-typescript` generates `web/src/lib/api/schema.d.ts` from
`api/openapi.json`; `openapi-fetch` provides the typed calls; `unwrap()`
turns a result into data or an `ApiRequestError` (`status`, `apiError`,
`network`); per-resource `queryOptions()` factories in
`src/lib/api/queries.ts` feed `createQuery`. Retries: network errors, 5xx,
408 and 429 only, at most twice; mutations never retry automatically. The
workflow is in [docs/web.md](../web.md).

### Placeholders (replaced by #22)

#11 shipped neutral placeholders: manifest `theme_color` `#404040`,
`background_color` `#ffffff`, unstyled connection and update notices, and
the icon set generated from `web/static/icons/icon.svg`. #22 replaced the
colours and notices without changing the behaviour contract above (next
section); the icon set is still the placeholder artwork.

### #22 additions (2026-09-25): design system foundation

The mockup-derived design system ([docs/design/README.md](../design/README.md))
adds these dependencies (exact versions, lockfile committed):

| Package | Version | License | Role | Loading |
| --- | --- | --- | --- | --- |
| `@fontsource-variable/inter` | 5.3.0 | **OFL-1.1** | UI typeface, self-hosted (the PWA works offline; no font CDN) | CSS `@font-face`, woff2 per unicode range |
| `@fontsource-variable/jetbrains-mono` | 5.3.0 | **OFL-1.1** | code, logs, IDs, terminals | same |
| `uqr` | 0.1.3 | MIT (no dependencies) | QR code of the TOTP `otpauth://` URI, rendered in the browser as SVG (the secret never leaves the page) | route-split (enrollment page) |
| `@codemirror/language`, `@codemirror/view`, `@codemirror/state`, `@lezer/highlight` | 6.12.4, 6.43.13, 6.7.6, 1.2.4 | MIT | already shipped with `codemirror`; now direct dependencies because the DockYard editor theme imports them | lazy |
| `@testing-library/svelte`, `@testing-library/jest-dom`, `@testing-library/user-event`, `jsdom` (dev) | 5.4.2, 7.0.1, 14.6.7, 30.1.1 | MIT | component tests (`*.test.ts`) | not shipped |

**OFL-1.1 for font packages only.** The SIL Open Font License permits
bundling and redistributing the fonts with software; its conditions apply to
the font files themselves (keep the copyright notice, do not sell the fonts
alone, keep the reserved names for modified fonts), which DockYard ships
unmodified. `scripts/license-check.sh` checks these two packages to be
exactly OFL-1.1 and excludes only them from the general allowlist run;
OFL-1.1 is **not** added to the general npm allowlist, so any other package
under it still fails the gate.

Other decisions:

- **Bits UI moves into the initial load.** The signed-in shell uses its
  menus, popovers, dialogs and tooltips on every page, so route-splitting it
  no longer helps. The dashboard's static closure is now ~143 KiB gzip of
  JS (was ~50 KiB for the unstyled #11 shell); CodeMirror, ECharts and
  xterm.js stay lazy (`verify-build.mjs`). `verify-build.mjs` now counts the
  `(app)` group layout (the shell) and the dashboard page as the initial
  load of `/`.
- **No `@tanstack/svelte-virtual`.** `Table` windows long lists itself
  (fixed row height, `virtualWindow` in `src/lib/ui/table.ts`, unit-tested)
  past 500 rows; revisit when a view needs variable row heights (#15).
- **No component kit or CSS framework.** Styling is plain scoped CSS over
  the tokens in `src/lib/design/tokens.css`.
- **Placeholders replaced:** manifest `theme_color` is `--surface-shell`
  (`#0e141d`), `background_color` `--surface-canvas` (`#0b1016`); the
  connection and update notices use the design system. The PWA icon set is
  still the #11 placeholder artwork.

Measured after #22 (`node web/scripts/verify-build.mjs --markdown`):

| bundle | chunks | raw KiB | gzip KiB |
| --- | ---: | ---: | ---: |
| initial load of / (JS) | 43 | 439.0 | 143.1 |
| initial load of / (CSS) | 8 | 61.3 | 16.0 |
| codemirror (lazy) | 4 | 418.6 | 136.6 |
| echarts (lazy) | 1 | 482.1 | 161.3 |
| xterm (lazy) | 1 | 323.4 | 80.2 |
| bits-ui (in initial load) | 2 | 169.1 | 47.1 |
| svelte-query (in initial load) | 4 | 33.3 | 10.9 |
| lucide (in initial load) | 28 | 29.2 | 13.0 |
| openapi-fetch (in initial load) | 1 | 6.4 | 2.0 |
| svelte + kit (in initial load) | 5 | 84.2 | 31.8 |
| all JS chunks | 77 | 1750.5 | 556.0 |
| service-worker precache | 116 files | 2128.2 | |

The precache grows by the font files (~300 KiB for every unicode-range
subset; browsers download only the subsets a page uses).

## Consequences

- #22 starts from a working shell: typed data access, icons, headless
  primitives, lazily loaded heavy components, an installable PWA, and
  offline and update notices to restyle.
- New heavy dependencies go behind `src/lib/lazy` and show up in the
  verify-build table; update the table here when it changes materially.
- #23 builds the live event stream on `queryKeys` invalidation and keeps the
  service-worker rules above (API network-only).
