# Policy safety: updates, pruning and backups

Nothing destructive or automatic happens until you turn it on. Every
policy has an explicit schedule (five-field cron plus a time zone), a
preview, a run history and audit records; scheduled runs run as DockYard
itself, so they keep working when the user who created them leaves.

## Defaults at a glance

| Policy | Starts | Needs before it acts |
| --- | --- | --- |
| Image updates (all environments or one environment, with target exclusions) | check and run schedules **off** | an enabled policy, a successful digest check, then a run |
| Docker prune | every rule and the schedule **off**; volume rules need their own opt-in | an enabled rule; manual runs need a confirmation |
| Backups | schedule **off**, container shutdown **off** | a confirmed Recovery Key and an enabled schedule (or *Back up now*) |
| Repository verification | suggested weekly, per repository | an enabled schedule |

Suggested schedules (editable under **Settings → Schedule defaults**, and
per policy): backups 02:00 daily, update checks 03:00 daily, update runs
04:00 daily, prune Sundays 03:00, verification Sundays 05:00. Every policy
stores its own time zone, so changing a default never moves an existing
policy.

## Image updates

- Updates compare the **digest** behind the tag you use (for your host's
  platform), never tag names or dates. An unchanged digest does nothing.
- Any explicit tag can be followed, including `latest` (the UI warns that
  such tags change meaning). `image@sha256:…` references and build-only
  services are **ineligible** and shown as such, as are services with
  `pull_policy: always`.
- DockYard never edits your `compose.yaml`, override or `.env` files: an
  update pulls and recreates only the changed services from the deployed
  revision; if the files changed on disk and were not deployed, the run is
  refused until you deploy or revert them.
- Dependencies are respected: stopped services stay stopped, dependents
  with `restart: true` restart.
- **No automatic rollback.** A failed update (pull, start or health check)
  is reported and its digest **quarantined** so it is not retried; pin an
  older `@sha256` digest in your own file if you need to go back.
- Registry errors (401/403/429) stop the run without repeated pulls; the
  next check has to succeed first. Scheduled runs respect the policy's
  update window; manual runs do not.
- Pulling a tag moves it for every stack on that host using it.

## Docker prune

- Categories: stopped containers, dangling and unused images, unused
  networks, anonymous and named volumes (separately opted in), build
  cache.
- Never pruned: DockYard's own containers, images and volumes; the
  projects and images of DockYard stacks; containers with a saved DockYard
  recreate specification; anything a backup or a migration still needs.
- **Preview** shows exactly what a run would remove. A run re-checks each
  object right before removing it, removes at most 300 objects (the rest
  next time), and can run in the background; it stays a durable job either
  way.

## Backups

- Container shutdown during backups is off by default. With it on,
  containers stop in dependency order and restart afterwards, also after
  failures and cancellations.
- Retention always keeps a minimum number of snapshots (the floor), and
  only forgets snapshots of its own policy.
- See [Backup and restore](backup-restore.md) for the Recovery Key.

## Missed runs and time changes

- After downtime, backups, verification and update checks run **once** to
  catch up; prune and update runs that were missed are recorded as missed
  and **skipped** (they should not surprise you at an odd hour).
- A local time skipped by a daylight-saving change runs once at the first
  instant after the gap; a repeated local time runs once, at its first
  occurrence.
- A run never overlaps the previous run of the same policy.

## Who may do what

Enabling, editing and running policies are separate permissions (for
example `update_policy.manage`, `update.run`, `maintenance.run`, `backup.restore`);
restores and prune runs are marked high risk in the permission editor.
Protected DockYard objects are refused for everyone, the owner included.
