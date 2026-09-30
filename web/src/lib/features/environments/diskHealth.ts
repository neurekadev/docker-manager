// Disk health and RAID of an environment (#143): pure view models for the
// System tab's "Disk health" and "RAID" cards. The manager sends the
// agent's SMART read of every disk (a disk in standby keeps its previous
// values, marked sleeping) and the software RAID and ZFS pool state; this
// module turns them into badges, issues in words, notices and the check
// toasts. Numbers go through the shared formatters ($lib/ui format).
import type { Schema } from '$lib/api/client';
import { formatBytes, formatDuration, formatHours, formatPercent } from '$lib/ui';

export type DiskHealth = Schema<'DiskHealth'>;
export type DiskDevice = Schema<'DiskDevice'>;
export type RaidHealth = Schema<'RAIDHealth'>;
export type RaidArray = Schema<'RAIDArray'>;
export type RaidMember = Schema<'RAIDMember'>;

/** Where the user docs explain how the agent gets access to the disks. */
export const DISK_ACCESS_DOCS =
	'https://docs.neureka.dev/docker-manager/monitoring/#give-the-agent-access-to-the-disks';

/** A disk's badge: a StatusBadge status (tone) and its label. */
export interface Badge {
	status: string;
	label: string;
	/** Tooltip: why a disk is unreadable. */
	title?: string;
}

const DISK_BADGE: Record<DiskDevice['state'], Badge> = {
	ok: { status: 'healthy', label: 'Healthy' },
	warning: { status: 'warning', label: 'Warning' },
	failing: { status: 'failing', label: 'Failing' },
	sleeping: { status: 'sleeping', label: 'Sleeping' },
	error: { status: 'unreadable', label: 'Unreadable' }
};

/** Why a disk could not be read, in words. */
export function unreadableReason(code: DiskDevice['errorCode']): string {
	switch (code) {
		case 'permission_denied':
			return 'The agent may not open this disk';
		case 'unsupported':
			return "Doesn't report SMART data";
		default:
			return "Couldn't open the disk";
	}
}

export function diskBadge(d: DiskDevice): Badge {
	const b = DISK_BADGE[d.state] ?? { status: 'unknown', label: 'Unknown' };
	if (d.state === 'error') return { ...b, title: unreadableReason(d.errorCode) };
	if (d.state === 'sleeping')
		return {
			...b,
			title: 'In standby: not woken for a check; the values are from the last read'
		};
	return b;
}

const count = (n: number, one: string, many = `${one}s`) =>
	`${n.toLocaleString('en')} ${n === 1 ? one : many}`;

/**
 * What is wrong with a disk, in words, most serious first: "Self-assessment
 * failed", "8 reallocated sectors", "Worn 92%", "Spare 3% (minimum 10%)".
 * Empty for a healthy disk.
 */
export function diskIssues(d: DiskDevice): string[] {
	if (d.state === 'error') return [unreadableReason(d.errorCode)];
	const out: string[] = [];
	if (d.passed === false) out.push('Self-assessment failed');
	if (d.criticalWarning) out.push('Critical warning');
	const failing = (d.failingAttributes ?? []).filter((a) => a.whenFailed === 'now');
	for (const a of failing) out.push(`${attributeName(a.name)} failing`);
	const n = (v: number | undefined) => v ?? 0;
	if (n(d.reallocatedSectors) > 0) out.push(count(n(d.reallocatedSectors), 'reallocated sector'));
	if (n(d.pendingSectors) > 0) out.push(count(n(d.pendingSectors), 'pending sector'));
	if (n(d.offlineUncorrectable) > 0)
		out.push(count(n(d.offlineUncorrectable), 'uncorrectable sector'));
	if (n(d.reportedUncorrectable) > 0)
		out.push(count(n(d.reportedUncorrectable), 'uncorrectable error'));
	if (n(d.mediaErrors) > 0) out.push(count(n(d.mediaErrors), 'media error'));
	if (n(d.grownDefects) > 0) out.push(count(n(d.grownDefects), 'grown defect'));
	if (n(d.uncorrectedErrors) > 0) out.push(count(n(d.uncorrectedErrors), 'uncorrected error'));
	if (d.percentageUsed !== undefined && d.percentageUsed >= WORN)
		out.push(`Worn ${formatPercent(d.percentageUsed)}`);
	if (
		d.availableSpare !== undefined &&
		d.availableSpareThreshold !== undefined &&
		d.availableSpare < d.availableSpareThreshold
	)
		out.push(
			`Spare ${formatPercent(d.availableSpare)} (minimum ${formatPercent(d.availableSpareThreshold)})`
		);
	const past = (d.failingAttributes ?? []).filter((a) => a.whenFailed === 'past');
	for (const a of past) out.push(`${attributeName(a.name)} failed in the past`);
	return out;
}

