import { describe, expect, it } from 'vitest';
import { buildCron, describeCron, parseCronPreset } from './cron';

// The viewer's zone is passed explicitly so the results do not depend on
// the machine running the tests.
const here = 'Europe/Berlin';
const say = (expr: string, zone = here) => describeCron(expr, zone, here);

describe('describeCron', () => {
	it('reads intervals', () => {
		expect(say('* * * * *')).toBe('Every minute');
		expect(say('*/1 * * * *')).toBe('Every minute');
		expect(say('*/15 * * * *')).toBe('Every 15 minutes');
		expect(say('0 * * * *')).toBe('Every hour');
		expect(say('5 * * * *')).toBe('Hourly at :05');
		expect(say('0 */6 * * *')).toBe('Every 6 hours');
		expect(say('30 */2 * * *')).toBe('Every 2 hours at :30');
		expect(say('@hourly')).toBe('Every hour');
	});

	it('reads daily, weekday, weekly and monthly schedules', () => {
		expect(say('0 3 * * *')).toBe('Daily at 03:00');
		expect(say('@daily')).toBe('Daily at 00:00');
		expect(say('0 3,15 * * *')).toBe('Daily at 03:00 and 15:00');
		expect(say('30 7 * * 1-5')).toBe('Weekdays at 07:30');
		expect(say('30 7 * * MON-FRI')).toBe('Weekdays at 07:30');
		expect(say('0 9 * * 0,6')).toBe('Weekends at 09:00');
		expect(say('0 3 * * 0')).toBe('Weekly on Sunday at 03:00');
		expect(say('0 3 * * 7')).toBe('Weekly on Sunday at 03:00');
		expect(say('0 4 * * 1,4')).toBe('Weekly on Monday and Thursday at 04:00');
		expect(say('0 4 * * 0,1,3,5')).toBe(
			'Weekly on Monday, Wednesday, Friday and Sunday at 04:00'
		);
		expect(say('0 4 * * 0-6')).toBe('Daily at 04:00');
		expect(say('0 2 1 * *')).toBe('Monthly on day 1 at 02:00');
		expect(say('0 2 1,15 * *')).toBe('Monthly on days 1 and 15 at 02:00');
		expect(say('@weekly')).toBe('Weekly on Sunday at 00:00');
		expect(say('@monthly')).toBe('Monthly on day 1 at 00:00');
	});

	it('falls back to the expression for other shapes', () => {
		expect(say('0 3 1 1 *')).toBe('0 3 1 1 *');
		expect(say('0 3 1 * 1')).toBe('0 3 1 * 1');
		expect(say('*/5 3 * * *')).toBe('*/5 3 * * *');
		expect(say('0 25 * * *')).toBe('0 25 * * *');
		expect(say('0 0 3 * * *')).toBe('0 0 3 * * *');
		expect(say('  not cron ')).toBe('not cron');
		expect(say('')).toBe('');
	});

	it('names the zone only when it is not the viewer’s', () => {
		expect(say('0 3 * * *', 'UTC')).toBe('Daily at 03:00 (UTC)');
		expect(say('0 3 * * *', 'Etc/UTC')).toBe('Daily at 03:00 (UTC)');
		expect(say('0 3 * * *', 'America/New_York')).toBe('Daily at 03:00 (America/New York)');
		expect(say('0 3 * * *', 'Europe/Berlin')).toBe('Daily at 03:00');
		expect(describeCron('0 3 * * *', 'UTC', 'Etc/UTC')).toBe('Daily at 03:00');
		expect(describeCron('0 3 * * *')).toBe('Daily at 03:00');
		// Short intervals read the same everywhere.
		expect(say('*/15 * * * *', 'UTC')).toBe('Every 15 minutes');
		expect(say('0 * * * *', 'UTC')).toBe('Every hour');
		expect(say('5 * * * *', 'Asia/Kolkata')).toBe('Hourly at :05 (Asia/Kolkata)');
		// A raw expression gets no zone.
		expect(say('0 3 1 1 *', 'UTC')).toBe('0 3 1 1 *');
	});
});

describe('cron presets', () => {
	it('recognises the shapes CronField edits with fields', () => {
		expect(parseCronPreset('15 * * * *')).toEqual({
			kind: 'hourly',
			minute: 15,
			hour: 3,
			day: 1
		});
		expect(parseCronPreset('0 3 * * *')).toMatchObject({ kind: 'daily', minute: 0, hour: 3 });
		expect(parseCronPreset('30 4 * * 6')).toMatchObject({
			kind: 'weekly',
			minute: 30,
			hour: 4,
			day: 6
		});
		expect(parseCronPreset('30 4 * * 7')).toMatchObject({ kind: 'weekly', day: 0 });
		expect(parseCronPreset('0 4 * * mon')).toMatchObject({ kind: 'weekly', day: 1 });
		for (const custom of ['*/15 * * * *', '0 3 1 * *', '0 3 * * 1-5', '0 3,15 * * *', 'x'])
			expect(parseCronPreset(custom).kind, custom).toBe('custom');
	});

	it('builds expressions from presets and keeps custom text', () => {
		expect(buildCron({ kind: 'hourly', minute: 5, hour: 9, day: 2 })).toBe('5 * * * *');
		expect(buildCron({ kind: 'daily', minute: 0, hour: 3, day: 2 })).toBe('0 3 * * *');
		expect(buildCron({ kind: 'weekly', minute: 30, hour: 23, day: 0 })).toBe('30 23 * * 0');
		expect(buildCron({ kind: 'daily', minute: 75, hour: -1, day: 0 })).toBe('59 0 * * *');
		expect(buildCron({ kind: 'custom', minute: 0, hour: 0, day: 0 }, '*/5 * * * *')).toBe(
			'*/5 * * * *'
		);
		for (const e of ['7 * * * *', '0 3 * * *', '45 22 * * 5'])
			expect(buildCron(parseCronPreset(e))).toBe(e);
	});
});
