import { describe, expect, it } from 'vitest';
import ChartPie from '@lucide/svelte/icons/chart-pie';
import MemoryStick from '@lucide/svelte/icons/memory-stick';
import Thermometer from '@lucide/svelte/icons/thermometer';
import { RESOURCE_ICONS } from '$lib/features/common/resourceIcons';
import { EVENT_KINDS } from '$lib/features/notifications/model';
import {
	ALERT_KINDS,
	alertFacts,
	alertHref,
	alertView,
	alertsByArray,
	alertsByDisk,
	arrayAlertKey,
	canDismiss,
	diskAlertKey,
	dismissedText,
	healthAlerts,
	healthNotice,
	healthSubject,
	healthTallies,
	isActive,
	kindIcon,
	kindLabel,
	resolutionLabel,
	severityLabel,
	severityTone,
	sortAlerts,
	worstSeverity
} from './model';
import { degradedArray, failingDisk, offlineEdge, sampleAlert } from './test/samples';

describe('alerts in words (#159)', () => {
	it('names the kinds in the notification channels’ order', () => {
		// Restores and prunes are notifications, never alerts.
		expect(ALERT_KINDS.map((k) => k.kind)).toEqual(
			EVENT_KINDS.map((k) => k.kind).filter((k) => !['restore', 'prune'].includes(k))
		);
		expect(ALERT_KINDS.map((k) => k.label)).toEqual([
			'Disk Health',
			'RAID',
			'Temperature',
			'Disk Space',
			'Memory',
			'Environment Offline',
			'Backups Paused',
			'Updates Available',
			'Failed Job'
		]);
		expect(kindLabel('job_failed')).toBe('Failed Job');
		expect(kindLabel('updates')).toBe('Updates Available');
		expect(kindLabel('something_new')).toBe('something_new');
		expect(kindIcon('temperature')).toBe(Thermometer);
		// Disks and RAID arrays show the tiles of their tables on the System tab.
		expect(kindIcon('disk_health')).toBe(RESOURCE_ICONS.disk.icon);
		expect(kindIcon('raid')).toBe(RESOURCE_ICONS.raidArray.icon);
		expect(kindIcon('disk_space')).toBe(ChartPie);
		expect(kindIcon('memory')).toBe(MemoryStick);
		expect(kindIcon('something_new')).toBeUndefined();
	});

	it('shows the short fields an alert names, not its environment or severity again', () => {
		expect(
			alertFacts({
				fields: [
					{ name: 'Environment', value: 'homelab', inline: true },
					{ name: 'Severity', value: 'Critical', inline: true },
					{ name: 'Sensor', value: 'coretemp Package id 0', inline: true },
					{ name: 'Highest', value: '92 °C', inline: true },
					{ name: 'Thresholds', value: 'Warning at 80 °C, critical at 90 °C' }
				]
			})
		).toEqual([
			{ name: 'Sensor', value: 'coretemp Package id 0', inline: true },
			{ name: 'Highest', value: '92 °C', inline: true }
		]);
		expect(alertFacts({ fields: [] })).toEqual([]);
	});

	it('reads severities as the badge vocabulary, critical first', () => {
		expect(['critical', 'warning', 'info'].map(severityLabel)).toEqual([
			'Critical',
			'Warning',
			'Info'
		]);
		expect(['critical', 'warning', 'info'].map(severityTone)).toEqual([
			'danger',
			'warn',
			'info'
		]);
		expect(worstSeverity([degradedArray, failingDisk])).toBe('critical');
		expect(worstSeverity([degradedArray])).toBe('warning');
		expect(worstSeverity([])).toBeUndefined();
		const info = sampleAlert({ id: 'i', severity: 'info', startedAt: '2026-09-29T00:00:00Z' });
		expect(sortAlerts([info, degradedArray, failingDisk]).map((a) => a.id)).toEqual([
			'a1',
			'a2',
			'i'
		]);
		// Same severity: the newest first.
		expect(sortAlerts([degradedArray, offlineEdge]).map((a) => a.id)).toEqual(['a3', 'a2']);
	});

	it('knows the state in the list and who may dismiss', () => {
		expect(alertView(failingDisk)).toBe('active');
		expect(alertView({ ...failingDisk, dismissed: true })).toBe('dismissed');
		expect(alertView({ ...failingDisk, state: 'resolved' })).toBe('resolved');
		expect(isActive(failingDisk)).toBe(true);
		expect(canDismiss(failingDisk)).toBe(true);
		expect(canDismiss(degradedArray)).toBe(false); // no alert.dismiss
		expect(canDismiss({ ...failingDisk, dismissed: true })).toBe(false);
		expect(canDismiss({ ...failingDisk, state: 'resolved' })).toBe(false);
	});

	it('links to the page the alert is about, never elsewhere', () => {
		expect(alertHref(failingDisk)).toBe('/environments/e1?tab=system');
		expect(alertHref({ link: '/jobs/j1' })).toBe('/jobs/j1');
		expect(alertHref({ link: 'https://evil.example/x' })).toBe('/notifications?tab=alerts');
		expect(alertHref({ link: '//evil.example/x' })).toBe('/notifications?tab=alerts');
		expect(alertHref({ link: '' })).toBe('/notifications?tab=alerts');
	});

	it('says how an alert ended and who dismissed it', () => {
		expect(resolutionLabel({})).toBe('Resolved');
		expect(resolutionLabel({ resolution: 'removed' })).toBe('Removed');
		expect(resolutionLabel({ resolution: 'expired' })).toBe('Expired');
		expect(resolutionLabel({ resolution: 'archived' })).toBe('Environment Archived');
		const now = new Date('2026-09-30T12:00:00Z');
		expect(
			dismissedText(
				{ dismissedAt: '2026-09-30T09:00:00Z', dismissedBy: { id: 'u1', name: 'Alex' } },
				now
			)
		).toBe('Dismissed by Alex 3 hours ago');
		expect(dismissedText({ dismissedAt: '2026-09-30T09:00:00Z' }, now)).toBe(
			'Dismissed 3 hours ago'
		);
	});
});

