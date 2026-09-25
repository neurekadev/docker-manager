# Scoped file manager (`…/files`, #15)

A file browser and editor for one **root**: a managed stack's project
directory or a Docker volume. The same routes exist under both roots:

| root | prefix | capabilities |
| --- | --- | --- |
| stack project directory | `/api/v1/stacks/{stackId}/files` | `stack.files.*` (+ `stack.definition.*` for Compose sources) |
| volume | `/api/v1/environments/{environmentId}/volumes/{volumeId}/files` | `volume.files.*` |

The manager authorizes (#17); the environment's agent confines every path to
the root (`internal/agent/files`); nothing is ever served from outside it.
Streams (downloads, uploads): [streams.md](streams.md#file-downloads-and-uploads-15).
Agent side: [agent-v1.md](../protocol/agent-v1.md#scoped-files-15).

## Routes

| method and suffix | operation | capability | answer |
| --- | --- | --- | --- |
| `GET /files?path=&sort=&q=&hidden=&cursor=&limit=` | list | `files.read` | `FileListing` |
| `GET /files/content?path=&offset=` | read (≤ 512 KiB) | `files.read` | `FileContent` + `ETag` |
| `PUT /files/content?path=` (`If-Match` / `If-None-Match: *`) | save (≤ 512 KiB) | `files.write` | `FileEntry` + `ETag` |
| `GET /files/downloads?path=…[&path=…][&format=]` | download | `files.download` | bytes (see streams.md) |
| `POST /files/entries` `{path, type: file\|dir, content?}` | create | `files.write` | `201 FileEntry` |
| `POST /files/uploads?path=&name=[&conflict=]` | upload one file | `files.write` | `201 FileUploadResult` |
| `POST /files/conflict-previews` `{operation, paths, destination, names, recursive}` | preview | `files.read` | `FilePreview` |
| `POST /files/copies` `{paths, destination, conflict}` | job `files.copy` | `files.copy` | `202 Job` |
| `POST /files/moves` `{paths, destination, conflict}` | job `files.move` (also rename) | `files.move` | `202 Job` |
| `POST /files/deletions` `{paths}` | job `files.delete` | `files.delete` | `202 Job` |
| `POST /files/archives` `{paths, destination, format, conflict}` | job `files.archive` | `files.archive` | `202 Job` |
| `POST /files/extractions` `{path, destination, conflict}` | job `files.extract` | `files.extract` | `202 Job` |
| `PATCH /files/metadata` `{paths, recursive, chmod?, chown?}` | job `files.metadata` | `files.chmod` and/or `files.chown` | `202 Job` |

`files.x` means `stack.files.x` or `volume.files.x`. Job routes take an
`Idempotency-Key`; follow the job with `GET /api/v1/jobs/{jobId}` (its
`items` report per-path results, at most 200 plus a summary).

## Paths

- Root-relative, slash-separated, percent-encoded UTF-8 in URLs; no leading
  `/`, no empty, `.` or `..` segments, no backslash, NUL or control
  characters, at most 4 096 bytes; empty or `.` is the root. A violation is
  `422 validation_failed` with the field (`query.path`, `body.paths[2]`, …).
- New names (entries, uploads) are one path component of at most 255 bytes.
- Responses carry root-relative paths only, never host paths.
- Symlinks inside the root are followed; symlinks whose target is absolute or
  leaves the root are listed (`linkTarget`, `linkStatus: outside`) but never
  followed: reading or listing through them is `422` on the path.

## Entries and revisions

`FileEntry`: `name`, `path`, `type` (`file`, `dir`, `symlink`, `other`),
`size`, `mode` (octal string, host semantics, e.g. `"0644"`), numeric `uid`
and `gid`, `modifiedAt`, `links` (hard link count), `linkTarget` and
`linkStatus` (`inside`, `outside`, `dangling`, `loop`) for symlinks, and
`etag` for regular files up to 256 MiB.

The **ETag** is the file's content revision: SHA-256 over the content, the
size and the modification time. Any change — through DockYard, a container
or a host tool — changes it. Saves and uploads that replace a file require
`If-Match` with the ETag the client loaded; a stale ETag is **`412
precondition_failed` with the current `ETag` header**: the editor keeps the
unsaved buffer and offers compare / reload / save as / overwrite (an
explicit overwrite re-sends with the new ETag). `If-None-Match: *` creates a
file only if the name is free. Without a precondition: `428`.

`FileContent` returns UTF-8 text as `content`, anything else (NUL bytes,
invalid UTF-8) as `contentBase64` with `binary: true`. At most 512 KiB are
returned per request (`truncated: true` and `offset` for more); files that
do not fit are downloaded and uploaded instead of edited.

## Listings

`FileListing {dir, items, nextCursor, total, truncated}`: `sort` is `name`
(default), `size`, `modified` or `type` (directories first), `-` for
descending, ties by name; `q` filters names (case-insensitive substring);
`hidden=true` includes dot files. Pages hold at most 200 entries (`limit`);
cursors are bound to the query. Directories with more than 100 000 entries
list the first ones read (`truncated: true`).

## Conflicts and previews

`POST …/files/conflict-previews` answers before an operation runs:

```json
{ "conflicts": [ { "source": "src/a.txt", "destination": "dst/a.txt",
                   "existing": { "name": "a.txt", "type": "file", "size": 3, … } } ],
  "conflictsTruncated": false,
  "impact": { "entries": 12, "files": 9, "dirs": 3, "symlinks": 0, "other": 0, "bytes": 5120, "truncated": false } }
```

- `copy` / `move`: destinations that exist (per top-level source) and the
  recursive impact of the sources.
- `upload`: `names` that exist in `destination`.
- `extract`: archive members that exist below `destination`; impact counts
  the archive's declared entries and sizes.
- `archive`: whether the archive file exists; impact of the sources.
- `delete`, `metadata`: the (recursive) impact.

At most 1000 conflicts are listed and 100 000 entries counted.

**Conflict policy** of copies, moves, extractions, archives and uploads:
`fail` (default: the item fails and is reported), `overwrite`, `skip`,
`keep_both` (new name `name (1).ext`). The policy applies to every item of
one request; the UI asks per item (apply-to-all is off by default) and sends
one request per decision group. Destructive and recursive actions are
previewed and confirmed in the UI before the request.

## Errors

| status | code | when |
| --- | --- | --- |
| 404 | `not_found` | the root is unknown or hidden from the caller, or the path does not exist |
| 403 | `forbidden` | the root is visible, the capability is not granted (the message names it) |
| 409 | `file_exists` | the name exists (create, entry) |
| 409 | `file_conflict` | the entry changed during the operation, or a directory into itself |
| 409 | `file_type_mismatch` | a directory where a file is needed (or the reverse) |
| 409 | `file_unsupported` | symlink, device, FIFO, socket, or a file with several hard links |
| 409 | `volume_files_unsupported` | non-local volume driver, remote-backed local volume, DockYard's own volumes, the stacks volume, or storage layout not verified |
| 411 | `length_required` | upload without `Content-Length` |
| 412 | `precondition_failed` | stale `If-Match` (with the current `ETag`) or `If-None-Match: *` on an existing name |
| 413 | `payload_too_large` | content over 512 KiB, upload over the limit, download or archive over 10 GiB |
| 416 | `range_not_satisfiable` | range outside the file |
| 422 | `validation_failed` | invalid or escaping path, name, mode, conflict policy |
| 422 | `content_digest_mismatch` | upload digest mismatch |
| 428 | `precondition_required` | save or upload without a precondition |
| 501 | `not_implemented` | the environment's agent does not serve files (upgrade it) |
| 503 | `unavailable` | the agent is offline or the Docker Engine unreachable |
| 504 | `timeout` | the agent did not answer in time |

Job failures (per item or for the whole job) carry the agent's code in the
item message or the job error (`too_large`, `forbidden_path`,
`unsupported_file`, …).

## Stacks: Compose sources

`compose.yaml` / `compose.yml`, `docker-compose.y(a)ml`, override files
(`compose.<name>.yaml`) and `.env` **at the project root** are the stack's
Compose sources. They may hold secrets (#25: no stack secret store) and the
on-disk files are authoritative (#7):

- reading them (content, download, copy, archive, or a download/archive of
  the whole root) additionally needs `stack.definition.read`;
- changing them (save, create, upload over them, delete, move, move or copy
  a Compose name into the root, extract into the root, chmod/chown) needs
  `stack.definition.write`;
- every change DockYard makes to them is reported to the stack service
  (`files.SourceObserver`, #7), which records a revision and marks
  undeployed changes; nothing is deployed automatically. Job-based changes
  are reported when the job finished.

Until the stack service (#7) installs its stack roots
(`app.Manager.Files().SetStacks`), stack routes answer 404.

## Volumes

Only local-driver volumes stored under the agent's verified volume
directory (#28) are served. Non-local drivers (plugins) and local volumes
backed by NFS/CIFS options are refused (`volume_files_unsupported`, #25),
as are the stacks volume (browse stacks per stack instead, so the Compose
source rules apply) and every volume mounted by DockYard's own containers
(label `dev.neureka.dockyard.role`: the manager's data and the agent's
state), so a file grant can never reach DockYard's database or credentials.

## Containment and safety (agent)

- Every access goes through an `os.Root` opened on the root directory:
  each path component is resolved relative to an open directory handle
  (`openat` with `O_NOFOLLOW` per component on Linux), symlinks are
  followed only while they stay inside, absolute and escaping targets fail.
  A directory swapped for a symlink between a check and its use (TOCTOU)
  cannot redirect an operation outside.
- Recursive operations (delete, copy, chmod/chown, archives, previews) never
  follow symlinks; a directory is descended only through a handle confirmed
  to be the directory that was checked.
- Content of regular files with more than one hard link is never read,
  copied, archived or chmod/chown'ed (another name of the inode may lie
  outside the root); deleting or renaming such a name is allowed.
- Writes go to a temporary file in the target directory, are synced and
  renamed into place after the precondition is re-checked; no-clobber
  creates use a hard link so an existing name is never replaced.
- chmod/chown go through the opened file (`fchmod`/`fchown`); special bits
  (setuid, setgid, sticky) cannot be set; special files are refused.
- Archives: members named `../…`, `/…`, `..\…`, `C:\…` or `C:/…` are
  refused, as are symlinks leaving the root, anything below a refused link,
  hard links to files outside the archive (allowed ones are extracted as
  copies), devices and FIFOs; permission bits only (no setuid); at most
  100 000 entries; bytes actually written are limited to 10 GiB and to 100x
  the archive's size (at least 1 MiB), whatever the headers claim; nested
  archives are not extracted.
- File contents are never logged and never recorded in audit events (paths,
  sizes, counts and outcomes are). DockYard's own changes are published as
  file invalidations (paths only) for open views; external changes come from
  the watcher (#23). File names reach only callers with the root's read
  capability.

**Residual risks** (documented, accepted for v1): a writer outside DockYard
can still change a file in the few syscalls between the re-check and the
rename (the ETag check then compares against the state just before); bind
mounts inside a volume are traversed like directories (`os.Root` does not
stop at mount points); on Linux `os.Root` uses a per-component `openat`
walk rather than `openat2(RESOLVE_BENEATH)`; job records of file jobs list
the paths they touched to everyone allowed to read those jobs.

## UI (Phase 6, #22)

The browser interactions of #15 — breadcrumbs, the `..` row and drop
target, multi-select, keyboard shortcuts, context menus, drag and drop,
CodeMirror 6 editing, unsaved-buffer and external-change conflict dialogs,
progress and cancellation — are built on these routes in #22 and #23.
