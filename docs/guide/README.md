# Docker Manager user and administrator guide

This guide is for the people who install and run Docker Manager. It walks
through the tasks in order and links to the reference documents for
details. Docker Manager has no semver releases yet: `main` publishes the
rolling `:edge` images `code.neureka.dev/docker-manager/docker-manager:edge` and
`code.neureka.dev/docker-manager/docker-agent:edge`.

| Task | Page |
| --- | --- |
| Put the manager, a local agent and a TLS reverse proxy on a Docker host | [Deployment](deployment.md) |
| Create the owner account and enroll the first agent | [First run](first-run.md) |
| Add more Docker hosts, handle offline hosts, remove or move them | [Multi-host operation](multi-host.md) |
| Install the web app, how it updates, what works offline | [PWA install, update and offline](pwa.md) |
| Upgrade Docker Manager, roll back, understand database migrations | [Upgrades and migrations](upgrades.md) |
| Back up and restore stacks, volumes and Docker Manager itself; the Recovery Key | [Backup and restore](backup-restore.md) |
| What updates, pruning and backups do by default, and how to turn them on safely | [Policy safety](policy-safety.md) |
| Logs, health, diagnostics, the support bundle and owner recovery | [Troubleshooting](troubleshooting.md) |

Reference:

- What Docker Manager supports (hosts, Engines, Compose features, browsers,
  versions): [support matrix](../support-matrix.md).
- Every configuration variable: [configuration.md](../configuration.md).
- Reverse proxy requirements in depth: [deployment.md](../deployment.md).
- The public API: [API conventions](../api/README.md) and
  `/api/v1/openapi.json` on your manager; streams:
  [streams.md](../api/streams.md); the agent protocol:
  [agent-v1.md](../protocol/agent-v1.md).
- Security review of v1 and its known limitations:
  [review-v1.md](../security/review-v1.md).
- What is and is not verified by automated tests: [verification status](../support-matrix.md#verification-status).

## Concepts in one minute

- The **manager** serves the web app and the API, stores users,
  permissions, stacks, policies, jobs and the audit log in SQLite, and
  never touches a Docker socket.
- An **agent** runs next to one Docker Engine, dials out to the manager and
  does the Docker, Compose, file and backup work there. It opens no port.
- An **Environment** is one agent plus the Engine it controls. One agent
  per Engine.
- Long operations (deploy, pull, build, update, prune, backup, restore,
  migration) are **jobs**: they survive a manager restart, show progress,
  can be cancelled where safe and are recorded in the audit log.
- Both containers run as **root** (UID 0); running them as another user
  is not supported. Docker socket access is equivalent to root on the
  host, so protect the manager like you protect root on every enrolled
  host.
