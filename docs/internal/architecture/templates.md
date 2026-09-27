# Stack templates (template registry)

Reusable, versioned Compose projects: a template holds a `compose.yaml`,
an optional `.env` and every file next to them (bind-mounted
configuration, scripts, ...). Users edit a template's draft with the same
file manager as stack and volume files, publish immutable versions, and
create stacks from them. Templates are private (this instance only) or
public (listed in the instance's public registry for other managers).

| package | role |
| --- | --- |
| `internal/domain/template.go` | domain types, limits, tag and label rules, errors |
| `internal/manager/store/templates.go` | `templates`, `template_icons`, `template_versions` persistence |
| `internal/manager/templates` | service: CRUD, visibility, icons (`DetectIcon`), drafts (`fsroot` instance, quota, sweep), publication (`writeArchive`, `checkDefinition`), archives |
| `internal/manager/api/templates.go` | `/api/v1/templates...` routes |
| `internal/manager/api/files.go`, `internal/manager/files` | the draft file routes (`/api/v1/templates/{templateId}/files...`), served locally |
| `internal/fsroot` | the file operations shared with the agent |

## Model

Migration `20260927223725_create_templates`:

- `templates`: name (unique, case-insensitive), description, tags (JSON,
  lowercase `[a-z0-9-]`, at most 16), visibility (`private`/`public`),
  `version_seq` (the last version number handed out), creator, revision.
- `template_icons`: at most one per template; media type, sha256, size and
  the bytes (at most 256 KiB).
- `template_versions`: number (never reused), label (unique per template),
  notes, the sealed archive, its sha256 and size, content size, entry
  count and the definition files (`compose*.y(a)ml`, `.env` at the root)
  with their sizes. A trigger refuses updates; versions can be deleted.

## Drafts

`<data dir>/templates/<id>/draft` (mode 0700). The API resolves a template
scope (`protocol.ScopeTemplate`) to the template service instead of an
agent: `files.Service` dispatches `List`, `Stat`, `Read`, `Write`, `Mkdir`,
`Preview`, `Download` (an `io.Pipe`), `Upload` and `StartJob` to the local
`fsroot.Service`, so paths, ETags, conflicts, previews, archives and
extraction guards behave exactly as for stacks and volumes. File
invalidations are published as `files.invalidated` events with
`scopeKind=template` and no environment; the live stream narrows them with
`templateId`.

Limits: `DOCKER_MANAGER_TEMPLATE_MAX_SIZE_MB` (default 32) bytes and 5000
entries per template. Writes and uploads check the cached draft size
first (`CheckQuota`); copy and extract jobs start only below the limit and
extraction is bounded by the same byte limit; publication enforces both
limits again.

Drafts are plain files (like a stack's directory on its host); `.env`
values in a draft are readable to whoever can read the manager's data
directory. Published versions are sealed.

## Publication

`Publish` takes the draft's exclusive lock (file operations hold it
shared), checks the definition (a default Compose file at the root, no
top-level `name:` in any Compose file, YAML top level only), writes the
canonical tar.gz (see the conventions) within the limits, seals it and
stores the version with the next `version_seq`. The same draft always
produces the same bytes, so its sha256 identifies the content.

## Icons and live updates

Icons are served by `GET /api/v1/templates/{id}/icon` to any signed-in
user; the DTO's `icon.url` carries `?v=<sha256>`, which makes the response
cacheable forever, so a changed icon is a new URL. Mutations of templates
publish `resource.changed` for the `template` type, relayed on the
`templates` live topic.

## Permissions

The `template` resource type (instance resources, scopable per template):
`template.read`, `template.use`, `template.create`, `template.manage`,
`template.publish`, `template.remove` and `template.files.*`. The minimal
view shows id, name, visibility and icon.
