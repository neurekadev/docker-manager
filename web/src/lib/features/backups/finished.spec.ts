// A job leaving the running list reports its outcome once.
import { describe, expect, it } from 'vitest';
import { leftList } from './finished.svelte';

describe('leftList', () => {
	it('names the jobs that left the running list', () => {
		expect(leftList(['a', 'b', 'c'], ['b', 'd'])).toEqual(['a', 'c']);
		expect(leftList([], ['a'])).toEqual([]);
		expect(leftList(['a'], ['a'])).toEqual([]);
	});
});
