import { describe, expect, it } from 'vitest';
import type { RegistryPullLimit } from '$lib/api/queries';
import { pullLimitView, windowLabel } from './pullLimits';

const now = new Date('2026-10-02T12:00:00Z');
const ago = (min: number) => new Date(now.getTime() - min * 60_000).toISOString();
const ahead = (min: number) => new Date(now.getTime() + min * 60_000).toISOString();

function limit(p: Partial<RegistryPullLimit>): RegistryPullLimit {
	return { host: 'docker.io', checkedAt: ago(4), limited: false, ...p };
}

describe('windowLabel', () => {
	it('names whole days, hours and minutes', () => {
		expect(windowLabel(3600)).toBe('hour');
		expect(windowLabel(21600)).toBe('6 hours');
		expect(windowLabel(86400)).toBe('day');
		expect(windowLabel(1800)).toBe('30 minutes');
		expect(windowLabel(90)).toBe('1 min 30 s');
	});
});

describe('pullLimitView', () => {
	it('is null for a credential never checked', () => {
		expect(pullLimitView(undefined, now)).toBeNull();
	});

	it('shows what is left of the limit, its window and when it was reported', () => {
		const v = pullLimitView(
			limit({ limit: 200, remaining: 187, windowSeconds: 21600, observedAt: ago(4) }),
			now
		);
		expect(v).toMatchObject({
			text: '187 of 200 left',
			sub: 'per 6 hours · checked 4 minutes ago',
			tone: 'normal'
		});
	});

	it('dates the figure by the last report, not a later answer without one', () => {
		const v = pullLimitView(
			limit({ limit: 100, remaining: 60, observedAt: ago(120), checkedAt: ago(1) }),
			now
		);
		expect(v?.sub).toBe('checked 2 hours ago');
	});

	it('warns below a tenth of the limit', () => {
		expect(pullLimitView(limit({ limit: 100, remaining: 9 }), now)?.tone).toBe('warn');
		expect(pullLimitView(limit({ limit: 100, remaining: 10 }), now)?.tone).toBe('normal');
	});

	it('shows the limit alone when the registry sends no remainder', () => {
		expect(pullLimitView(limit({ limit: 5000 }), now)?.text).toBe('5000 allowed');
	});

	it('says when a registry reports no limit', () => {
		expect(pullLimitView(limit({}), now)).toMatchObject({
			text: 'Not Reported',
			sub: 'checked 4 minutes ago',
			tone: 'muted'
		});
	});

	it('shows a reached limit with its reset', () => {
		const v = pullLimitView(
			limit({ limit: 100, remaining: 0, limited: true, limitedUntil: ahead(12) }),
			now
		);
		expect(v).toMatchObject({
			text: 'Limit Reached',
			sub: 'resets in 12 minutes',
			tone: 'warn'
		});
		expect(pullLimitView(limit({ limited: true }), now)?.sub).toBe('checked 4 minutes ago');
	});
});
