import { describe, expect, it } from 'vitest';
import { normalize, parentOf, pickerRoots, tickState, toggle, within } from './selection';

const tree: Record<string, string[]> = {
	'/v': ['/v/a.jpg', '/v/thumbs', '/v/docs'],
	'/v/thumbs': ['/v/thumbs/1.png', '/v/thumbs/2.png', '/v/thumbs/sub'],
	'/v/thumbs/sub': ['/v/thumbs/sub/x.png']
};
const children = (d: string) => tree[d];

describe('backup path selection (#10)', () => {
	it('knows paths inside directories', () => {
		expect(within('/v/thumbs/1.png', '/v/thumbs')).toBe(true);
		expect(within('/v/thumbs', '/v/thumbs')).toBe(true);
		expect(within('/v/thumbs2', '/v/thumbs')).toBe(false);
		expect(within('/anything', '/')).toBe(true);
		expect(parentOf('/v/thumbs/1.png')).toBe('/v/thumbs');
		expect(parentOf('/v')).toBe('/');
	});

	it('keeps no path inside a selected directory', () => {
		expect(normalize(['/v/thumbs/1.png', '/v/thumbs', '/v/a.jpg', '/v/a.jpg'])).toEqual([
			'/v/a.jpg',
			'/v/thumbs'
		]);
	});

	it('shows checked, mixed and unchecked', () => {
		const sel = ['/v/thumbs/sub'];
		expect(tickState(sel, '/v/thumbs/sub/x.png')).toBe('checked');
		expect(tickState(sel, '/v/thumbs')).toBe('mixed');
		expect(tickState(sel, '/v')).toBe('mixed');
		expect(tickState(sel, '/v/a.jpg')).toBe('unchecked');
	});

	it('ticks a directory whole, replacing what was ticked inside it', () => {
		let sel = toggle([], '/v/thumbs/1.png', children);
		expect(sel).toEqual(['/v/thumbs/1.png']);
		sel = toggle(sel, '/v/thumbs', children);
		expect(sel).toEqual(['/v/thumbs']);
		sel = toggle(sel, '/v/thumbs', children);
		expect(sel).toEqual([]);
	});

	it('splits a ticked directory when something inside is unticked', () => {
		const sel = toggle(['/v'], '/v/thumbs/sub/x.png', children);
		expect(sel).toEqual(['/v/a.jpg', '/v/docs', '/v/thumbs/1.png', '/v/thumbs/2.png']);
		expect(tickState(sel, '/v/thumbs')).toBe('mixed');
		expect(tickState(sel, '/v/thumbs/sub')).toBe('unchecked');
	});

	it('leaves the selection alone when a listing is not loaded', () => {
		expect(toggle(['/v'], '/v/docs/readme.md', children)).toEqual(['/v']);
	});

	it('offers the project and volumes of a stack backup, or one volume', () => {
		const b = {
			kind: 'stack',
			stackName: 'shop',
			projectPath: '/stacks/shop',
			volumePaths: { shop_db: '/vol/shop_db/_data', shop_cache: '/vol/shop_cache/_data' }
		};
		expect(pickerRoots(b).map((r) => r.label)).toEqual([
			'shop project files',
			'Volume shop_cache',
			'Volume shop_db'
		]);
		expect(pickerRoots(b, 'shop_db')).toEqual([
			{ path: '/vol/shop_db/_data', label: 'Volume shop_db', kind: 'volume' }
		]);
		expect(pickerRoots({ kind: 'volume', volume: 'up', paths: ['/vol/up/_data'] })).toEqual([
			{ path: '/vol/up/_data', label: 'Volume up', kind: 'volume' }
		]);
	});
});
