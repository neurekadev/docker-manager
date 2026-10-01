import { describe, expect, it } from 'vitest';
import { RESOURCE_ICONS } from '$lib/features/common/resourceIcons';
import {
	NOTIFICATION_KINDS,
	NOTIFICATION_OUTCOMES,
	blockFields,
	dayKey,
	dayLabel,
	groupByDay,
	inlineFields,
	notificationCount,
	notificationHref,
	notificationKindLabel,
	notificationTile,
	notificationsSummary,
	outcomeLabel,
	outcomeTone
} from './model';
import { failedUpdate, nightlyBackup, sampleNotification, sep, weeklyPrune } from './test/samples';

// Local times, so the days do not depend on the machine's time zone.
const now = new Date(2026, 8, 30, 12, 0);

describe('notifications in words', () => {
	it('names the kinds and outcomes as the badges and filters do', () => {
		expect(NOTIFICATION_KINDS.map((k) => k.label)).toEqual([
			'Backups and restores',
			'Prune',
			'Image updates'
		]);
		expect(notificationKindLabel('prune')).toBe('Prune');
		expect(notificationKindLabel('something_new')).toBe('something_new');
		expect(NOTIFICATION_OUTCOMES.map((o) => o.label)).toEqual(['Done', 'Warning', 'Failed']);
		expect(['success', 'warning', 'failure'].map(outcomeTone)).toEqual([
			'ok',
			'warn',
			'danger'
		]);
		expect(outcomeLabel('failure')).toBe('Failed');
	});

	it('shows each kind with the tile of the policy that runs it', () => {
		expect(notificationTile('backup')).toEqual(RESOURCE_ICONS.backup);
		expect(notificationTile('prune')).toEqual(RESOURCE_ICONS.maintenancePolicy);
		expect(notificationTile('updates')).toEqual(RESOURCE_ICONS.updatePolicy);
		expect(notificationTile('other')).toEqual(RESOURCE_ICONS.job);
	});

	it('links to the run inside the app, never elsewhere', () => {
		expect(notificationHref(nightlyBackup)).toBe('/jobs/job-n1');
		expect(notificationHref({ link: 'https://evil.example/x', jobId: 'j9' })).toBe('/jobs/j9');
		expect(notificationHref({ link: '//evil.example/x', jobId: 'j9' })).toBe('/jobs/j9');
		expect(notificationHref({ link: '', jobId: 'j9' })).toBe('/jobs/j9');
	});

	it('splits the labelled values into short ones and lines, without the environment', () => {
		expect(inlineFields(weeklyPrune).map((f) => f.name)).toEqual([
			'Reclaimed',
			'Containers',
			'Images',
			'Volumes',
			'Networks',
			'Build cache'
		]);
		expect(blockFields(weeklyPrune)).toEqual([]);
		expect(inlineFields(failedUpdate).map((f) => f.name)).toEqual(['Target']);
		expect(blockFields(failedUpdate).map((f) => f.name)).toEqual([
			'Updated',
			'Failed',
			'What to do'
		]);
	});

	it('names days as Today, Yesterday or the date in the local time zone', () => {
		expect(dayLabel(new Date(2026, 8, 30, 0, 1), now)).toBe('Today');
		expect(dayLabel(new Date(2026, 8, 29, 23, 59), now)).toBe('Yesterday');
		expect(dayLabel(new Date(2026, 8, 27, 4), now)).toBe('Sun, Sep 27');
		// Another year carries the year.
		expect(dayLabel(new Date(2025, 11, 31, 22), now)).toBe('Wed, Dec 31, 2025');
		// Yesterday across a month and a year.
		expect(dayLabel(new Date(2026, 8, 30, 8), new Date(2026, 9, 1, 8))).toBe('Yesterday');
		expect(dayLabel(new Date(2025, 11, 31, 8), new Date(2026, 0, 1, 8))).toBe('Yesterday');
		expect(dayKey(new Date(2026, 0, 5, 1))).toBe('2026-01-05');
	});

	it('groups notifications by day, newest first, in the order they come', () => {
		const late = sampleNotification({ id: 'n4', createdAt: sep(30, 11) });
		const groups = groupByDay([late, nightlyBackup, weeklyPrune, failedUpdate], now);
		expect(groups.map((g) => [g.label, g.items.map((n) => n.id)])).toEqual([
			['Today', ['n4', 'n1']],
			['Yesterday', ['n2']],
			['Sun, Sep 27', ['n3']]
		]);
		expect(groups.map((g) => g.key)).toEqual(['2026-09-30', '2026-09-29', '2026-09-27']);
		expect(groupByDay([], now)).toEqual([]);
		// A time that is not a time is left out.
		expect(groupByDay([sampleNotification({ id: 'x', createdAt: 'never' })], now)).toEqual([]);
	});

	it('counts what is shown', () => {
		expect(notificationCount(1)).toBe('1 notification');
		expect(notificationsSummary({ shown: 3, loaded: 3, more: false })).toBe('3 notifications');
		expect(notificationsSummary({ shown: 50, loaded: 50, total: 120, more: true })).toBe(
			'120 notifications'
		);
		expect(notificationsSummary({ shown: 50, loaded: 50, more: true })).toBe(
			'50 notifications loaded'
		);
		expect(notificationsSummary({ shown: 2, loaded: 5, more: false })).toBe(
			'2 of 5 notifications'
		);
		expect(notificationsSummary({ shown: 2, loaded: 50, more: true })).toBe(
			'2 of 50 notifications loaded'
		);
	});
});
