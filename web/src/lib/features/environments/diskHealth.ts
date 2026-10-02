// Disk health and RAID of an environment (#143): pure view models for the
// System tab's "Disk health" and "RAID" cards. The manager sends the
// agent's SMART read of every disk (a disk in standby keeps its previous
// values, marked sleeping) and the software RAID and ZFS pool state; this
// module turns them into badges, issues in words, notices and the check
// toasts. Numbers go through the shared formatters ($lib/ui format).
import type { Schema } from '$lib/api/client';
import {
	formatBytes,
	formatDuration,
	formatHours,
	formatPercent,
	formatTemperature
} from '$lib/ui';

export type DiskHealth = Schema<'DiskHealth'>;
export type DiskDevice = Schema<'DiskDevice'>;
export type RaidHealth = Schema<'RAIDHealth'>;
export type RaidArray = Schema<'RAIDArray'>;
export type RaidMember = Schema<'RAIDMember'>;
export type DiskAttributeRow = Schema<'DiskAttributeRow'>;
export type DiskValue = Schema<'DiskValue'>;

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
		case 'timeout':
			return "Didn't answer in time";
		case 'missing':
			return 'No longer found: it may have failed or been removed';
		case 'smart_disabled':
			return 'SMART is turned off on the disk';
		case 'no_data':
			return 'Reported no health data';
		default:
			return "Couldn't open the disk";
	}
}

export function diskBadge(d: DiskDevice): Badge {
	const b = DISK_BADGE[d.state] ?? { status: 'unknown', label: 'Unknown' };
	if (d.state === 'error' && d.errorCode === 'missing')
		return { status: 'warning', label: 'Missing', title: unreadableReason(d.errorCode) };
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
	const limit = exceededLimit(d);
	if (limit !== undefined)
		out.push(
			`Too hot: ${formatTemperature(d.temperatureC)} (limit ${formatTemperature(limit)})`
		);
	else if (d.criticalWarning === NVME_WARN_TEMPERATURE) out.push('Too hot');
	if (d.criticalWarning !== NVME_WARN_TEMPERATURE) {
		if (d.passed === false) out.push('Self-assessment failed');
		if (d.criticalWarning) out.push('Critical warning');
	}
	const failing = (d.failingAttributes ?? []).filter((a) => a.whenFailed === 'now');
	for (const a of failing) out.push(`${attributeName(a.name)} failing`);
	const n = (v: number | undefined) => v ?? 0;
	if (n(d.reallocatedSectors) > 0) out.push(count(n(d.reallocatedSectors), 'reallocated sector'));
	if (n(d.pendingSectors) > 0) out.push(count(n(d.pendingSectors), 'pending sector'));
	if (n(d.offlineUncorrectable) > 0)
		out.push(count(n(d.offlineUncorrectable), 'uncorrectable sector'));
	if (n(d.reportedUncorrectable) > 0)
		out.push(count(n(d.reportedUncorrectable), 'uncorrectable error'));
	if (n(d.endToEndErrors) > 0) out.push(count(n(d.endToEndErrors), 'end-to-end error'));
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
	if (n(d.overTemperatureMinutes) > 0)
		out.push(`Ran above its temperature limit for ${minutes(n(d.overTemperatureMinutes))}`);
	if (n(d.criticalTemperatureMinutes) > 0)
		out.push(
			`Ran above its critical temperature for ${minutes(n(d.criticalTemperatureMinutes))}`
		);
	const past = (d.failingAttributes ?? []).filter((a) => a.whenFailed === 'past');
	for (const a of past) out.push(`${attributeName(a.name)} failed in the past`);
	return out;
}

/**
 * The disk's own temperature limit its reading is at or above (#212, the
 * agent's OverTemperatureLimit): its limit, else its critical one;
 * undefined when it is cooler or reports no limit.
 */
export function exceededLimit(d: DiskDevice): number | undefined {
	if (d.temperatureC === undefined) return undefined;
	for (const l of [d.temperatureLimitC, d.temperatureCriticalC])
		if (l !== undefined && d.temperatureC >= l) return l;
	return undefined;
}

/** The wear from which a disk counts as worn out (the agent's rule). */
export const WORN = 90;

/**
 * The NVMe critical warning that is only "too hot" (bit 1): a warning
 * that clears when the drive cools, not a failing drive (the agent's rule).
 */
export const NVME_WARN_TEMPERATURE = 2;

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

