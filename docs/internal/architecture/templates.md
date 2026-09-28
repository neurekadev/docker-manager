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
  lowercase `[a-z0-9-]`, at most 16), links (JSON list of label and URL,
  migration `20260928180542_stack_template_links`), visibility
  (`private`/`public`), `version_seq` (the last version number handed
  out), creator, revision.
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

Manager-state backups carry every draft as `templates.tar.gz` next to the
database snapshot (see [backups](backups.md#manager-state)); restoring
such a set puts them back before the manager starts serving.

## Publication

`Publish` takes the draft's exclusive lock (file operations hold it
shared), checks the definition (a default Compose file at the root, no
top-level `name:` in any Compose file, YAML top level only), writes the
canonical tar.gz (see the conventions) within the limits, seals it and
stores the version with the next `version_seq`. The same draft always
produces the same bytes, so its sha256 identifies the content.

## Stacks from templates

`POST /api/v1/stacks/template-creations` (synchronous, like creating a stack
from files): the version's archive (digest checked) is gunzipped and
streamed through the migration transfer (`migration.receive` into
`<stacks>/.docker-manager-migrations/<id>/project`, `migration.commit`
into `<stacks>/<name>`, which must not exist), `compose.read` records
revision 1, and `migration.cleanup` removes the staging area (or, when a
later step fails, the committed directory). The stack remembers the
template version (`stacks.template_*` columns, migration
`20260928000500_stack_templates`) but never depends on it. The create
dialog reads the version's Compose files and `.env`
(`GET .../versions/{version}/definition`, `template.use`); an edited `.env`
is saved to the new stack with the stack file routes before an optional
deploy, so secrets never travel in the creation request.

## Public registry

Every instance serves its public templates: `GET /api/v1/template-registry`
(format `docker-manager.template-registry/v1`: instance ID and name, the
registry URL, and per public template with at least one version its tags,
links (left out when there are none), icon and newest 50 versions with
archive digests and sizes), plus the
icons and the version archives under `/api/v1/template-registry/templates/`.
No sign-in; per-address token buckets (60/s burst for the index and icons,
20 then one per 3 s for archives); `If-None-Match` answers 304. The
instance ID identifies a registry across URL changes. The web app's public
page `/registry` renders the same index for people.

## Registries of other instances

The owner adds another manager by its address (`POST
/api/v1/template-registries`; `internal/manager/templates/registries.go`).
The index is read at once and cached (`template_registries`,
`template_registry_entries`, `template_registry_icons`, migration
`20260928013000_create_template_registries`); a background loop syncs every
`DOCKER_MANAGER_TEMPLATE_REGISTRY_SYNC_INTERVAL` (default 30m, conditional
with the index ETag; failures back off, doubling up to 6 h). The registry
is keyed by the remote instance ID: the same instance under a new address
replaces the URL, this instance's own ID is refused, and a registry that is
removed and added again finds the stacks created from its templates (their
`stack.template.instanceId`), so their icons come back.

The index is validated entry by entry, never trusted: a template's links
(`RegistryLinks`, whose decoding leaves out a list or entry of the wrong
shape) pass `domain.SanitizeLinks` (the same rules as local links);
invalid or repeated ones are dropped, never the template or the registry,
and at most 10 are kept (`template_registry_entries.links`).

`GET /api/v1/template-catalog` lists this instance's templates and every
registry's cached templates; `/templates/remote/{instanceId}/{templateId}`
shows one read-only. Creating a stack from a registry template downloads
the version (`templateSource` in `internal/manager/app`) and checks its
size and digest against the cached index before the transfer described
above. Registry icons are served from the cache
(`/template-registries/{instanceId}/templates/{templateId}/icon`) and are
part of `GET /template-icons`.

## Web UI

`$lib/features/templates` and `web/src/routes/(app)/templates`. The UI calls
registries of other instances **template sources** ("Template sources" at
`/templates/registries`, "Add template source"); the API keeps the registry
names. `/templates` is one `ListCard` of `TemplateCard`s (a grid that fills
the row) with one set of filters: search, Source, Tag and Status ("Ready
to use" = has a version, "Draft only"; never confused with the Public/Private
visibility); a source whose last sync failed is named in a notice above it.
In the create-from-template dialog a card is a button that chooses the
template (`TemplateCard` `onselect`); elsewhere it links to its page.

Template pages use `PageHeader`: "Create stack" is the primary action once
a version is published (with `template.use`), "Publish version" otherwise.
Versions read "Version 1.2.0" as a label and "1.2.0" as a value. The
overview shows the latest version beside the details (`Columns`), the
archive digest only under "Details", and "What it runs": the services,
images and ports of the version's Compose files and the names of its `.env`
settings (`DefinitionSummary`, pure helpers `composeServices`/`envKeys` in
`model.ts`; YAML parsed with `parseYaml` from `$lib/lazy`). It reads the
version definition, so it needs `template.use`; values are never rendered.
Without `template.use` it names the Compose files only; with the minimal
view the overview says access is limited. Contents count like the Files
tab: "2 items (1 hidden)" (`contentsSummary`). "Duplicate as a new
template" sits in the versions card's header.

## Copies: duplicate, restore, save a stack

`POST /api/v1/templates/duplicates` copies a published version (this
instance's, or a registry's downloaded now) into a new private template;
`POST /api/v1/templates/{id}/draft-restores` puts a version's files back
into the draft; `POST /api/v1/templates/stack-imports` reads the chosen
entries of a stack's project directory from its agent (`files.download`,
tar.gz; escaping symlinks, hard links and special files are left out) into
a new template or an existing draft. All three fill a fresh directory and
swap it in, so the draft never ends half-written.

## Links

A template's links (documentation, website, repository; `domain.Link`)
are edited with its details (`POST /templates`, `PATCH
/templates/{templateId}` with `links`; `template.manage`) and checked by
`domain.NormalizeLinks` (at most 10, absolute `http`/`https` URLs of at
most 2048 characters without user information, each URL once, labels of at
most 60 characters; `422` names the field, `body.links[0].url`). The full
view, the catalog (own and registry templates) and the public registry
carry them. A stack created from a template starts with a copy
(`stacks.TemplateArchive.Links`), its own from then on. URLs may carry
query strings: never logged or audited (audit details carry `linkCount`
and the number of links in the diff). The web shows them under the page
header (`LinkList`) and edits them in Settings → Details (`LinksEditor`).

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
