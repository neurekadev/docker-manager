// Disk health view models (#143): badges, issues in words, durations,
// notices, toasts and the RAID rows.
import { describe, expect, it } from 'vitest';
import { formatHours, formatTemperature } from '$lib/ui';
import {
	attributeName,
	canCheckDisks,
	checkedToast,
	deviceName,
	diskBadge,
	diskKey,
	diskIssues,
	diskKind,
	diskNotice,
	diskSummary,
	issuesText,
	memberLabel,
	noDisks,
	noRaidText,
	noSmartDisks,
	poweredOn,
	raidBadge,
	raidCheckedToast,
	raidDisks,
	raidLevel,
	raidProgress,
	showRaidCard,
	sortArrays,
	sortDisks,
	sortMembers,
	type DiskDevice,
	type DiskHealth,
	type RaidArray,
	type RaidHealth
} from './diskHealth';

const disk = (p: Partial<DiskDevice> = {}): DiskDevice => ({
	name: '/dev/sda',
	type: 'sat',
	protocol: 'ata',
	smartSupported: true,
	state: 'ok',
	...p
});

const health = (p: Partial<DiskHealth> = {}): DiskHealth => ({
	status: 'ok',
	checking: false,
	devices: [disk()],
	...p
});

const md = (p: Partial<RaidArray> = {}): RaidArray => ({
	kind: 'md',
	name: 'md0',
	level: 'raid1',
	state: 'healthy',
	devices: 2,
	active: 2,
	members: [
		{ name: 'sda1', slot: 0, state: 'active' },
		{ name: 'sdb1', slot: 1, state: 'active' }
	],
	...p
});

describe('formatters', () => {
	it('reads power-on hours in words with years and days', () => {
		expect(formatHours(0)).toBe('0 h');
		expect(formatHours(5)).toBe('5 h');
		expect(formatHours(24)).toBe('1 d');
		expect(formatHours(41 * 24 + 5)).toBe('41 d 5 h');
		expect(formatHours((3 * 365 + 41) * 24 + 7)).toBe('3 y 41 d');
		expect(formatHours(2 * 365 * 24)).toBe('2 y');
		expect(formatHours(-4)).toBe('0 h');
		expect(formatHours(Number.NaN)).toBe('0 h');
	});

	it('shows temperatures with up to two decimals', () => {
		expect(formatTemperature(38)).toBe('38 °C');
		expect(formatTemperature(41.5)).toBe('41.5 °C');
		expect(formatTemperature(40.126)).toBe('40.13 °C');
		expect(formatTemperature(undefined)).toBe('—');
	});
});