/** A missing disk ranks with the warnings (it raises one). */
const severity = (d: DiskDevice) =>
	d.state === 'error' && d.errorCode === 'missing' ? SEVERITY.warning : (SEVERITY[d.state] ?? 9);

/** Disks that need attention first, then by name and controller slot. */
export function sortDisks(ds: DiskDevice[]): DiskDevice[] {
	return [...ds].sort(
		(a, b) =>
			severity(a) - severity(b) ||
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

/**
 * A SMART value's verdict for the disk details (#210): OK, warning or
 * danger with a short label, by the agent's rules (DeriveDiskState), so a
 * mark never contradicts the disk's badge. null: the value is information
 * only (power-on hours, data written).
 */
export interface SmartCheck {
	tone: 'ok' | 'warn' | 'danger';
	label: string;
}

const OK: SmartCheck = { tone: 'ok', label: 'OK' };

/**
 * The self-assessment: passed OK, failed danger, or a warning when the
 * only NVMe critical warning is the temperature (it clears when the drive
 * cools; the agent calls that a warning); null when not reported.
 */
export function selfAssessmentCheck(d: DiskDevice): SmartCheck | null {
	if (d.passed === undefined) return null;
	if (d.passed) return { tone: 'ok', label: 'Passed' };
	if (d.criticalWarning === NVME_WARN_TEMPERATURE)
		return { tone: 'warn', label: 'Failed: too hot' };
	return { tone: 'danger', label: 'Failed' };
}

/**
 * The temperature against the disk's own limit (#212): at or above it a
 * warning, else OK, labelled with both ("72 °C (limit 70 °C)"); null
 * when the disk reports no reading or no limit (shown without a mark).
 */
export function temperatureCheck(d: DiskDevice): SmartCheck | null {
	const limit = exceededLimit(d) ?? d.temperatureLimitC ?? d.temperatureCriticalC;
	if (d.temperatureC === undefined || limit === undefined) return null;
	const label = `${formatTemperature(d.temperatureC)} (limit ${formatTemperature(limit)})`;
	return { tone: exceededLimit(d) === undefined ? 'ok' : 'warn', label };
}

/** The wear (percentage used): a warning from WORN percent; null when not reported. */
export function wearCheck(d: DiskDevice): SmartCheck | null {
	if (d.percentageUsed === undefined) return null;
	const label = `${formatPercent(d.percentageUsed)} used`;
	return { tone: d.percentageUsed >= WORN ? 'warn' : 'ok', label };
}

/**
 * The time the disk spent above its temperature limits in its lifetime:
 * any a warning ("34 min, 1 min above critical"), none OK ("Never");
 * null when the disk does not count it.
 */
export function hotTimeCheck(d: DiskDevice): SmartCheck | null {
	const over = d.overTemperatureMinutes;
	const crit = d.criticalTemperatureMinutes;
	if (over === undefined && crit === undefined) return null;
	if (!over && !crit) return { tone: 'ok', label: 'Never' };
	const parts: string[] = [];
	if (over) parts.push(minutes(over));
	if (crit) parts.push(`${minutes(crit)} above critical`);
	return { tone: 'warn', label: parts.join(', ') };
}

/** The ATA attributes the agent counts (raw value above 0: a warning), by ID. */
const ATA_COUNTED: Record<number, string> = {
	5: 'reallocated sector',
	184: 'end-to-end error',
	187: 'uncorrectable error',
	197: 'pending sector',
	198: 'uncorrectable sector'
};

/**
 * An ATA attribute's verdict: failing now danger; failed in the past or a
 * counted attribute with a raw value above 0 ("8 reallocated sectors")
 * warning; else OK for an attribute that can fail (pre-fail, a threshold
 * above 0 or a counted one); null for one that only informs (old age
 * without a threshold: power-on hours, temperature).
 */
export function attributeCheck(a: DiskAttributeRow): SmartCheck | null {
	if (a.whenFailed === 'now') return { tone: 'danger', label: 'Failing now' };
	if (a.whenFailed === 'past') return { tone: 'warn', label: 'Failed in the past' };
	const counted = ATA_COUNTED[a.id];
	if (counted && a.raw !== undefined && a.raw > 0)
		return { tone: 'warn', label: count(a.raw, counted) };
	if (counted || a.prefailure || (a.threshold ?? 0) > 0) return OK;
	return null;
}

/**
 * A health value's verdict: the NVMe critical warning (none OK, the
 * temperature alone a warning, else danger), available spare against its
 * minimum, wear from WORN percent, media errors and SCSI uncorrected
 * errors; null for the values that only inform.
 */
export function valueCheck(v: DiskValue, d: DiskDevice): SmartCheck | null {
	switch (v.key) {
		case 'critical_warning':
			if (v.value === 0) return OK;
			if (v.value === NVME_WARN_TEMPERATURE) return { tone: 'warn', label: 'Too hot' };
			return { tone: 'danger', label: 'Critical' };
		case 'available_spare':
			if (d.availableSpareThreshold === undefined) return null;
			return v.value < d.availableSpareThreshold
				? { tone: 'warn', label: 'Below the minimum' }
				: OK;
		case 'percentage_used':
			return v.value >= WORN ? { tone: 'warn', label: 'Worn' } : OK;
		case 'media_errors':
			return v.value > 0 ? { tone: 'warn', label: 'Errors found' } : OK;
		case 'temperature': {
			const c = temperatureCheck({ ...d, temperatureC: v.value });
			return c && { tone: c.tone, label: c.tone === 'ok' ? 'OK' : 'Too hot' };
		}
		case 'warning_temp_time':
		case 'critical_comp_time':
			return v.value > 0 ? { tone: 'warn', label: 'Ran hot' } : OK;
	}
	if (v.key.endsWith('.total_uncorrected_errors'))
		return v.value > 0 ? { tone: 'warn', label: 'Errors found' } : OK;
	return null;
}

/** An ATA attribute's type as smartctl names it. */
export function attributeType(a: DiskAttributeRow): string {
	return a.prefailure ? 'Pre-fail' : 'Old age';
}

/** An ATA attribute's raw value: smartctl's text when it says more ("36 (Min/Max 20/49)"). */
export function attributeRaw(a: DiskAttributeRow): string {
	if (a.rawText) return a.rawText;
	return a.raw === undefined ? '—' : wholeNumber(a.raw);
}

const wholeNumber = (v: number) => v.toLocaleString('en');

const minutes = (v: number) => formatDuration(v * 60);

/** NVMe counts data in units of 1,000 sectors of 512 bytes. */
const NVME_DATA_UNIT = 512_000;

/** Labels and formats of the health values smartctl reports, by key. */
const VALUES: Record<string, { label: string; format?: (v: number) => string }> = {
	critical_warning: {
		label: 'Critical warning',
		format: (v) => (v ? `0x${v.toString(16).padStart(2, '0')}` : 'None')
	},
	temperature: { label: 'Temperature', format: formatTemperature },
	available_spare: { label: 'Available spare', format: formatPercent },
	available_spare_threshold: { label: 'Available spare threshold', format: formatPercent },
	percentage_used: { label: 'Percentage used', format: formatPercent },
	data_units_read: { label: 'Data read', format: (v) => formatBytes(v * NVME_DATA_UNIT) },
	data_units_written: { label: 'Data written', format: (v) => formatBytes(v * NVME_DATA_UNIT) },
	host_reads: { label: 'Host read commands' },
	host_writes: { label: 'Host write commands' },
	controller_busy_time: { label: 'Controller busy time', format: minutes },
	power_cycles: { label: 'Power cycles' },
	power_cycle_count: { label: 'Power cycles' },
	power_on_hours: { label: 'Powered on', format: formatHours },
	unsafe_shutdowns: { label: 'Unsafe shutdowns' },
	media_errors: { label: 'Media and data integrity errors' },
	num_err_log_entries: { label: 'Error log entries' },
	warning_temp_time: { label: 'Time above the warning temperature', format: minutes },
	critical_comp_time: { label: 'Time above the critical temperature', format: minutes }
};

/**
 * A health value as shown: its label and value in words. Nested SCSI
 * counters name their operation first ("Read: total errors corrected").
 */
export function healthValue(v: DiskValue): { label: string; text: string } {
	const spec = VALUES[v.key];
	if (spec) return { label: spec.label, text: (spec.format ?? wholeNumber)(v.value) };
	const dot = v.key.indexOf('.');
	const label =
		dot > 0
			? `${attributeName(v.key.slice(0, dot))}: ${attributeName(v.key.slice(dot + 1)).toLowerCase()}`
			: attributeName(v.key);
	return { label, text: wholeNumber(v.value) };
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
		(d) =>
			d.state === 'failing' ||
			d.state === 'warning' ||
			(d.state === 'error' && d.errorCode === 'missing')
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

/**
 * A kernel device name as its path: "sda1" → "/dev/sda1"; the kernel
 * writes a "/" in a name as "!" ("cciss!c0d0p1" → "/dev/cciss/c0d0p1").
 */
export function devPath(name: string): string {
	return name.startsWith('/') ? name : `/dev/${name.replaceAll('!', '/')}`;
}

/** An array's name as shown: md arrays by their device path ("/dev/md0"), ZFS pools by name. */
export function arrayName(a: RaidArray): string {
	return a.kind === 'md' ? devPath(a.name) : a.name;
}

/**
 * The paths smartctl may list a member's disk under: "sda1" → /dev/sda,
 * "nvme0n1p2" → /dev/nvme0 or /dev/nvme0n1; none for other devices.
 */
export function memberDiskPaths(name: string): string[] {
	const nvme = name.match(/^(nvme\d+)(n\d+)(?:p\d+)?$/);
	if (nvme) return [`/dev/${nvme[1]}`, `/dev/${nvme[1]}${nvme[2]}`];
	const disk = name.match(/^((?:sd|vd|hd|xvd)[a-z]+)\d*$/);
	return disk ? [`/dev/${disk[1]}`] : [];
}

/** The disk a member lives on, among the SMART devices; undefined when none matches. */
export function memberDisk(m: RaidMember, devices: DiskDevice[]): DiskDevice | undefined {
	for (const p of memberDiskPaths(m.name)) {
		const d = devices.find((d) => d.name === p);
		if (d) return d;
	}
	return undefined;
}

const MEMBER_ROLE: Record<RaidMember['state'], string> = {
	active: 'Active',
	spare: 'Spare',
	failed: 'Failed',
	replacement: 'Replacing a member',
	journal: 'Journal'
};

/** A member's role in words: "Active", "Spare", "Failed", "Active, write-mostly". */
export function memberRole(m: RaidMember): string {
	const role = MEMBER_ROLE[m.state] ?? 'Unknown';
	return m.writeMostly ? `${role}, write-mostly` : role;
}

/** md's raid5/6 parity algorithms by number (mdadm's names). */
const MD_ALGORITHMS: Record<number, string> = {
	0: 'left-asymmetric',
	1: 'right-asymmetric',
	2: 'left-symmetric',
	3: 'right-symmetric',
	4: 'parity-first',
	5: 'parity-last',
	16: 'left-asymmetric-6',
	17: 'right-asymmetric-6',
	18: 'left-symmetric-6',
	19: 'right-symmetric-6',
	20: 'parity-first-6'
};

/** An md layout in words: "Left-symmetric (algorithm 2)", "2 near-copies"; "" when not reported. */
export function raidLayout(a: RaidArray): string {
	if (!a.layout) return '';
	const m = a.layout.match(/^algorithm (\d+)$/);
	const name = m ? MD_ALGORITHMS[Number(m[1])] : undefined;
	if (name) return `${name[0].toUpperCase()}${name.slice(1)} (${a.layout})`;
	return a.layout[0].toUpperCase() + a.layout.slice(1);
}

/** The write-intent bitmap in words: "Yes, 64 MB chunks", "None". */
export function raidBitmap(a: RaidArray): string {
	if (!a.bitmap) return 'None';
	return a.bitmapChunkBytes ? `Yes, ${formatBytes(a.bitmapChunkBytes)} chunks` : 'Yes';
}

/** The array's kind: "RAID 1\", "RAID 10", "Linear", "ZFS pool". */
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

/** A member's label by its path and whether it is highlighted (failed). */
export function memberLabel(m: RaidMember): { text: string; failed: boolean; title: string } {
	const path = devPath(m.name);
	switch (m.state) {
		case 'failed':
			return { text: `${path} failed`, failed: true, title: `${path} has failed` };
		case 'spare':
			return { text: `${path} spare`, failed: false, title: `${path} is a spare` };
		case 'replacement':
			return {
				text: `${path} replacing`,
				failed: false,
				title: `${path} replaces a member`
			};
		case 'journal':
			return { text: `${path} journal`, failed: false, title: `${path} is the journal` };
	}
	return {
		text: path,
		failed: false,
		title: m.writeMostly ? `${path} (write-mostly)` : path
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
