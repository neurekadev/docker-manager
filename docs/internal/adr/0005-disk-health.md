# ADR 0005: Disk health with smartctl in a privileged agent

- Status: accepted
- Date: 2026-09-29
- Issues: #143 (disk health: SMART and RAID monitoring); builds on #5
  (observation), #21 (no CLI execution) and #10 (the restic runner)

## Context

Users want to know when a disk of a server is about to fail and when a RAID
array lost a member, on the environment's page and, in a later change, as an
alert. Two sources exist on Linux:

- **SMART** data of each disk (ATA attributes, the NVMe health log, SCSI
  error counters). Reading it needs raw device access: `SG_IO` ioctls on
  `/dev/sd*` (ATA and SCSI, `CAP_SYS_RAWIO`) and admin commands on
  `/dev/nvme*` (`CAP_SYS_ADMIN`), on device nodes a container does not
  have by default.
- **Software RAID and ZFS state**: `/proc/mdstat` and
  `/proc/spl/kstat/zfs/<pool>/state`, readable from any procfs the agent
  already reads for host telemetry (`DOCKER_AGENT_HOST_PROC`).

Parsing SMART is a large, device-specific job (vendor attribute tables, USB
bridges, NVMe and SCSI logs, standby handling). smartmontools' `smartctl`
does it, is packaged by every distribution and prints a stable JSON format
(`--json`) since 7.0. No Go library covers the same ground.

## Decisions

### smartctl, a separate program in the agent image

The agent image ships a static `smartctl` built from the pinned,
SHA-256-verified smartmontools source release (7.5,
`deploy/docker/agent.Dockerfile`), and the agent runs it through
`internal/agent/smartctl` — after restic, the second and last process
execution Docker Manager allows (forbidigo exception for that one file):

- a fixed binary (`DOCKER_AGENT_SMARTCTL_BINARY`, default
  `/usr/local/bin/smartctl`), never a shell, arguments that are flags, a
  device type and a `/dev/...` path from smartctl's own scan (anything
  that could be read as an option is refused);
- a minimal environment (`LANG=C`), bounded output (4 MiB) and stderr tail,
  30 s per call, cancellation with an interrupt and a kill after a grace
  period;
- read-only use: `--scan-open` and `-a` only. The agent never starts a
  self-test, never changes a drive setting, and passes `-n standby` so a
  disk in standby is not spun up (it keeps its previous values, marked
  sleeping). "Check disks now" means a fresh read, not a self-test.
- a smartctl that does not exit after it was killed (a process in
  uninterruptible I/O on a dying disk, which no signal ends) is given up
  on 10 s after the kill: the read reports a timeout, and that device is
  not read again until the stuck process exits (#172).

**Amendment (#172): a disk asleep at every check.** `-n standby` alone
never reads a disk that is always asleep when the agent looks (a NAS
whose disks spin down sooner than the read interval), so it would never
be checked again after an agent restart. Like smartd's standby skip
limit, a disk not read for `DOCKER_AGENT_SMART_WAKE_AFTER` (default 24 h,
1 h to 30 days) is read with `-n never`, which wakes it: at most one
spin-up per day by default. `0` keeps the never-wake behavior for
operators who prefer it.

**Amendment (#212): device statistics.** `-a` leaves out an ATA
drive's device statistics log, where SATA SSDs report their wear (page 7)
and drives their maximum operating temperature and the time spent above
it (page 5). ATA devices are read with `-l devstat,5 -l devstat,7` too:
two more read-only log reads, no setting changed, `-n standby` unchanged.
A drive without the log or a page reports less (smartctl may set exit
bit 2, which the agent does not judge by when the JSON is complete).

**Licensing.** smartmontools is GPL-2.0-or-later; Docker Manager is
AGPL-3.0. The agent does not link it: it executes an independent program
and parses its output, which is aggregation, not a derivative work. The
image carries smartmontools' `COPYING` and the exact source tarball the
binary was built from under `/usr/share/doc/smartmontools/`, so the
corresponding source travels with every copy of the image.

### The agent runs privileged

The documented compose files and the manager's install commands and move
files run the agent with `privileged: true` (`docker run --privileged`).
This gives smartctl `CAP_SYS_RAWIO`, `CAP_SYS_ADMIN` and every device
node of the host, including disks attached later (they appear after an
agent restart, when the container's `/dev` is populated again).

The agent already mounts the Docker socket, which is root-equivalent on the
host: anyone who controls the agent can start a privileged container
anyway. Privileged mode therefore adds no authority an attacker with the
agent's control would not already have, while the alternatives are worse
to operate:

- listing each disk under `devices:` plus `cap_add` differs per host,
  breaks when disks change and still misses NVMe controllers or new disks;
- a helper container started per check through the Engine would add a
  second image, a second lifecycle and the same privileges.

Operators who do not want it remove `privileged: true` (the agent reports
`no_access` and the UI says how to enable it) or turn SMART off with
`DOCKER_AGENT_SMART_ENABLED=false`. RAID state needs no privilege.

### Out of scope

- **btrfs** RAID profiles (their state is not in procfs; `btrfs device
  stats` would need another program).
- **Hardware RAID controllers** (MegaRAID, Areca, HP Smart Array, ...):
  disks behind them need controller-specific `-d` types and the array state
  comes from vendor tools. smartctl may list such disks; the controller's
  own array state is not read.
- ZFS details beyond the pool's state (vdev errors, scrub progress): they
  are not in procfs.
- Starting SMART self-tests or RAID scrubs from the UI.

## Consequences

- The agent image grows by a static smartctl (about 1 MB) and the source
  tarball (about 1 MB); the smartctl stage compiles natively for each
  target platform (CI builds each architecture on its own runner).
- Upgrading smartmontools means bumping the version and SHA-256 in the
  Dockerfile together (`docs/internal/support-matrix.md`).
- Existing installations get disk health after adding `privileged: true`
  to their agent service (the upgrade note in the Quickstart).