describe('disks', () => {
	it('labels every state and explains unreadable and sleeping disks', () => {
		expect(diskBadge(disk())).toEqual({ status: 'healthy', label: 'Healthy' });
		expect(diskBadge(disk({ state: 'warning' })).label).toBe('Warning');
		expect(diskBadge(disk({ state: 'failing' }))).toEqual({
			status: 'failing',
			label: 'Failing'
		});
		expect(diskBadge(disk({ state: 'sleeping' })).title).toContain('not woken');
		expect(diskBadge(disk({ state: 'error', errorCode: 'permission_denied' }))).toMatchObject({
			status: 'unreadable',
			label: 'Unreadable'
		});
		// A disk no scan finds any more raises a warning: it looks like one.
		expect(diskBadge(disk({ state: 'error', errorCode: 'missing' }))).toMatchObject({
			status: 'warning',
			label: 'Missing'
		});
		expect(diskBadge(disk({ state: 'error', errorCode: 'timeout' })).title).toBe(
			"Didn't answer in time"
		);
	});

	it('names the issues in words, most serious first', () => {
		expect(diskIssues(disk())).toEqual([]);
		expect(issuesText(disk())).toBe('None');
		expect(
			diskIssues(
				disk({
					state: 'failing',
					passed: false,
					reallocatedSectors: 8,
					pendingSectors: 1,
					reportedUncorrectable: 2,
					offlineUncorrectable: 3,
					failingAttributes: [
						{ id: 5, name: 'Reallocated_Sector_Ct', whenFailed: 'now' },
						{ id: 190, name: 'Airflow_Temperature_Cel', whenFailed: 'past' }
					]
				})
			)
		).toEqual([
			'Self-assessment failed',
			'Reallocated sector ct failing',
			'8 reallocated sectors',
			'1 pending sector',
			'3 uncorrectable sectors',
			'2 uncorrectable errors',
			'Airflow temperature cel failed in the past'
		]);
		expect(
			diskIssues(
				disk({
					protocol: 'nvme',
					state: 'failing',
					criticalWarning: 4,
					mediaErrors: 1200,
					percentageUsed: 92,
					availableSpare: 13,
					availableSpareThreshold: 20
				})
			)
		).toEqual([
			'Critical warning',
			'1,200 media errors',
			'Worn 92%',
			'Spare 13% (minimum 20%)'
		]);
		expect(diskIssues(disk({ protocol: 'nvme', percentageUsed: 89 }))).toEqual([]);
		expect(
			diskIssues(
				disk({ protocol: 'scsi', state: 'warning', grownDefects: 12, uncorrectedErrors: 1 })
			)
		).toEqual(['12 grown defects', '1 uncorrected error']);
		expect(issuesText(disk({ state: 'error', errorCode: 'unsupported' }))).toBe(
			"Doesn't report SMART data"
		);
		expect(issuesText(disk({ state: 'error', errorCode: 'open_failed' }))).toBe(
			"Couldn't open the disk"
		);
		expect(issuesText(disk({ state: 'error', errorCode: 'smart_disabled' }))).toBe(
			'SMART is turned off on the disk'
		);
		expect(issuesText(disk({ state: 'error', errorCode: 'no_data' }))).toBe(
			'Reported no health data'
		);
		expect(issuesText(disk({ state: 'error', errorCode: 'missing' }))).toContain(
			'No longer found'
		);
		// An NVMe drive that is only too hot: smartctl fails its
		// self-assessment, the words say why.
		expect(
			diskIssues(
				disk({ protocol: 'nvme', state: 'warning', passed: false, criticalWarning: 2 })
			)
		).toEqual(['Too hot']);
		expect(
			diskIssues(disk({ state: 'warning', endToEndErrors: 1, reallocatedSectors: 2 }))
		).toEqual(['2 reallocated sectors', '1 end-to-end error']);
		// A disk asleep since the agent started was never read.
		expect(issuesText(disk({ state: 'sleeping' }))).toBe('—');
		expect(issuesText(disk({ state: 'sleeping', readAt: '2026-09-29T10:00:00Z' }))).toBe(
			'None'
		);
	});

	it('shows power-on time, kind and attribute names in words', () => {
		expect(poweredOn(disk({ powerOnHours: (3 * 365 + 41) * 24 }))).toBe('3 y 41 d');
		expect(poweredOn(disk())).toBe('—');
		expect(diskKind(disk({ rotationRpm: 7200 }))).toBe('HDD, 7,200 rpm');
		expect(diskKind(disk({ rotationRpm: 0 }))).toBe('SSD');
		expect(diskKind(disk({ protocol: 'nvme' }))).toBe('NVMe SSD');
		expect(diskKind(disk())).toBe('');
		expect(attributeName('Current_Pending_Sector')).toBe('Current pending sector');
	});

	it('puts disks that need attention first', () => {
		const sorted = sortDisks([
			disk({ name: '/dev/sdb' }),
			disk({ name: '/dev/sdc', state: 'sleeping' }),
			disk({ name: '/dev/sdd', state: 'warning' }),
			disk({ name: '/dev/sda' }),
			disk({ name: '/dev/sde', state: 'failing' }),
			disk({ name: '/dev/sdf', state: 'error', errorCode: 'open_failed' }),
			disk({ name: '/dev/sdg', state: 'error', errorCode: 'missing' })
		]).map((d) => d.name);
		expect(sorted).toEqual([
			'/dev/sde',
			'/dev/sdd',
			'/dev/sdg',
			'/dev/sdf',
			'/dev/sdc',
			'/dev/sda',
			'/dev/sdb'
		]);
	});

	it('tells disks behind one controller path apart by their type', () => {
		const slot1 = disk({ name: '/dev/bus/0', type: 'megaraid,1' });
		const slot0 = disk({ name: '/dev/bus/0', type: 'megaraid,0' });
		const all = [slot1, slot0, disk()];
		expect(diskKey(slot0)).not.toBe(diskKey(slot1));
		expect(deviceName(slot1, all)).toBe('/dev/bus/0 (megaraid,1)');
		expect(deviceName(all[2], all)).toBe('/dev/sda');
		expect(sortDisks(all).map((d) => d.type)).toEqual(['megaraid,0', 'megaraid,1', 'sat']);
	});

	it('summarizes and toasts in words', () => {
		const h = health({
			devices: [
				disk(),
				disk({ name: '/dev/sdb', state: 'warning' }),
				disk({ name: '/dev/sdc', state: 'error', errorCode: 'unsupported' })
			]
		});
		expect(diskSummary(h)).toBe('3 disks, 1 needs attention');
		expect(
			diskSummary(
				health({
					devices: [
						disk(),
						disk({ name: '/dev/sdb', state: 'error', errorCode: 'missing' })
					]
				})
			)
		).toBe('2 disks, 1 needs attention');
		expect(diskSummary(health())).toBe('1 disk');
		expect(diskSummary(health({ devices: [] }))).toBe('');
		expect(checkedToast(h, 'homelab')).toBe('Checked 2 disks on homelab');
		expect(checkedToast(health(), 'nas')).toBe('Checked 1 disk on nas');
		expect(checkedToast(health({ devices: [] }), 'nas')).toBe(
			'No disks with SMART data found on nas'
		);
	});
});

