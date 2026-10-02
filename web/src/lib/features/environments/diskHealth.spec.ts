// Disk health view models (#143): badges, issues in words, durations,
// notices, toasts and the RAID rows.
import { describe, expect, it } from 'vitest';
import { formatBytes, formatHours, formatTemperature } from '$lib/ui';
import {
	arrayName,
	attributeName,
	attributeRaw,
	attributeCheck,
	attributeType,
	canCheckDisks,
	checkedToast,
	deviceName,
	diskBadge,
	diskKey,
	diskIssues,
	diskKind,
	diskNotice,
	diskSummary,
	exceededLimit,
	healthValue,
	hotTimeCheck,
	issuesText,
	memberDisk,
	memberDiskPaths,
	memberLabel,
	memberRole,
	noDisks,
	noRaidText,
	noSmartDisks,
	poweredOn,
	raidBadge,
	raidCheckedToast,
	raidBitmap,
	raidDisks,
	raidLayout,
	raidLevel,
	raidProgress,
	selfAssessmentCheck,
	showRaidCard,
	sortArrays,
	sortDisks,
	sortMembers,
	temperatureCheck,
	valueCheck,
	wearCheck,
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
		).toBe('ZFS Pool');
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
			text: '/dev/sdc1 failed',
			failed: true
		});
		expect(memberLabel({ name: 'sdd1', slot: 3, state: 'spare' }).text).toBe('/dev/sdd1 spare');
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

