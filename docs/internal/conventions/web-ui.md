# Web UI (#22)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guides: `docs/internal/design/README.md` (tokens, components, copy, a11y),
`docs/internal/web.md` (client, PWA); live gallery `/design`. Dark-only v1.

- **Components:** build pages only from `$lib/ui` (one barrel) and tokens
  (`$lib/design/tokens.css`); no raw hex, no one-off copies of a component.
  `IconButton` needs `label`; confirmations use `ConfirmDialog` /
  `DestructiveConfirm` (consequences listed, type-to-confirm for high
  impact); status is `StatusBadge` (dot + text). Service colours:
  `serviceIdentity`/`serviceSeriesColor` (`$lib/design/hue`). Heavy
  libraries only through `$lib/lazy` or `CodeEditor`/`Sparkline`/`TerminalView`.
- **Pages:** signed-in pages in `web/src/routes/(app)/<section>/` (replace
  the `SectionPlaceholder`), public ones in `(auth)`. URLs only from
  `$lib/routes.ts`. Call `usePage({ title, crumbs, environmentScoped })`;
  lists filter by `environmentSelection.id` (null = all). Feature view
  models and components live in `$lib/features/<area>/` (pure `*.ts` with
  `*.spec.ts`); only generic pieces go to `$lib/ui` (stacks:
  `$lib/features/stacks/` with `stackKeys`, `model.ts`, `actions.ts`).
  The environment route parameter is `[environmentId]`
  (`routes/(app)/environments/[environmentId]`);
  Docker object pages below it must use the same name.
- **Charts:** `TimeSeriesChart` for metric responses (nulls are breaks,
  gaps shaded and listed as text), `Sparkline` in KPI cards; metric queries
  keyed with `liveKeys.metrics(envId, …)`. 204 responses: `unwrapEmpty`.
- **Data:** typed client + Svelte Query; a `queryOptions` factory per
  resource in `src/lib/api/queries.ts` keyed with `liveKeys` ([live-sync.md](live-sync.md)) so live events refresh it; mutations invalidate by prefix, never
  retry; views never read the stream. Jobs: `JobProgress` / `JobWatcher`.
  Feature screens may keep their factories in `$lib/features/<area>/queries.ts`
  (docs/internal/web.md, "Feature modules"); step-up-guarded calls go through
  `withStepUp` (`$lib/auth/stepup.svelte`).
  Never store API data in `localStorage`/Cache Storage.
- **Permissions:** show actions from DTO `actions`/`view`; navigation from
  `/me/permissions` (`$lib/shell/nav.ts`); hide, don't disable; Restricted
  users get `DeniedState`. The server still decides.
- **Copy:** buttons name the result and the toast repeats it ("Deployed
  Silo"); errors say what happened and what to do, no apology; empty states
  invite action; sentence case, no all-caps labels.
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
- **Search:** `GET /api/v1/search` (`internal/manager/api/search.go`) feeds
  the ⌘K palette; new searchable resource types go there, filtered with the
  resource's own `ViewOf` and identity/status fields only.
- **Files, logs, terminals** (`docs/internal/web.md`): reuse
  `$lib/features/files/FileManager.svelte` (stack or volume scope),
  `$lib/features/logs/LogPanel.svelte` (stack or container; `LogDock` as a
  bottom drawer) and `$lib/features/terminal/TerminalPanel.svelte`; link to
  them with `routes.stack(id, 'files' | 'logs' | 'terminal')`,
  `routes.volumeFiles`, `routes.containerLogs`, `routes.containerTerminal`.
  Log viewers stream at most one container over HTTP/1.1 (six connections
  per host for all tabs) and poll the rest.
