# First run: owner account and first agent

## 1. Create the owner account

Open the public origin (`https://docker.example.com`). A new manager shows
**Set up Docker Manager**: choose a username and a password (at least 15
characters; common and breached passwords are refused). This account is
the **instance owner**: it can do everything, cannot be disabled or
deleted, and ownership cannot be transferred in v1.

- Setup works only over HTTPS on the public origin. If it says the request
  did not arrive over HTTPS, you opened the internal address, or the proxy
  is not in `DOCKER_MANAGER_TRUSTED_PROXIES` (see
  [First-run setup over HTTPS](../deployment.md#first-run-setup-over-https)).
- Setup is single use: the first request wins, every later one gets
  "setup is complete".
- Restoring a lost manager instead? Choose **Import from backup** on this
  screen ([Backup and restore](backup-restore.md#restore-a-lost-manager)).

Right after setup, open **Settings → Profile and security** and add an
authenticator app or a passkey, then **Generate new codes** and store the
ten recovery codes somewhere safe.

## 2. Enroll the first agent (the Docker host Docker Manager runs on)

The example's `docker-agent` container is already running and waits for
a one-use enrollment token.

1. Go to **Environments → Add environment**. Enter a display name (for
   example the host name) and create the token. It is shown once, with the
   install commands for a co-located agent and for another host.
2. Hand the token to the running agent on stdin, so it never appears in a
   URL, a process list or the container configuration:

   ```bash
   cd deploy/caddy
   printf '%s\n' '<token>' | docker compose exec -T docker-agent docker-agent enroll
   # enrolled: agent …, environment …; the environment is online
   ```

3. The environment appears as **online** on the dashboard within seconds,
   with its containers, images, volumes, networks and metrics.

Without the UI (for automation), create the token inside the manager
container: `docker compose exec -T docker-manager docker-manager
enrollment create -name host-a` (add `-json` for scripts). Tokens expire
(1 hour by default), work once, and can be revoked on the same screen.

The co-located agent uses the internal plain-HTTP URL
`http://docker-manager:8080` with an explicit opt-in; the host page marks
this. Agents on other hosts always use the public HTTPS origin
([Multi-host operation](multi-host.md)).

## 3. Deploy a first stack

**Stacks → Create stack**: pick the environment, paste a `compose.yaml`
(and optionally an override file and `.env`), check the validation result
and create it. The files are written into a new project directory of the
stacks volume on that host; they are the source of truth. **Deploy** runs
as a job with progress; the stack shows its services, containers, logs and
files afterwards. Files next to `compose.yaml` (relative bind mounts such as
`./html`) go into the same project directory: upload them in the stack's
**Files** tab before deploying.

## 4. Invite other people

**Access → Users → Invite user → Create invite link** creates a
single-use link (shown once, 72 h by default; **Options** binds it to an
email address or changes the expiry). Docker Manager sends no email: send the
link yourself. The person opens it and registers their own account
(username and password). New users join the default group
**Restricted**, which can see nothing until you grant permissions to the
group or the user (**Access → Groups**). Grants are scoped: everything, one
environment, one stack, one container, and so on; a user rule overrides the
group's. The **Effective access** view and the preview before saving show
what a change does.

## 5. API tokens for scripts

**Settings → API tokens → New token**: pick exactly the grants the script
needs (never more than you have yourself) and an expiry. The token
(`dy_…`) is shown once; send it as `Authorization: Bearer dy_…`. Tokens
cannot manage users, tokens or the sign-in policy, and stop working when
revoked, expired, or when your own permissions shrink.

## Next

- [Multi-host operation](multi-host.md)
- [Policy safety](policy-safety.md): automatic updates, pruning and backups
  start disabled; turn them on deliberately.
