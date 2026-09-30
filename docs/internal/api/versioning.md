# API versioning and breaking changes

## Versions

- The public API version is the path prefix: `/api/v1`. `info.version` in
  the OpenAPI document is `v1`. There is no per-request version header.
- Docker Manager ships only rolling `edge` images from `main` (#25); there are no
  semver releases or tags. The web UI is built from the same commit as the
  manager, so it always matches. **External clients** (API tokens, #31) are
  why the contract must not break silently.
- The agent protocol `docker-manager.agent/v1` is versioned separately; its rules
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

## Breaking changes (need explicit approval)

Anything that can break an existing, correct client, for example:

- removing or renaming an operation, path, parameter, member or enum value;
  changing an `operationId` (it names generated client functions);
- adding a required parameter or request member, tightening validation
  (shorter limits, new patterns, fewer enum values in requests);
- changing a type, format, status code, media type, the error shape or the
  meaning of an existing error code;
- removing a response member or making it optional;
- changing authentication requirements of an existing operation.

Changes to `x-docker-manager-capability` or `x-docker-manager-scope` are authorization
changes: they are visible in review through the route inventory
(`TestRouteInventory` requires the inventory to change with them) and must
be agreed with #17.

## Reviewing for breaking changes

There is no automated contract check any more: the former oasdiff-based
`api-contract` workflow and its `api-breaking-change` label were removed on
2026-09-25 with the move to Forgejo (and not restored with the move back
to GitHub on 2026-09-30). Breaking changes are found by review:

- `api/openapi.json` is committed and `TestOpenAPISnapshot` (part of
  `go test ./...`) fails when it is stale, so every contract change shows
  up in the change's diff of `api/openapi.json`.
- The author and the reviewer read that diff against the lists above.

### Approving a breaking change

A breaking change needs the owner's explicit approval. The change's
description must contain a section `## API breaking change` with:

1. what breaks and for whom (UI, API-token clients),
2. why a compatible alternative (new operation, new optional member,
   deprecation period) is not possible,
3. the migration path for clients.

## Deprecation

Prefer deprecation to breaking: mark the operation or member
`deprecated: true` in OpenAPI (Huma `Deprecated`), describe the replacement,
keep it working, and remove it only in a later approved change once the UI and
known clients no longer use it. A deprecated operation should also answer
with a `Deprecation` header and a `Link` to its replacement (RFC 9745) so
clients notice at run time.
