import { describe, expect, it } from 'vitest';
import {
	foldersBetween,
	normalize,
	parentOf,
	pickerCrumbs,
	pickerEntries,
	pickerStart,
	placeOf,
	selectedIn,
	selectionText,
	tickState,
	toggle,
	within,
	type PickerEntry
} from './filePicker';

const tree: Record<string, string[]> = {
	'/v': ['/v/a.jpg', '/v/thumbs', '/v/docs'],
	'/v/thumbs': ['/v/thumbs/1.png', '/v/thumbs/2.png', '/v/thumbs/sub'],
	'/v/thumbs/sub': ['/v/thumbs/sub/x.png']
};
const children = (d: string) => tree[d];

describe('file picker selection', () => {
	it('knows paths inside folders', () => {
		expect(within('/v/thumbs/1.png', '/v/thumbs')).toBe(true);
		expect(within('/v/thumbs', '/v/thumbs')).toBe(true);
		expect(within('/v/thumbs2', '/v/thumbs')).toBe(false);
		expect(within('/anything', '/')).toBe(true);
		expect(parentOf('/v/thumbs/1.png')).toBe('/v/thumbs');
		expect(parentOf('/v')).toBe('/');
	});

	it('keeps no path inside a selected folder', () => {
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

	it('ticks a folder whole, replacing what was ticked inside it', () => {
		let sel = toggle([], '/v/thumbs/1.png', children);
		expect(sel).toEqual(['/v/thumbs/1.png']);
		sel = toggle(sel, '/v/thumbs', children);
		expect(sel).toEqual(['/v/thumbs']);
		sel = toggle(sel, '/v/thumbs', children);
		expect(sel).toEqual([]);
	});

	it('splits a ticked folder when something inside is unticked', () => {
		const sel = toggle(['/v'], '/v/thumbs/sub/x.png', children);
		expect(sel).toEqual(['/v/a.jpg', '/v/docs', '/v/thumbs/1.png', '/v/thumbs/2.png']);
		expect(tickState(sel, '/v/thumbs')).toBe('mixed');
		expect(tickState(sel, '/v/thumbs/sub')).toBe('unchecked');
	});

	it('ignores a listing that names its own folder', () => {
		const withSelf = (dir: string) => {
			const entries = children(dir);
			return entries && [dir, ...entries];
		};
		expect(toggle(['/v'], '/v/thumbs/sub/x.png', withSelf)).toEqual([
			'/v/a.jpg',
			'/v/docs',
			'/v/thumbs/1.png',
			'/v/thumbs/2.png'
		]);
	});

	it('leaves the selection alone when a listing is not loaded', () => {
		expect(toggle(['/v'], '/v/docs/readme.md', children)).toEqual(['/v']);
	});

	it('names the folders a split needs listed', () => {
		expect(foldersBetween('/v', '/v/thumbs/sub/x.png')).toEqual([
			'/v',
			'/v/thumbs',
			'/v/thumbs/sub'
		]);
		expect(foldersBetween('/v', '/v/a.jpg')).toEqual(['/v']);
		expect(foldersBetween('/', '/a/b')).toEqual(['/', '/a']);
		expect(foldersBetween('/v', '/v')).toEqual([]);
	});

	it('counts and words the selection', () => {
		expect(selectedIn(['/v/a.jpg', '/v/thumbs', '/w/x'], '/v')).toBe(2);
		expect(selectionText(0)).toBe('Nothing selected');
		expect(selectionText(1)).toBe('1 item selected');
		expect(selectionText(3)).toBe('3 items selected');
	});
});

describe('file picker browsing', () => {
	const places = [
		{ path: '/stacks/web', label: 'web Project Files' },
		{ path: '/vol/db/_data', label: 'Volume db' }
	];

	it('finds the deepest place holding a path', () => {
		const nested = [{ path: '/' }, { path: '/vol/a' }];
		expect(placeOf(nested, '/vol/a/x')?.path).toBe('/vol/a');
		expect(placeOf(nested, '/etc/x')?.path).toBe('/');
		expect(placeOf(nested.slice(1), '/vol/ab')).toBeUndefined();
	});

	it("opens on the first chosen path's folder, else the first place", () => {
		expect(pickerStart(places, [' /vol/db/_data/pg/conf '])).toBe('/vol/db/_data/pg');
		expect(pickerStart(places, ['', '/elsewhere/x', '/stacks/web/a.env'])).toBe('/stacks/web');
		expect(pickerStart(places, ['/vol/db/_data'])).toBe('/stacks/web');
		expect(pickerStart(places, [])).toBe('/stacks/web');
		expect(pickerStart([], [])).toBe('/');
	});

	it('starts the crumbs at the place and never above it', () => {
		const place = places[1];
		expect(pickerCrumbs(place, '/vol/db/_data')).toEqual([
			{ name: 'Volume db', path: '/vol/db/_data' }
		]);
		expect(pickerCrumbs(place, '/vol/db/_data/x/y')).toEqual([
			{ name: 'Volume db', path: '/vol/db/_data' },
			{ name: 'x', path: '/vol/db/_data/x' },
			{ name: 'y', path: '/vol/db/_data/x/y' }
		]);
		expect(
			pickerCrumbs({ path: '/', label: 'Backup Root' }, '/srv').map((c) => c.path)
		).toEqual(['/', '/srv']);
	});

	it('lists folders first, by name, and filters in any case', () => {
		const e = (name: string, type: PickerEntry['type']): PickerEntry => ({
			name,
			path: `/d/${name}`,
			type
		});
		const entries = [e('b.txt', 'file'), e('Zed', 'dir'), e('a.txt', 'file')];
		expect(pickerEntries(entries).map((x) => x.name)).toEqual(['Zed', 'a.txt', 'b.txt']);
		expect(pickerEntries(entries, ' A.T ').map((x) => x.name)).toEqual(['a.txt']);
	});
});
