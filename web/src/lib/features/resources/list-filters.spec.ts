import { afterEach, describe, expect, it, vi } from 'vitest';
import { LIST_FILTERS_PREFIX, ListFilters } from './list-filters.svelte';

function memoryStorage(initial: Record<string, string> = {}) {
	const data = new Map(Object.entries(initial));
	return {
		data,
		getItem: (k: string) => data.get(k) ?? null,
		setItem: (k: string, v: string) => void data.set(k, v),
		removeItem: (k: string) => void data.delete(k)
	};
}

afterEach(() => vi.unstubAllGlobals());

describe('list filters kept per list and browser tab', () => {
	it('restores what a list stored when the page comes back', () => {
		const storage = memoryStorage();
		const first = new ListFilters('containers', storage);
		first.q = 'web';
		first.set('status', 'running');
		expect(storage.data.has(`${LIST_FILTERS_PREFIX}containers`)).toBe(true);

		const again = new ListFilters('containers', storage);
		expect(again.q).toBe('web');
		expect(again.get('status')).toBe('running');
		expect(again.state).toEqual({ q: 'web', values: { status: 'running' } });
	});

	it('keeps each list separate', () => {
		const storage = memoryStorage();
		new ListFilters('containers', storage).set('stack', 'silo');
		const images = new ListFilters('images', storage);
		expect(images.get('stack')).toBe('');
		expect(images.q).toBe('');
	});

	it('clears the search and every filter and forgets the stored entry', () => {
		const storage = memoryStorage();
		const f = new ListFilters('volumes', storage);
		f.q = 'data';
		f.set('usage', 'unused');
		f.set('usage', '');
		expect(f.get('usage')).toBe('');
		f.clear();
		expect(f.state).toEqual({ q: '', values: {} });
		expect(storage.data.size).toBe(0);
	});

	it('starts empty from a corrupt entry', () => {
		const storage = memoryStorage({ [`${LIST_FILTERS_PREFIX}networks`]: '{not json' });
		expect(new ListFilters('networks', storage).state).toEqual({ q: '', values: {} });
	});

	it('works in memory without storage or when storage throws', () => {
		const none = new ListFilters('stacks', null);
		none.q = 'x';
		expect(none.q).toBe('x');

		const broken = {
			getItem: () => {
				throw new Error('denied');
			},
			setItem: () => {
				throw new Error('quota');
			},
			removeItem: () => {
				throw new Error('denied');
			}
		};
		const f = new ListFilters('stacks', broken);
		f.set('status', 'running');
		expect(f.get('status')).toBe('running');
		f.clear();
		expect(f.q).toBe('');
	});

	it("uses the tab's sessionStorage by default, or memory where there is none", () => {
		const storage = memoryStorage();
		vi.stubGlobal('sessionStorage', storage);
		new ListFilters('images').q = 'nginx';
		expect(new ListFilters('images').q).toBe('nginx');

		vi.stubGlobal('sessionStorage', undefined);
		const f = new ListFilters('images');
		expect(f.q).toBe('');
		f.q = 'redis';
		expect(f.q).toBe('redis');
		expect(storage.data.get(`${LIST_FILTERS_PREFIX}images`)).toContain('nginx');
	});
});