describe('notices', () => {
	it('explains every status the card cannot list', () => {
		expect(diskNotice(health())).toBeNull();
		const noAccess = diskNotice(health({ status: 'no_access', devices: [] }))!;
		expect(noAccess.title).toBe('Docker Manager can’t read this server’s disks.');
		expect(noAccess.body).toContainEqual({ code: 'privileged: true' });
		expect(noAccess.href).toContain('/monitoring/#give-the-agent-access-to-the-disks');
		expect(diskNotice(health({ status: 'disabled', devices: [] }))!.title).toBe(
			'Disk health is turned off for this agent.'
		);
		expect(diskNotice(health({ status: 'agent_outdated', devices: [] }))!.title).toBe(
			'Update the agent to see disk health.'
		);
		// A finished scan without any disk (a VM's virtio disks, which
		// smartctl does not list) is not "disks without SMART data".
		expect(noSmartDisks(health({ devices: [] }))).toBe(false);
		const none = health({ devices: [], checkedAt: '2026-09-29T11:48:00Z' });
		expect(noDisks(none)).toBe(true);
		expect(diskNotice(none)!.title).toBe('No disks with SMART data were found on this server.');
		// While the first read runs, the card says it is reading.
		expect(noDisks(health({ devices: [], checking: true }))).toBe(false);
		expect(diskNotice(health({ devices: [], checking: true }))).toBeNull();
		expect(
			diskNotice(health({ devices: [disk({ state: 'error', errorCode: 'unsupported' })] }))!
				.title
		).toBe('This server’s disks don’t report SMART data (for example virtual disks).');
		expect(
			diskNotice(health({ devices: [disk({ state: 'error', errorCode: 'unsupported' })] }))!
				.replacesList
		).toBe(true);
		// A failed scan keeps the last known disks under the notice.
		expect(diskNotice(health({ status: 'error' }))!.replacesList).toBe(false);
		expect(diskNotice(health({ status: 'unknown', devices: [] }))).toBeNull();
	});

	it('offers the check only where the agent can read disks', () => {
		expect(canCheckDisks(health())).toBe(true);
		expect(canCheckDisks(health({ status: 'no_access' }))).toBe(false);
		expect(canCheckDisks(health({ status: 'disabled' }))).toBe(false);
		expect(canCheckDisks(health({ status: 'agent_outdated' }))).toBe(false);
		expect(canCheckDisks(health({ status: 'not_installed' }))).toBe(false);
	});
});