describe('details (#206)', () => {
	it('names md arrays by their device path, ZFS pools by name', () => {
		expect(arrayName(md())).toBe('/dev/md0');
		expect(arrayName(md({ name: '/dev/md/data' }))).toBe('/dev/md/data');
		expect(memberLabel({ name: 'cciss!c0d0p1', slot: 0, state: 'active' }).text).toBe(
			'/dev/cciss/c0d0p1'
		);
		expect(arrayName({ kind: 'zfs', name: 'tank', state: 'healthy', members: [] })).toBe(
			'tank'
		);
	});

	it('describes the layout, bitmap and member roles', () => {
		expect(raidLayout(md({ layout: 'algorithm 2' }))).toBe('Left-symmetric (algorithm 2)');
		expect(raidLayout(md({ layout: 'algorithm 7' }))).toBe('Algorithm 7');
		expect(raidLayout(md({ layout: '2 near-copies' }))).toBe('2 near-copies');
		expect(raidLayout(md())).toBe('');
		expect(raidBitmap(md())).toBe('None');
		expect(raidBitmap(md({ bitmap: true, bitmapChunkBytes: 64 * 1024 * 1024 }))).toBe(
			'Yes, 64 MB chunks'
		);
		expect(raidBitmap(md({ bitmap: true }))).toBe('Yes');
		expect(memberRole({ name: 'sda1', slot: 0, state: 'active', writeMostly: true })).toBe(
			'Active, Write-Mostly'
		);
		expect(memberRole({ name: 'sdb1', slot: 1, state: 'replacement' })).toBe(
			'Replacing a Member'
		);
	});

	it('finds the disk a member lives on', () => {
		expect(memberDiskPaths('sda1')).toEqual(['/dev/sda']);
		expect(memberDiskPaths('sdab')).toEqual(['/dev/sdab']);
		expect(memberDiskPaths('nvme0n1p2')).toEqual(['/dev/nvme0', '/dev/nvme0n1']);
		expect(memberDiskPaths('loop0')).toEqual([]);
		const nvme = disk({ name: '/dev/nvme0', type: 'nvme', protocol: 'nvme' });
		const devices = [disk(), nvme];
		expect(memberDisk({ name: 'sda2', slot: 0, state: 'active' }, devices)).toBe(devices[0]);
		expect(memberDisk({ name: 'nvme0n1p1', slot: 1, state: 'active' }, devices)).toBe(nvme);
		expect(memberDisk({ name: 'sdc1', slot: 2, state: 'active' }, devices)).toBeUndefined();
	});

	it('shows ATA attributes as smartctl does', () => {
		const a = { id: 194, name: 'Temperature_Celsius', raw: 36 };
		expect(attributeRaw(a)).toBe('36');
		expect(attributeRaw({ ...a, rawText: '36 (Min/Max 20/49)' })).toBe('36 (Min/Max 20/49)');
		expect(attributeRaw({ id: 1, name: 'X' })).toBe('—');
		expect(attributeType({ ...a, prefailure: true })).toBe('Pre-Fail');
		expect(attributeType(a)).toBe('Old Age');
	});

	it('marks each health-relevant value OK, warning or danger, as the agent judges it (#210)', () => {
		const temp = { id: 194, name: 'Temperature_Celsius', raw: 36, threshold: 0 };
		// Information only: old age without a threshold.
		expect(attributeCheck(temp)).toBeNull();
		expect(attributeCheck({ ...temp, whenFailed: 'past' })).toEqual({
			tone: 'warn',
			label: 'Failed in the Past'
		});
		expect(attributeCheck({ id: 5, name: 'Reallocated_Sector_Ct', whenFailed: 'now' })).toEqual(
			{
				tone: 'danger',
				label: 'Failing Now'
			}
		);
		expect(attributeCheck({ id: 5, name: 'Reallocated_Sector_Ct', raw: 8 })).toEqual({
			tone: 'warn',
			label: '8 reallocated sectors'
		});
		expect(attributeCheck({ id: 197, name: 'Current_Pending_Sector', raw: 1 })?.label).toBe(
			'1 pending sector'
		);
		expect(attributeCheck({ id: 197, name: 'Current_Pending_Sector', raw: 0 })).toEqual({
			tone: 'ok',
			label: 'OK'
		});
		expect(attributeCheck({ id: 1, name: 'Raw_Read_Error_Rate', prefailure: true })?.tone).toBe(
			'ok'
		);
		expect(attributeCheck({ id: 10, name: 'Spin_Retry_Count', threshold: 97 })?.tone).toBe(
			'ok'
		);

		expect(selfAssessmentCheck(disk({ passed: true }))).toEqual({
			tone: 'ok',
			label: 'Passed'
		});
		expect(selfAssessmentCheck(disk({ passed: false }))).toEqual({
			tone: 'danger',
			label: 'Failed'
		});
		expect(selfAssessmentCheck(disk({ passed: false, criticalWarning: 2 }))?.tone).toBe('warn');
		expect(selfAssessmentCheck(disk())).toBeNull();

		const nvme = disk({ protocol: 'nvme', availableSpareThreshold: 10 });
		const check = (key: string, value: number, d = nvme) => valueCheck({ key, value }, d);
		expect(check('critical_warning', 0)?.tone).toBe('ok');
		expect(check('critical_warning', 2)).toEqual({ tone: 'warn', label: 'Too Hot' });
		expect(check('critical_warning', 4)).toEqual({ tone: 'danger', label: 'Critical' });
		expect(check('available_spare', 100)?.tone).toBe('ok');
		expect(check('available_spare', 5)).toEqual({ tone: 'warn', label: 'Below the Minimum' });
		expect(check('available_spare', 5, disk())).toBeNull();
		expect(check('percentage_used', 89)?.tone).toBe('ok');
		expect(check('percentage_used', 90)).toEqual({ tone: 'warn', label: 'Worn' });
		expect(check('media_errors', 0)?.tone).toBe('ok');
		expect(check('media_errors', 3)?.tone).toBe('warn');
		expect(check('read.total_uncorrected_errors', 1)?.tone).toBe('warn');
		expect(check('verify.total_uncorrected_errors', 0)?.tone).toBe('ok');
		// Information only.
		for (const key of ['data_units_written', 'power_cycles', 'power_on_hours', 'temperature'])
			expect(check(key, 5)).toBeNull();
	});

	it('labels and formats the other health values', () => {
		expect(healthValue({ key: 'data_units_written', value: 2 })).toEqual({
			label: 'Data Written',
			text: formatBytes(1_024_000)
		});
		expect(healthValue({ key: 'power_on_hours', value: 30 })).toEqual({
			label: 'Powered On',
			text: '1 d 6 h'
		});
		expect(healthValue({ key: 'critical_warning', value: 0 }).text).toBe('None');
		expect(healthValue({ key: 'critical_warning', value: 4 }).text).toBe('0x04');
		expect(healthValue({ key: 'controller_busy_time', value: 90 }).text).toBe('1 h 30 min');
		expect(healthValue({ key: 'temperature', value: 38 }).text).toBe(formatTemperature(38));
		expect(healthValue({ key: 'unsafe_shutdowns', value: 1234 })).toEqual({
			label: 'Unsafe Shutdowns',
			text: '1,234'
		});
		expect(healthValue({ key: 'read.total_errors_corrected', value: 5 })).toEqual({
			label: 'Read: Total Errors Corrected',
			text: '5'
		});
		expect(healthValue({ key: 'accumulated_start_stop_cycles', value: 7 }).label).toBe(
			'Accumulated Start Stop Cycles'
		);
	});
});

