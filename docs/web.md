# Web UI

The DockYard UI is a SvelteKit single-page app (Svelte 5, TypeScript) built
with `@sveltejs/adapter-static` and embedded into `dockyard-manager`
(`web/embed.go`). It is an installable PWA. Library choices, licenses and
bundle sizes: [ADR 0002](adr/0002-frontend-libraries.md). Visual design,
tokens and the component library: [design/README.md](design/README.md) (#22;
live gallery at `/design`). Docker-free local stack for UI work:
[development.md](development.md#ui-devstack-no-docker).

```
web/src/
  app.html                    document template (manifest link, theme-color)
  service-worker.ts           service worker entry (SvelteKit-built)
  routes/+layout.svelte       global styles, QueryClientProvider (session-expiry hook),
                              SW registration, connection/update notices, toasts
  routes/+error.svelte        not-found and router errors
  routes/(app)/               signed-in area: auth guard + AppShell (+layout.svelte),
                              the dashboard (+page.svelte), one folder per section
  routes/(auth)/              public pages: setup, sign-in, enroll, invitation,
                              password-reset (centred layout)
  routes/design/              the design system gallery (public, sample data)
  lib/design/                 tokens.css, global.css, service hues, icon registry, demo data
  lib/ui/                     the component library ($lib/ui barrel)
  lib/features/<area>/        feature-local components and logic (resources: Docker
                              objects, refusals, job follow-up; builds; registries)
  lib/shell/                  app shell: sidebar, nav filter, environment switcher,
                              top bar, command palette, notices, page title/breadcrumbs
  lib/auth/                   route guard, session lifecycle, WebAuthn, QR, one-time codes
  lib/routes.ts               every in-app URL
  lib/api/schema.d.ts         GENERATED from api/openapi.json
  lib/api/client.ts           typed client, unwrap(), ApiRequestError, schema type aliases
  lib/api/queries.ts          query keys, queryOptions factories, QueryClient
  lib/api/jobs.svelte.ts      JobWatcher (job event stream with a polling fallback)
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
bash scripts/web-check.sh           # npm ci (if needed), lint, svelte-check, vitest, build, verify-build
npm --prefix web run dev            # dev server; proxies /api to 127.0.0.1:8080
npm --prefix web run build          # web/build/app (embedded by the next go build)
node web/scripts/verify-build.mjs --markdown   # bundle table for ADR 0002
bash scripts/generate.sh            # regenerate api/openapi.json + schema.d.ts
```

The service worker is not registered under `vite dev`; test PWA behaviour
against a built manager (below) or the E2E stack.

## Generated client workflow

1. Add or change the Go operation (`internal/manager/api`, see CLAUDE.md).
2. `bash scripts/generate.sh` regenerates `api/openapi.json` and
   `web/src/lib/api/schema.d.ts` (openapi-typescript). Commit both; the PR
   gate (`generate.sh --check`) fails when they are stale.
3. Call it through the typed client. Paths, parameters, bodies and responses
   are checked by TypeScript against the schema:

   ```ts
   import { api, unwrap } from '$lib/api/client';
   const stack = await unwrap(
   	api.GET('/api/v1/stacks/{stackId}', { params: { path: { stackId } }, signal })
   );
   ```

   `unwrap()` returns the data or throws an `ApiRequestError`:
   `status` (null for network failures), `apiError` (the DockYard error body;
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
`sessionStorage` or Cache Storage.

## Feature modules and step-up

Screens of one area keep their query factories, pure presentation helpers
(`model.ts`, unit-tested in `model.spec.ts`) and area components in
`src/lib/features/<area>/`; generic pieces stay in `$lib/ui`. Shared page
pieces are in `src/lib/features/common` (`Page`, `QueryView` for the
loading/denied/not-found/error states, `Facts`, `NameCell`, `Fields`,
`FormFooter`, `ScheduleSummary`, `useUnsaved`/`useCriticalWork`). Query keys
still follow `liveKeys` (a feature marker after `'list'` keeps cached
shapes apart).

The permission editor of #17 (the design's "PermissionTree") is
`$lib/features/access/PermissionEditor.svelte`: a searchable resource tree
(`ResourceTree`, categories in `tree.ts`) beside the actions of the chosen
scope (`ActionMatrix`), in three modes: `group` (No rule / Allow / Deny),
`user` (Inherit / Allow / Deny with the inherited decision explained) and
`token` (grants limited to what the caller holds, #31). Rule logic
(scope keys, diffs, inheritance precedence) is in `permissions.ts`;
`RulesSaveBar` lists every change before the revisioned, step-up save.

Changes the manager guards with recent authentication answer
`403 step_up_required`; wrap the call in `withStepUp(() => …)` from
`$lib/auth/stepup.svelte`: the signed-in layout's `StepUpDialog` asks for
the password (plus TOTP) or a passkey once and the call is retried;
dismissing it throws `StepUpCancelledError`.

## PWA

- **Manifest**: `src/lib/pwa/manifest.ts` (written to
  `/manifest.webmanifest` by `@vite-pwa/sveltekit`), linked from
  `app.html`. `id`, `start_url` and `scope` are `/`, `display` is
  `standalone`. Colours are the design tokens (#22): `THEME_COLOR` is
  `--surface-shell`, `BACKGROUND_COLOR` `--surface-canvas`; keep
  `app.html`'s `theme-color` equal to `THEME_COLOR` (unit-tested).
- **Icons** (#22): the app icon is the shell's cube mark
  (`src/lib/shell/Logo.svelte`) on the `--surface-shell` tile with a faint
  accent glow. Sources: `web/static/icons/icon.svg` (rounded tile,
  transparent corners: the `any` icons and `favicon.ico`) and
  `web/scripts/icon-maskable.svg` (full bleed, the cube inside the 80 %
  safe circle: the maskable and Apple touch icons). Keep the cube's colours
  equal to the logo's. Regenerate the PNGs after changing either:

  ```bash
  cd web
  npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets.config.mjs
  npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets-maskable.config.mjs
  mv static/icons/favicon.ico static/favicon.ico
  mv scripts/maskable-icon-512x512.png scripts/apple-touch-icon-180x180.png static/icons/
  ```

- **Service worker** (`src/service-worker.ts`, rules in
  `src/lib/pwa/sw-core.ts`, unit tests in `sw-core.spec.ts`): precaches only
  content-hashed build files, the manifest, icons and the SPA shell;
  `/api/*`, `/agent/*`, non-GET and cross-origin requests are never handled
  or stored; navigations are network-first with the precached shell as the
  offline fallback. Details and rationale: ADR 0002.
- **Updates**: a new build installs in the background and waits. The
  *App update* notice offers *Reload to update* (applies it and reloads once)
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
- **Offline**: the *Connection status* notice shows when the browser is
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
go build -o /tmp/dockyard-manager ./cmd/dockyard-manager
DOCKYARD_PUBLIC_URL=http://localhost:8080 DOCKYARD_LISTEN_ADDR=127.0.0.1:8080 \
  DOCKYARD_DATA_DIR=/tmp/dockyard-data /tmp/dockyard-manager
E2E_BASE_URL=http://localhost:8080 npm --prefix e2e test -- tests/pwa.spec.ts
```

The HTTPS-only assertions (and the stream helper tests, which need the echo
fixture) fail in this mode; CI runs everything behind Caddy
([testing/harness.md](testing/harness.md#browser-e2e)).

## Live data (#23)

The root layout starts one live client per tab (`startLive(queryClient)`,
`src/lib/live`): a single `EventSource` on `/api/v1/live/stream`
([streams.md](api/streams.md#live-invalidation-stream-23)) whose events
invalidate the affected Svelte Query keys. Views do not subscribe to
anything themselves; they only have to

1. **key their queries by the conventions** in `src/lib/live/keys.ts`
   (build them with `liveKeys`):

   | data | key |
   | --- | --- |
   | a list of a topic | `liveKeys.list('stacks', filters)` → `['stacks', 'list', filters]` (refreshed at most every second) |
   | an instance-wide resource | `liveKeys.item('stacks', stackId, 'revisions')` → `['stacks', 'item', id, …]` (stacks, jobs, environments, agents, policies, backups, registries, settings, permissions) |
   | a Docker object | `liveKeys.item('containers', envId, name, 'logs')` (containers, images, volumes, networks are named per environment) |
   | charts | `liveKeys.metrics(envId, …)` (at most every 10 s; the same `metrics` event also refreshes `['overview']`, the dashboard's latest usage) |
   | a stack's containers | `liveKeys.stackServices(stackId)` (refreshed on container events) |
   | scoped files | `liveKeys.files({kind: 'stack', id: stackId}, 'list' \| 'stat' \| 'content', path)`; volumes use `id: '<envId>/<volume>'` |
   | the caller's permissions | `liveKeys.myPermissions` |

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
stream is back.

**`liveStatus`** (`src/lib/live/status.svelte.ts`) is the interface for the
shell (#22): reactive `state` (`idle`, `connecting`, `live`,
`reconnecting`, `polling`, `unauthenticated`, `stopped`), `since`,
`lastEventAt`, `failures`, `environments` (`{ [envId]: 'online' |
'offline' }` from agent events) and `stale` (anything but `live`: data may
be behind; keep showing it, say so). Only the live client writes it.

Tests: `client.spec.ts` (cursor resume, gap and environment resets,
dedupe, revocation clearing, polling, throttling), `keys.spec.ts`
(invalidation map, critical work); end to end `e2e/tests/live.spec.ts`.

## Files, logs and terminals (#15, #8)

Feature code lives in `src/lib/features/{files,logs,terminal}`; the routes
only wire resources to it:

| screen | route | component |
| --- | --- | --- |
| stack files (Files tab) | `(app)/stacks/[stackId]/files` | `FileManager` (+ `LogDock`, the logs as a bottom drawer) |
| volume files | `(app)/volumes/[environmentId]/[volumeId]/files` | `FileManager` (same component, volume scope) |
| stack logs (Logs tab), container logs | `…/stacks/[stackId]/logs`, `(app)/containers/[environmentId]/[containerId]/logs` | `LogPanel` → `LogViewer` |
| logs in their own window | `(popout)/popout/logs?stack=` / `?environment=&container=` | `LogPanel` without the app shell |
| stack terminal (service picker), container terminal | `…/stacks/[stackId]/terminal[?container=]`, `(app)/containers/[environmentId]/[containerId]/terminal` | `TerminalPanel` |

- **Files:** `FilesApi` (`files/api.ts`) calls the typed client for either
  root; listings and contents are keyed `liveKeys.files(...)`; the
  `EditorSession` keeps buffers, ETags and conflicts (never replacing
  unsaved text); selection, keyboard and conflict grouping are pure modules
  with Node tests. Details: [api/files.md](api/files.md#ui-22-23).
- **Logs:** `LogFeed` follows each container's SSE stream with its cursor
  (Follow off/on resumes with `since`, repeats are skipped) and merges them
  by time. Over HTTP/1.1 the browser allows six connections per host for all
  tabs, so a viewer streams one container and polls the others
  (`GET …/logs?since=`, every 3 s); over HTTP/2 or HTTP/3 it streams up to
  twelve. Service colours come from `serviceIdentity` (the services table's
  tile colour).
- **Terminals:** `ExecTerminal` creates the exec session, opens the
  WebSocket with the ticket in the subprotocol, frames stdin/stdout,
  sends `resize` when `TerminalView` (`fit`) changes size, maps close codes
  to messages (4422: "This image has no /bin/sh — try another command.") and
  warns after 25 idle minutes.

## Lazy-loaded libraries

CodeMirror, ECharts and xterm.js are reachable only through
`src/lib/lazy/index.ts`:

```ts
import { mountYamlEditor } from '$lib/lazy';
const editor = await mountYamlEditor(element, text, { label: 'compose.yaml', onChange });
// editor.text(), editor.setText(), editor.focus(), editor.destroy()
```

Prefer the `$lib/ui` wrappers `CodeEditor`, `Sparkline` and `TerminalView`;
every mount applies DockYard's theme (`codemirror-theme.ts`,
`echarts-theme.ts`, `TERMINAL_THEME`).

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
- Playwright (`e2e/tests/pwa.spec.ts`, `ui.spec.ts`, extended workflow job
  `e2e`): manifest, service worker under the TLS proxy, deep-link reloads,
  API responses absent from Cache Storage, offline shell, lazy loading (on
  `/design`); first-run setup, sign-in and sign-out, shell navigation,
  environment switcher and the Restricted user's denied state. Locally
  against the devstack: [development.md](development.md#playwright-against-the-devstack).