describe('RAID', () => {
	it('labels levels, states and members', () => {
		expect(raidLevel(md())).toBe('RAID 1');
		expect(raidLevel(md({ level: 'raid10' }))).toBe('RAID 10');
		expect(raidLevel(md({ level: 'linear' }))).toBe('Linear');
		expect(raidLevel(md({ level: undefined, state: 'inactive' }))).toBe('Software RAID');
		expect(
			raidLevel({
				kind: 'zfs',
				name: 'tank',
				state: 'degraded',
				health: 'DEGRADED',
				members: []
			})
		).toBe('ZFS pool');
		expect(raidBadge(md({ state: 'degraded' }))).toEqual({
			status: 'degraded',
			label: 'Degraded'
		});
		expect(raidBadge(md({ state: 'failed' })).label).toBe('Failed');
		expect(raidBadge(md({ state: 'rebuilding' })).label).toBe('Rebuilding');
		expect(raidDisks(md({ active: 1 }))).toBe('1 of 2 disks working');
		expect(raidDisks(md({ level: 'raid0', devices: undefined, active: undefined }))).toBe(
			'2 disks'
		);
		expect(memberLabel({ name: 'sdc1', slot: 2, state: 'failed' })).toMatchObject({
			text: 'sdc1 failed',
			failed: true
		});
		expect(memberLabel({ name: 'sdd1', slot: 3, state: 'spare' }).text).toBe('sdd1 spare');
		expect(
			sortMembers([
				{ name: 'sda1', slot: 0, state: 'active' },
				{ name: 'sdc1', slot: 2, state: 'failed' },
				{ name: 'sdb1', slot: 1, state: 'active' }
			]).map((m) => m.name)
		).toEqual(['sdc1', 'sda1', 'sdb1']);
	});

	it('shows a rebuild with its estimate, and a waiting one', () => {
		expect(raidProgress(md())).toBeNull();
		expect(
			raidProgress(
				md({
					state: 'rebuilding',
					action: 'recovery',
					progress: 17.3,
					finishSeconds: 4686,
					speedBytesPerSecond: 150 * 1024 * 1024
				})
			)
		).toEqual({ percent: 17.3, text: 'Rebuilding 17.3%, about 1 h 18 min left, 150 MB/s' });
		expect(raidProgress(md({ state: 'checking', action: 'check', progress: 45 }))).toEqual({
			percent: 45,
			text: 'Checking 45%'
		});
		expect(raidProgress(md({ action: 'resync', pending: true }))).toEqual({
			percent: null,
			text: 'Resyncing: waiting to start'
		});
	});

	it('shows the card only with arrays and says when there are none', () => {
		const none: RaidHealth = { status: 'ok', arrays: [] };
		expect(showRaidCard(none)).toBe(false);
		expect(noRaidText(none)).toBe('No RAID arrays found');
		expect(showRaidCard({ status: 'ok', arrays: [md()] })).toBe(true);
		expect(noRaidText({ status: 'ok', arrays: [md()] })).toBe('');
		expect(showRaidCard({ status: 'error', arrays: [] })).toBe(true);
		expect(noRaidText({ status: 'agent_outdated', arrays: [] })).toBe('');
		expect(showRaidCard(undefined)).toBe(false);
		expect(
			sortArrays([md({ name: 'md1' }), md({ name: 'md2', state: 'degraded' })]).map(
				(a) => a.name
			)
		).toEqual(['md2', 'md1']);
		expect(raidCheckedToast({ status: 'ok', arrays: [md(), md({ name: 'md1' })] }, 'nas')).toBe(
			'Checked 2 arrays on nas'
		);
		expect(raidCheckedToast(none, 'nas')).toBe('No RAID arrays found on nas');
	});
});