describe('the disk’s own limits (#212)', () => {
	it('judges the temperature against the disk’s limit, else its critical one', () => {
		expect(exceededLimit(disk({ temperatureC: 69, temperatureLimitC: 70 }))).toBeUndefined();
		expect(exceededLimit(disk({ temperatureC: 70, temperatureLimitC: 70 }))).toBe(70);
		expect(exceededLimit(disk({ temperatureC: 86, temperatureCriticalC: 85 }))).toBe(85);
		expect(exceededLimit(disk({ temperatureLimitC: 70 }))).toBeUndefined();
		expect(temperatureCheck(disk({ temperatureC: 41, temperatureLimitC: 70 }))).toEqual({
			tone: 'ok',
			label: `${formatTemperature(41)} (limit ${formatTemperature(70)})`
		});
		expect(temperatureCheck(disk({ temperatureC: 72, temperatureLimitC: 70 }))?.tone).toBe(
			'warn'
		);
		// No limit: no mark.
		expect(temperatureCheck(disk({ temperatureC: 41 }))).toBeNull();
	});

	it('names a hot disk and its time above the limits in its issues', () => {
		expect(
			diskIssues(disk({ state: 'warning', temperatureC: 72, temperatureLimitC: 70 }))
		).toEqual([`Too hot: ${formatTemperature(72)} (limit ${formatTemperature(70)})`]);
		// The NVMe temperature warning and the limit: one issue.
		expect(
			diskIssues(
				disk({
					state: 'warning',
					passed: false,
					criticalWarning: 2,
					temperatureC: 83,
					temperatureLimitC: 82
				})
			)
		).toHaveLength(1);
		expect(
			diskIssues(
				disk({
					state: 'warning',
					overTemperatureMinutes: 34,
					criticalTemperatureMinutes: 1
				})
			)
		).toEqual([
			'Ran above its temperature limit for 34 min',
			'Ran above its critical temperature for 1 min'
		]);
		expect(diskIssues(disk({ overTemperatureMinutes: 0 }))).toEqual([]);
	});

	it('marks wear and the time above the limits', () => {
		expect(wearCheck(disk({ percentageUsed: 7 }))).toEqual({ tone: 'ok', label: '7% used' });
		expect(wearCheck(disk({ percentageUsed: 93 }))?.tone).toBe('warn');
		expect(wearCheck(disk())).toBeNull();
		expect(hotTimeCheck(disk())).toBeNull();
		expect(hotTimeCheck(disk({ overTemperatureMinutes: 0 }))).toEqual({
			tone: 'ok',
			label: 'Never'
		});
		expect(
			hotTimeCheck(disk({ overTemperatureMinutes: 90, criticalTemperatureMinutes: 2 }))
		).toEqual({ tone: 'warn', label: '1 h 30 min, 2 min above critical' });
		const nvme = disk({ protocol: 'nvme', temperatureLimitC: 82 });
		expect(valueCheck({ key: 'temperature', value: 38 }, nvme)).toEqual({
			tone: 'ok',
			label: 'OK'
		});
		expect(valueCheck({ key: 'temperature', value: 84 }, nvme)).toEqual({
			tone: 'warn',
			label: 'Too Hot'
		});
		expect(valueCheck({ key: 'warning_temp_time', value: 0 }, nvme)?.tone).toBe('ok');
		expect(valueCheck({ key: 'critical_comp_time', value: 3 }, nvme)).toEqual({
			tone: 'warn',
			label: 'Ran Hot'
		});
	});
});