describe('disks and RAID arrays with alerts (#159 System tab)', () => {
	const zfs = sampleAlert({
		id: 'z1',
		kind: 'raid',
		resourceType: 'zfs_pool',
		resourceId: 'tank',
		severity: 'critical',
		title: 'ZFS pool tank on homelab is faulted',
		facts: { pool: 'tank', arrayKind: 'zfs', health: 'FAULTED' },
		dismissed: true
	});

	it('keeps the firing disk and RAID alerts of one environment', () => {
		const resolved = { ...failingDisk, id: 'r', state: 'resolved' as const };
		expect(
			healthAlerts([failingDisk, degradedArray, offlineEdge, resolved, zfs], 'e1').map(
				(a) => a.id
			)
		).toEqual(['a1', 'a2', 'z1']);
	});

	it('finds the alert of a disk by path and type and of an array by kind and name', () => {
		const disks = alertsByDisk([failingDisk, degradedArray]);
		expect(disks.get(diskAlertKey('/dev/sda', 'sat'))?.id).toBe('a1');
		expect(disks.get(diskAlertKey('/dev/sda', 'nvme'))).toBeUndefined();
		const arrays = alertsByArray([failingDisk, degradedArray, zfs]);
		expect(arrays.get(arrayAlertKey('md', 'md0'))?.id).toBe('a2');
		expect(arrays.get(arrayAlertKey('zfs', 'tank'))?.id).toBe('z1');
		// Two alerts of one disk: the worse one marks it.
		const warn = { ...failingDisk, id: 'w', severity: 'warning' as const };
		expect(alertsByDisk([warn, failingDisk]).get('/dev/sda|sat')?.id).toBe('a1');
	});

	it('keeps monitoring alerts off the disk and array rows and names them', () => {
		const smart = sampleAlert({
			id: 'm1',
			resourceType: 'environment',
			resourceId: 'e1',
			severity: 'warning',
			title: 'Disks on homelab can’t be scanned',
			facts: { monitoring: 'smart', reason: 'scan_failed' }
		});
		const raid = sampleAlert({
			id: 'm2',
			kind: 'raid',
			resourceType: 'environment',
			resourceId: 'e1',
			severity: 'warning',
			facts: { monitoring: 'raid', reason: 'raid_read' }
		});
		expect(alertsByDisk([smart]).size).toBe(0);
		expect(alertsByArray([raid]).size).toBe(0);
		expect(healthSubject(smart)).toBe('disk health');
		expect(healthSubject(raid)).toBe('RAID state');
		expect(healthNotice([smart, failingDisk])?.body).toBe('/dev/sda and disk health.');
	});

	it('states one alert by its title and several by count and names', () => {
		expect(healthNotice([])).toBeNull();
		expect(healthNotice([failingDisk])).toEqual({
			tone: 'danger',
			title: 'Disk /dev/sda on homelab is failing',
			body: 'SMART self-assessment failed, 8 reallocated sectors.'
		});
		expect(healthNotice([degradedArray, failingDisk])).toEqual({
			tone: 'danger',
			title: '2 disk and RAID alerts',
			body: '/dev/sda and md0.'
		});
		const disk = (id: string, device: string) =>
			sampleAlert({ id, facts: { device, deviceType: 'sat' } });
		expect(
			healthNotice([disk('1', '/dev/sda'), disk('2', '/dev/sdb'), disk('3', '/dev/sdc')])
		).toEqual({
			tone: 'warn',
			title: '3 disk alerts',
			body: '/dev/sda, /dev/sdb and /dev/sdc.'
		});
		expect(
			healthNotice([
				disk('1', '/dev/sda'),
				disk('2', '/dev/sdb'),
				disk('3', '/dev/sdc'),
				disk('4', '/dev/sdd')
			])?.body
		).toBe('/dev/sda, /dev/sdb and 2 more.');
		expect(healthNotice([degradedArray, { ...degradedArray, id: 'x' }])?.title).toBe(
			'2 RAID alerts'
		);
	});

	it('counts the active disk and RAID alerts for the dashboard', () => {
		expect(healthTallies([failingDisk, degradedArray, offlineEdge, zfs])).toEqual({
			disks: { count: 1, critical: true },
			raid: { count: 1, critical: false } // the faulted pool is dismissed
		});
		expect(healthTallies([])).toEqual({
			disks: { count: 0, critical: false },
			raid: { count: 0, critical: false }
		});
	});
});
