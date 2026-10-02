# Registry connections (#19)

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

Guide: `docs/internal/architecture/registries.md`. Owner-administered, write-only
registry credentials (`internal/manager/registries`, `app.Manager.Registries()`).

- **Never accept a credential in a request or job input.** Resolve the
  image's connection with `Registries().Select(ctx,
  domain.RegistrySelectRequest{Reference, EnvironmentID, StackID,
  ConnectionID})` (map `*domain.AmbiguousRegistryError` →
  `ambiguous_registry_connection`, revoked → `registry_connection_revoked`
  via the API's `registryError`) and put the selected ID into the job input
  as `jobspec.CredentialRefs` (`"registryConnections": [id]`). The engine
  resolves it to `protocol.CommandSecrets` at every dispatch and audits the
  use; a deleted/revoked connection fails the job (`credential_unavailable`).
- **Agent executors** read `sc.Secrets` (memory only, never journaled) and
  call `regauth.ForReference(sc.Secrets, ref, required)` /
  `regauth.All(sc.Secrets)` for the Engine/Compose adapters. Never fall
  back to anonymous when the input named a connection.
- **Manager-side digest checks** (#20): `Registries().Check(ctx,
  registries.CheckRequest{...})` (cached, deduplicated, rate-limit aware,
  `regclient` error classes). Image references and hosts: `internal/imageref`.
- **Rate limits are per host and credential** (#217): the `regclient`
  cooldown after a 429 is keyed by API host and credential key, and every
  registry response is reported to the observer the registries service
  sets (`SetRateLimitObserver`), which keeps the last reported pull limit
  per host and connection (`''` anonymous) in `registry_pull_limits`.
  Never key rate-limit state by host alone, never store or log Docker
  Hub's `docker-ratelimit-source` (an IP address), and never let recording
  a limit fail a check. Every new registry request path goes through
  `authorized`, so it is reported too.
- Tests: fake registry `regclient/regtest`; register secrets as
  `canary.RegistryCredential`.
