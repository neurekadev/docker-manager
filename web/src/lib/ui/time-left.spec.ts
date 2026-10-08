import { describe, expect, it } from 'vitest';
import { MIN_ELAPSED_MS, secondsLeft, timeLeftText } from './time-left';

describe('secondsLeft', () => {
	const mark = { at: 10_000, percent: 10 };

	it('estimates from the rate since the mark', () => {
		// 20 points in 10 s: 70 points left take 35 s.
		expect(secondsLeft(mark, 20_000, 30)).toBe(35);
	});

	it('grows while the job makes no progress', () => {
		expect(secondsLeft(mark, 30_000, 30)).toBe(70);
	});

	it('cannot tell without a mark, too soon, without progress or once done', () => {
		expect(secondsLeft(undefined, 20_000, 30)).toBeUndefined();
		expect(secondsLeft(mark, 20_000, undefined)).toBeUndefined();
		expect(secondsLeft(mark, mark.at + MIN_ELAPSED_MS - 1, 30)).toBeUndefined();
		expect(secondsLeft(mark, 20_000, 10)).toBeUndefined();
		expect(secondsLeft(mark, 20_000, 100)).toBeUndefined();
	});
});

describe('timeLeftText', () => {
	it('uses steps of 5 s under a minute', () => {
		expect(timeLeftText(0.4)).toBe('About 5 s left');
		expect(timeLeftText(31)).toBe('About 35 s left');
	});

	it('uses whole minutes, then hours and minutes', () => {
		expect(timeLeftText(75)).toBe('About 1 min left');
		expect(timeLeftText(150)).toBe('About 3 min left');
		expect(timeLeftText(3 * 3600 + 20 * 60)).toBe('About 3 h 20 min left');
	});
});