/** The wear from which a disk counts as worn out (the agent's rule). */
export const WORN = 90;

/** An ATA attribute name in words: "Reallocated_Sector_Ct" → "Reallocated sector ct". */
export function attributeName(name: string): string {
	const s = name.replaceAll('_', ' ').trim();
	return s ? s[0].toUpperCase() + s.slice(1).toLowerCase() : 'An attribute';
}

/** The issues cell: the issues joined, "None" or "—" (never read). */
export function issuesText(d: DiskDevice): string {
	const issues = diskIssues(d);
	if (issues.length) return issues.join(', ');
	if (d.state === 'sleeping' && !d.readAt) return '—';
	return 'None';
}

/** Power-on time in words ("3 y 41 d"); "—" when the disk does not say. */
export function poweredOn(d: DiskDevice): string {
	return d.powerOnHours === undefined ? '—' : formatHours(d.powerOnHours);
}

/** The disk's kind in words: "SSD", "HDD, 7,200 rpm", "NVMe SSD". */
export function diskKind(d: DiskDevice): string {
	if (d.protocol === 'nvme') return 'NVMe SSD';
	if (d.rotationRpm === undefined) return '';
	if (d.rotationRpm === 0) return 'SSD';
	return `HDD, ${d.rotationRpm.toLocaleString('en')} rpm`;
}

/** Capacity in the shared byte format; "" when unknown. */
export function capacity(d: DiskDevice): string {
	return d.capacityBytes ? formatBytes(d.capacityBytes) : '';
}

const SEVERITY: Record<DiskDevice['state'], number> = {
	failing: 0,
	warning: 1,
	error: 2,
	sleeping: 3,
	ok: 4
};

/** Disks that need attention first, then by name and controller slot. */
export function sortDisks(ds: DiskDevice[]): DiskDevice[] {
	return [...ds].sort(
		(a, b) =>
			(SEVERITY[a.state] ?? 9) - (SEVERITY[b.state] ?? 9) ||
			a.name.localeCompare(b.name, 'en', { numeric: true }) ||
			a.type.localeCompare(b.type, 'en', { numeric: true })
	);
}

/**
 * True when the agent found disks but none reports SMART data (virtual
 * disks, unknown USB bridges). A scan without any disk is noDisks.
 */
export function noSmartDisks(h: DiskHealth): boolean {
	return (
		h.status === 'ok' &&
		h.devices.length > 0 &&
		h.devices.every((d) => d.state === 'error' && d.errorCode === 'unsupported')
	);
}

/**
 * True when a finished scan found no disk at all. The agent reports
 * no_access instead when the server has disks it can't open, so this is a
 * server whose disks smartctl does not list (a virtual machine's virtio
 * disks) or one without local disks. While the first read runs it is
 * false (the card says "Reading the disks…").
 */
export function noDisks(h: DiskHealth): boolean {
	return h.status === 'ok' && h.devices.length === 0 && !!h.checkedAt;
}

/** A key per disk: disks behind one RAID controller share its path. */
export function diskKey(d: DiskDevice): string {
	return `${d.name}|${d.type}`;
}

/**
 * The device's name as shown: the path, plus the controller slot when
 * another disk shares the path ("/dev/bus/0 (megaraid,1)").
 */
export function deviceName(d: DiskDevice, all: DiskDevice[]): string {
	const shared = all.some((o) => o !== d && o.name === d.name);
	return shared ? `${d.name} (${d.type})` : d.name;
}

