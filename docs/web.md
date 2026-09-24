# Web UI

The DockYard UI is a SvelteKit single-page app (Svelte 5, TypeScript) built
with `@sveltejs/adapter-static` and embedded into `dockyard-manager`
(`web/embed.go`). It is an installable PWA. Library choices, licenses and
bundle sizes: [ADR 0002](adr/0002-frontend-libraries.md). Visual design and
components: #22 (not selected yet; the shell is intentionally unstyled).

```
web/src/
  app.html                    document template (manifest link, theme-color)
  service-worker.ts           service worker entry (SvelteKit-built)
  routes/+layout.svelte       QueryClientProvider, SW registration, notices
  routes/+page.svelte         minimal shell: GET /api/v1/health via Svelte Query
  routes/lazy-proof/          TEMPORARY lazy-loading proof page (remove in #22)
  lib/api/schema.d.ts         GENERATED from api/openapi.json
  lib/api/client.ts           typed client, unwrap(), ApiRequestError
  lib/api/queries.ts          query keys, queryOptions factories, QueryClient
  lib/lazy/                   the only entry points to CodeMirror/ECharts/xterm.js
  lib/pwa/                    SW rules, registration/update flow, connectivity,
                              manifest, notices
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
5. Writes use `createMutation` and invalidate by key prefix
   (`queryClient.invalidateQueries({ queryKey: queryKeys.stacks })`).
   Mutations are never retried automatically; dangerous retries use the
   API's idempotency keys (#4). Live invalidation from the manager event
   stream is #23.

Rules: the browser talks only to same-origin `/api/v1` (never an agent or
Docker socket); tokens and secrets never go to `localStorage`,
`sessionStorage` or Cache Storage.

## PWA

- **Manifest**: `src/lib/pwa/manifest.ts` (written to
  `/manifest.webmanifest` by `@vite-pwa/sveltekit`), linked from
  `app.html`. `id`, `start_url` and `scope` are `/`, `display` is
  `standalone`. Colours are **provisional placeholders** until #22; keep
  `app.html`'s `theme-color` equal to `PLACEHOLDER_THEME_COLOR`
  (unit-tested).
- **Icons**: placeholder artwork authored as `web/static/icons/icon.svg`
  (not derived from the mockup). Regenerate the PNGs after changing it:

  ```bash
  cd web
  npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets.config.mjs
  mv static/icons/favicon.ico static/favicon.ico
  ```

- **Service worker** (`src/service-worker.ts`, rules in
  `src/lib/pwa/sw-core.ts`, unit tests in `sw-core.spec.ts`): precaches only
  content-hashed build files, the manifest, icons and the SPA shell;
  `/api/*`, `/agent/*`, non-GET and cross-origin requests are never handled
  or stored; navigations are network-first with the precached shell as the
  offline fallback. Details and rationale: ADR 0002.
- **Updates**: a new build installs in the background and waits. The
  *App update* notice offers *Reload to update* (applies it and reloads once)
  or *Later*; nothing reloads on its own. Views with critical unsaved state
  (#15 editor, #19 terminal, #10 restore) must keep working until the user
  chooses to reload.
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

## Lazy-loaded libraries

CodeMirror, ECharts and xterm.js are reachable only through
`src/lib/lazy/index.ts`:

```ts
import { mountYamlEditor } from '$lib/lazy';
const editor = await mountYamlEditor(element, text); // editor.text(), editor.destroy()
```

`verify-build.mjs` fails the web gate if one of them is statically imported
by any entry chunk. Bits UI and Lucide are imported directly (Lucide per
icon: `import X from '@lucide/svelte/icons/x'`).

## Tests

- `npm --prefix web test` (vitest, Node): client/Svelte Query integration,
  service-worker routing and caching rules, update flow, connectivity,
  manifest.
- `web/scripts/verify-build.mjs`: lazy chunks, precache list, manifest and
  icon sizes after the build.
- Go: `internal/manager/server` (`TestPWAAssets`, deep links, caching),
  `web` (`TestAssetsHaveIndex` checks PWA files in a real build).
- Playwright (`e2e/tests/pwa.spec.ts`, extended workflow job `e2e`):
  manifest, service worker under the TLS proxy, deep-link reloads, API
  responses absent from Cache Storage, offline shell, lazy loading.
