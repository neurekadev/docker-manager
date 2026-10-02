# Stack templates (template registry)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/templates.md`. Manager:
`internal/manager/templates` (`app.Manager` wires it); API:
`internal/manager/api/templates.go` plus the template scope of
`internal/manager/api/files.go`; domain: `internal/domain/template.go`;
store: `internal/manager/store/templates.go`.

- A template has one **draft** (a directory `<data dir>/templates/<id>/draft`)
  and immutable **versions**. Only the file routes change a draft; they go
  through the template service's `internal/fsroot` instance (never plain
  `os` calls outside `internal/manager/templates`). Every change that adds
  bytes checks `CheckQuota` first and holds `Writing(id)` while it runs, so
  a publication never captures a half-written file.
- Drafts are in manager-state backups (`backup.go`: `templates.tar.gz`
  written by `WriteDrafts`, put back by `RestoreDrafts` when a restore is
  applied); published versions are in the database.
- Template file jobs are the `template.files.*` kinds: manager executor,
  one exclusive `template` lock (instance-level, like repository locks), no
  path locks, capability `template.files.<verb>` from the `template` target.
  `jobspec.TemplateFilesKind` maps a `files.*` kind to its template kind.
- A version is a canonical tar.gz (`writeArchive`): `.` first, lexical
  order, PAX headers, owner 0:0, the publication time as every mtime,
  permission bits only; only directories, regular files with one hard link
  and symlinks that stay inside. It is sealed with the keyring (context
  `template_versions/<id>/archive`) because `.env` files may hold secrets.
  Never store, log, audit or return archive or draft contents outside the
  file routes; audit details carry numbers, labels and digests only.
- Publishing refuses a draft without a default Compose file at its root and
  any Compose file with a top-level `name:` (the manager only reads the
  YAML top level; Compose semantics stay in the agent). Version numbers
  come from `templates.version_seq` and are never reused.
- Making a template public, or publishing a version of a public one, needs
  the explicit acknowledgement (`acknowledgePublic`,
  `domain.ErrTemplatePublicAckRequired`): every file, `.env` included,
  becomes readable by anyone with the registry URL.
- Links: `domain.NormalizeLinks` for a template's own (a problem is a
  `FieldError` naming `links[i].url`/`.label`), `domain.SanitizeLinks` for
  a registry's (invalid links are dropped, never the template or the
  registry). Never log or audit a URL; audit the number of links. A stack
  created from a template starts with a copy of its links.
- Icons: `DetectIcon` decides the type from the bytes (PNG, JPEG, GIF, WebP,
  SVG), at most 256 KiB and 1024x1024 pixels; SVG without scripts,
  embedded documents or DOCTYPE/ENTITY. Serve icons only through
  `iconResponse` (nosniff, sandboxing CSP, immutable caching only for the
  matching `?v=<sha256>`), render them only with `<img>`. Any signed-in
  user may load an icon.
- Deleting a template removes its row, versions, icon and directory and
  calls `ForgetResource`; startup (`sweep`) removes directories without a
  template. Stacks created from a template never depend on it.
- Stacks from templates: `stacks.Service.CreateFromTemplate` gets the
  version from a `stacks.TemplateSource` (app's `templateSource` for this
  instance's templates), checks its digest, validates the default Compose
  files on the agent, streams the unzipped tar with `transfer.NewWriter`
  over `migration.receive`, commits, reads revision 1 back and cleans up
  staging (removing the committed directory again when a later step
  fails). The route needs `stack.create` in the environment and
  `template.use` on the template. `GET /template-icons` (any signed-in
  user) maps registry + template to the current icon URL; the web joins it
  to `stack.template` (`StackIcon.svelte`), so icon changes need no stack
  writes (stacks have no icon of their own).
- The public registry (`api/template_registry.go`): `public` routes that
  never call `CheckerFor`; they only ever read templates that are public
  and have a version (`registryAPI.public`), throttle per client address
  (`auth/throttle`, archives lower), and answer 404 when disabled. The
  index is deterministic (no request time in it) so its ETag
  (`If-None-Match` → 304) lets other managers revalidate cheaply. The web
  shows it at `/registry` (public page) and the registry URL is
  `DOCKER_MANAGER_PUBLIC_URL`.
- Registries of other instances (`registries.go`, `registryclient.go`):
  a registry is the other manager's origin, identified by its instance ID
  (the `template_registries` primary key and `stack.template.instanceId`),
  so adding it again under any URL restores its stacks' icons. The client
  only follows same-origin redirects, only fetches paths below
  `/api/v1/template-registry/templates/` of that origin, limits every body
  (index 4 MiB, icons 256 KiB, archives their declared size up to twice
  the template limit), validates the index (never trust another instance)
  and checks archive digests. HTTPS only (plain HTTP for loopback); private
  addresses are allowed because only the owner adds registries
  (`template_registry.manage`, owner-only). Cached entries and icons are
  replaced by each sync (`SyncDue` every `SyncInterval`, backoff up to 6 h)
  and deleted with the registry. `/template-catalog` merges this
  instance's templates (per-template views) with every registry's cached
  templates (`template.read` on the instance); creating a stack from a
  registry template needs `template.use` on the instance and downloads
  the version at that moment.
- Drafts filled from archives (`imports.go`): duplicating a version,
  restoring a draft to a version and saving a stack as a template extract
  a tar.gz into `draft.next` (cleaned member names, only directories,
  regular files and inside symlinks, permission bits only, the template
  limits counted while writing), then swap it in (`draft` →
  `draft.old` → removed; `sweep` restores an interrupted swap). A failed
  fill never changes the draft; a new template whose fill fails is
  deleted. Saving a stack reads its files with the agent's
  `files.download` stream (tar.gz of the chosen entries) and needs
  `stack.files.download` + `stack.definition.read`; replacing a draft needs
  `template.files.write` + `template.files.delete`.
- Capabilities: `template.read`, `template.use` (high: version files incl.
  `.env`), `template.create` (instance), `template.manage`,
  `template.publish` (high), `template.remove` (high) and the
  `template.files.*` file capabilities, on the `template` resource type.
- Web (`$lib/features/templates`, guide "Web UI"): registries of other
  instances are "template sources" in copy; versions read "Version 1.2.0"
  as a label and "1.2.0" as a value; the publication filter is "Status"
  ("Ready to Use", "Draft Only"), never a word near "Public". "What It
  Runs" parses the version definition in the browser (`template.use`) and
  shows service names, images, ports and `.env` names only, never a value;
  archive digests stay under "Details".
