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
- Icons: `DetectIcon` decides the type from the bytes (PNG, JPEG, GIF, WebP,
  SVG), at most 256 KiB and 1024x1024 pixels; SVG without scripts,
  embedded documents or DOCTYPE/ENTITY. Serve icons only through
  `iconResponse` (nosniff, sandboxing CSP, immutable caching only for the
  matching `?v=<sha256>`), render them only with `<img>`. Any signed-in
  user may load an icon.
- Deleting a template removes its row, versions, icon and directory and
  calls `ForgetResource`; startup (`sweep`) removes directories without a
  template. Stacks created from a template never depend on it.
- Capabilities: `template.read`, `template.use` (high: version files incl.
  `.env`), `template.create` (instance), `template.manage`,
  `template.publish` (high), `template.remove` (high) and the
  `template.files.*` file capabilities, on the `template` resource type.