/** Notice text: plain parts and code (a setting to type). */
export type NoticePart = string | { code: string };

/** A notice in place of (or above) the disk list. */
export interface DiskNotice {
	tone: 'info' | 'warn';
	title: string;
	body?: NoticePart[];
	/** Link to the docs (no access). */
	href?: string;
	linkLabel?: string;
	/** The list is left out (nothing to show). */
	replacesList: boolean;
}

/** The notice the Disk health card shows for a report, or null. */
export function diskNotice(h: DiskHealth): DiskNotice | null {
	switch (h.status) {
		case 'agent_outdated':
			return {
				tone: 'info',
				title: 'Update the agent to see disk health.',
				body: ['This agent is older than disk health. Upgrade it like any other stack.'],
				replacesList: true
			};
		case 'disabled':
			return {
				tone: 'info',
				title: 'Disk health is turned off for this agent.',
				body: [
					'To turn it on, remove ',
					{ code: 'DOCKER_AGENT_SMART_ENABLED' },
					' from the agent’s settings and deploy it again.'
				],
				replacesList: true
			};
		case 'no_access':
			return {
				tone: 'warn',
				title: 'Docker Manager can’t read this server’s disks.',
				body: [
					'Run the agent with ',
					{ code: 'privileged: true' },
					' in its compose.yaml and deploy it again.'
				],
				href: DISK_ACCESS_DOCS,
				linkLabel: 'How to give the agent access',
				replacesList: true
			};
		case 'not_installed':
			return {
				tone: 'warn',
				title: 'This agent has no disk health tool.',
				body: ['Update the agent to the current image.'],
				replacesList: true
			};
		case 'error':
			return {
				tone: 'warn',
				title: 'The disks could not be scanned.',
				body: ['Docker Manager tries again with the next check.'],
				replacesList: h.devices.length === 0
			};
		case 'ok':
			if (noSmartDisks(h))
				return {
					tone: 'info',
					title: 'This server’s disks don’t report SMART data (for example virtual disks).',
					replacesList: true
				};
			if (noDisks(h))
				return {
					tone: 'info',
					title: 'No disks with SMART data were found on this server.',
					body: ['Virtual disks, such as a virtual machine’s, don’t report it.'],
					replacesList: true
				};
			return null;
	}
	return null;
}

/**
 * Whether "Check disks now" applies: the agent can read the disks. Without
 * access a check changes nothing (the fix restarts the agent, which reads
 * them at once).
 */
export function canCheckDisks(h: DiskHealth): boolean {
	return h.status === 'ok' || h.status === 'error' || h.status === 'unknown';
}

/** The toast after a check: "Checked 6 disks on homelab". */
export function checkedToast(h: DiskHealth, environment: string): string {
	const readable = h.devices.filter((d) => d.state !== 'error' || d.errorCode !== 'unsupported');
	if (!readable.length) return `No disks with SMART data found on ${environment}`;
	return `Checked ${count(readable.length, 'disk')} on ${environment}`;
}

/** A one-line summary for the card: "4 disks, 1 needs attention". */
export function diskSummary(h: DiskHealth): string {
	const n = h.devices.length;
	if (!n) return '';
	const attention = h.devices.filter(
		(d) => d.state === 'failing' || d.state === 'warning'
	).length;
	if (!attention) return count(n, 'disk');
	return `${count(n, 'disk')}, ${attention} ${attention === 1 ? 'needs' : 'need'} attention`;
}

// ---------------------------------------------------------------- RAID

const RAID_BADGE: Record<RaidArray['state'], Badge> = {
	healthy: { status: 'healthy', label: 'Healthy' },
	degraded: { status: 'degraded', label: 'Degraded' },
	rebuilding: { status: 'rebuilding', label: 'Rebuilding' },
	checking: { status: 'checking', label: 'Checking' },
	failed: { status: 'failed', label: 'Failed' },
	inactive: { status: 'inactive', label: 'Inactive' }
};

export function raidBadge(a: RaidArray): Badge {
	return RAID_BADGE[a.state] ?? { status: 'unknown', label: 'Unknown' };
}

