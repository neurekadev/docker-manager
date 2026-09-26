import { describe, expect, it } from 'vitest';
import { countText, coverageCount, setExcluded } from './coverage';

describe('policy coverage', () => {
	it('adds and removes exclusions without duplicates', () => {
		expect(setExcluded(['a'], ['a', 'b'], true)).toEqual(['a', 'b']);
		expect(setExcluded(['a', 'b', 'c'], ['b', 'x'], false)).toEqual(['a', 'c']);
	});

	it('counts what is covered', () => {
		const items = [{ key: 'a' }, { key: 'b' }, { key: 'c' }];
		expect(coverageCount(items, ['b', 'gone'])).toEqual({ included: 2, total: 3 });
	});

	it('names exclusion counts', () => {
		expect(countText([[0, 'stack', 'stacks']], 'Nothing')).toBe('Nothing');
		expect(
			countText([
				[2, 'stack', 'stacks'],
				[1, 'volume', 'volumes']
			])
		).toBe('2 stacks and 1 volume');
	});
});
