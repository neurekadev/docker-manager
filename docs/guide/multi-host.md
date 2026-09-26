# Multi-host operation

Every Docker host you manage is an **Environment**: one agent plus the
Engine it controls. The manager and the first agent run on host A
([Deployment](deployment.md)); every other host runs only an agent that
dials out to the public origin. Agents open no port.

## Add a host

1. On host A's UI: **Environments → Add environment**, name it (for
   example `host-b`), create the token. The page shows the command for
   "another Docker host".
2. On host B (Linux, Docker Engine 25.0+):

   ```bash
   cd deploy/remote-agent            # copy this directory to host B
   cp .env.example .env
   # .env: DOCKER_AGENT_MANAGER_URL=https://docker.example.com
   #       DOCKER_AGENT_ENROLLMENT_TOKEN=<token>
   #       DOCKER_AGENT_ENVIRONMENT_NAME=host-b
   docker compose up -d
   ```

   Remove the token from `.env` once the environment is online; it only
   works once anyway. For a private CA, put the bundle into the
   `docker-manager_agent_ca` volume and set `DOCKER_AGENT_MANAGER_CA_FILE`
   (comment in `deploy/remote-agent/compose.yaml`).
3. The environment comes online on the dashboard. Its agent validates the
   manager's certificate, never follows redirects and never falls back to
   plain HTTP.

One agent per Engine: a second agent for an enrolled Engine is refused
(`engine_already_enrolled`) unless you create the token with the intent to
**replace** the old agent, which revokes it. Cloned VMs share an Engine ID
and are refused (`engine_identity_conflict`) until you allow it for that
enrollment.

## Working across hosts

- The environment switcher at the top narrows every list to one
  environment, or shows all of them; lists report environments that could
  not be read instead of hiding them.
- Every open view updates live: containers starting, jobs progressing,
  files changed on the host, other users' changes. After a network
  interruption the app reconnects and refreshes what it may have missed.
- Permissions can be granted per environment, so a team can run its own
  hosts without seeing the others.

## When a host goes offline

The environment turns **offline** and keeps its last known state
(containers, stacks, metrics up to the gap, shown as a gap in the charts).
Jobs for it wait until the agent is back or fail after their deadline
(never retried behind your back when the outcome is unknown). When the
agent reconnects, the manager reconciles running jobs and inventories and
marks the environment online again.

## Move stacks and volumes between hosts

**Stacks → (stack) → Migrate**: choose the destination environment; the
preflight lists everything that could go wrong (images, name and port
conflicts, bind paths outside the project, disk space, downtime,
permission changes) before anything stops. The migration stops the stack,
copies the project directory and its volumes through the manager (with
checksums), deploys it on the destination and keeps the stopped source
until you confirm **Remove from source**. A failure before completion
rolls back and restarts the source. Volumes alone migrate from the volume
page. Details: [migrations.md](../architecture/migrations.md).

## Remove or re-attach a host

- **Remove an agent**: its credential stops working at once; the
  environment is detached and waits for a new agent.
- **Archive an environment**: the preview lists everything that belongs to
  it (stacks, policies, permission rules, backups); nothing on the host is
  touched, and backups are kept. An archived environment can be
  **re-attached** later with a token created for it.

Procedure and API: [removing-hosts.md](../operations/removing-hosts.md).

## Versions across hosts

Upgrade the manager first, then the agents. A manager serves agents of its
own and the previous minor release; the host page shows outdated agents
with upgrade instructions ([Upgrades and migrations](upgrades.md)).