/** The array's kind: "RAID 1", "RAID 10", "Linear", "ZFS pool". */
export function raidLevel(a: RaidArray): string {
	if (a.kind === 'zfs') return 'ZFS pool';
	const m = a.level?.match(/^raid(\d+)$/);
	if (m) return `RAID ${m[1]}`;
	if (!a.level) return 'Software RAID';
	return a.level[0].toUpperCase() + a.level.slice(1);
}

/** Members in words: "2 of 2 disks working" ([n/m]), else a count. */
export function raidDisks(a: RaidArray): string {
	if (a.kind === 'zfs') return a.health ? `Pool ${a.health.toLowerCase()}` : '';
	if (a.devices) return `${a.active ?? 0} of ${count(a.devices, 'disk')} working`;
	return count(a.members.length, 'disk');
}

/** A member's label and whether it is highlighted (failed). */
export function memberLabel(m: RaidMember): { text: string; failed: boolean; title: string } {
	switch (m.state) {
		case 'failed':
			return { text: `${m.name} failed`, failed: true, title: `${m.name} has failed` };
		case 'spare':
			return { text: `${m.name} spare`, failed: false, title: `${m.name} is a spare` };
		case 'replacement':
			return {
				text: `${m.name} replacing`,
				failed: false,
				title: `${m.name} replaces a member`
			};
		case 'journal':
			return { text: `${m.name} journal`, failed: false, title: `${m.name} is the journal` };
	}
	return {
		text: m.name,
		failed: false,
		title: m.writeMostly ? `${m.name} (write-mostly)` : m.name
	};
}

/** Members in slot order, failed ones first. */
export function sortMembers(ms: RaidMember[]): RaidMember[] {
	return [...ms].sort(
		(a, b) => Number(b.state === 'failed') - Number(a.state === 'failed') || a.slot - b.slot
	);
}

const ACTION: Record<NonNullable<RaidArray['action']>, string> = {
	recovery: 'Rebuilding',
	resync: 'Resyncing',
	reshape: 'Reshaping',
	check: 'Checking',
	repair: 'Repairing'
};

/** A running (or waiting) sync: the meter's value and its text, or null. */
export function raidProgress(a: RaidArray): { percent: number | null; text: string } | null {
	if (!a.action) return null;
	const what = ACTION[a.action] ?? 'Syncing';
	if (a.pending) return { percent: null, text: `${what}: waiting to start` };
	const parts = [what];
	if (a.progress !== undefined) parts[0] = `${what} ${formatPercent(a.progress)}`;
	if (a.finishSeconds !== undefined) parts.push(`about ${formatDuration(a.finishSeconds)} left`);
	if (a.speedBytesPerSecond) parts.push(`${formatBytes(a.speedBytesPerSecond)}/s`);
	return { percent: a.progress ?? null, text: parts.join(', ') };
}

/** Arrays that need attention first, then by name. */
export function sortArrays(as: RaidArray[]): RaidArray[] {
	const rank: Record<RaidArray['state'], number> = {
		failed: 0,
		degraded: 1,
		rebuilding: 2,
		inactive: 3,
		checking: 4,
		healthy: 5
	};
	return [...as].sort(
		(a, b) =>
			(rank[a.state] ?? 9) - (rank[b.state] ?? 9) ||
			a.name.localeCompare(b.name, 'en', { numeric: true })
	);
}

/** Whether the RAID card shows (arrays found, or the read failed). */
export function showRaidCard(r: RaidHealth | undefined): boolean {
	return !!r && (r.arrays.length > 0 || r.status === 'error');
}

/** The small line under the disk list when no array exists. */
export function noRaidText(r: RaidHealth | undefined): string {
	return r?.status === 'ok' && r.arrays.length === 0 ? 'No RAID arrays found' : '';
}

/** The toast after "Check RAID now": "Checked 2 arrays on homelab". */
export function raidCheckedToast(r: RaidHealth, environment: string): string {
	if (!r.arrays.length) return `No RAID arrays found on ${environment}`;
	return `Checked ${count(r.arrays.length, 'array')} on ${environment}`;
}
