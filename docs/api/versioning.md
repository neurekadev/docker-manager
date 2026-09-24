# API versioning and breaking changes

## Versions

- The public API version is the path prefix: `/api/v1`. `info.version` in
  the OpenAPI document is `v1`. There is no per-request version header.
- DockYard ships only rolling `edge` images from `main` (#25); there are no
  semver releases or tags. The web UI is built from the same commit as the
  manager, so it always matches. **External clients** (API tokens, #31) are
  why the contract must not break silently.
- The agent protocol `dockyard.agent/v1` is versioned separately; its rules
  and the N-1 agent window are in [agent-v1.md](../protocol/agent-v1.md#version-window-34).

## Compatible changes (allowed any time)

- New operations, new optional query/header parameters, new optional request
  body members.
- New response members, new response headers, new documented error codes or
  statuses for new situations, new enum values in **responses** (clients
  must tolerate unknown values), new SSE event types or stream topics
  (clients ignore unknown events).
- Relaxing request validation (longer max lengths, fewer required members).
- Documentation, examples, summaries.

## Breaking changes (need the explicit label)

Anything that can break an existing, correct client, for example:

- removing or renaming an operation, path, parameter, member or enum value;
  changing an `operationId` (it names generated client functions);
- adding a required parameter or request member, tightening validation
  (shorter limits, new patterns, fewer enum values in requests);
- changing a type, format, status code, media type, the error shape or the
  meaning of an existing error code;
- removing a response member or making it optional;
- changing authentication requirements of an existing operation.

Changes to `x-dockyard-capability` or `x-dockyard-scope` are authorization
changes: they are not detected by the diff tool but by the route inventory
(`TestRouteInventory` requires the inventory to change with them, which makes
them visible in review) and must be agreed with #17.

## The breaking-change check

`scripts/api-breaking.sh` compares the PR's `api/openapi.json` with the
version at the merge base with `main`, using **oasdiff v1.32.1** (a pinned
release binary verified by SHA-256; the script downloads it into
`~/.cache/dockyard/` when missing).

```bash
bash scripts/api-breaking.sh            # against origin/main
bash scripts/api-breaking.sh <base-ref> # against another base
```

It prints the full changelog, then fails when oasdiff reports a breaking
change at level `ERR`. The CI workflow `.github/workflows/api-contract.yaml`
runs it on every pull request (and again when labels change).

### Approving a breaking change

Add the label **`api-breaking-change`** to the pull request. The check then
still runs and prints the breaking changes, but passes. The PR description
must contain a section `## API breaking change` with:

1. what breaks and for whom (UI, API-token clients),
2. why a compatible alternative (new operation, new optional member,
   deprecation period) is not possible,
3. the migration path for clients.

Removing the label makes the check fail again. Without the label there is no
way to merge a breaking diff: `generate.sh --check` keeps the committed spec
in sync with the code, and the check compares that spec.

## Deprecation

Prefer deprecation to breaking: mark the operation or member
`deprecated: true` in OpenAPI (Huma `Deprecated`), describe the replacement,
keep it working, and remove it only in a later labelled PR once the UI and
known clients no longer use it. A deprecated operation should also answer
with a `Deprecation` header and a `Link` to its replacement (RFC 9745) so
clients notice at run time.
