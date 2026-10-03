import { describe, expect, it, vi } from 'vitest';
import type { ApiClient } from '$lib/api/client';
import type { BackupNode } from './model';
import { backupPlaces, backupSource, pickerEntry } from './picker';

const node = (path: string, type: BackupNode['type']) =>
	({
		name: path.slice(path.lastIndexOf('/') + 1),
		path,
		type,
		size: 3,
		mode: 0,
		uid: 0,
		gid: 0
	}) as BackupNode;

describe('backup places (#10)', () => {
	const stack = {
		kind: 'stack',
		stackName: 'shop',
		projectPath: '/stacks/shop',
		volumePaths: { shop_db: '/vol/shop_db/_data', shop_cache: '/vol/shop_cache/_data' }
	};

	it('offers the project and volumes of a stack backup, or one volume', () => {
		expect(backupPlaces(stack).map((p) => [p.label, p.path])).toEqual([
			['shop Project Files', '/stacks/shop'],
			['Volume shop_cache', '/vol/shop_cache/_data'],
			['Volume shop_db', '/vol/shop_db/_data']
		]);
		expect(backupPlaces(stack, 'shop_db').map((p) => p.label)).toEqual(['Volume shop_db']);
		expect(
			backupPlaces({ kind: 'volume', volume: 'up', paths: ['/vol/up/_data'] }).map((p) => [
				p.label,
				p.path
			])
		).toEqual([['Volume up', '/vol/up/_data']]);
	});

	it('falls back to the whole backup', () => {
		expect(backupPlaces({ kind: 'stack' }).map((p) => [p.label, p.path])).toEqual([
			['Backup Root', '/']
		]);
	});
});

describe('backup picker source (#10)', () => {
	it('maps node types to picker entries', () => {
		expect(pickerEntry(node('/a/b', 'symlink')).type).toBe('symlink');
		expect(pickerEntry(node('/a/fifo', 'fifo')).type).toBe('other');
	});

	it('lists a folder without the folder itself', async () => {
		const GET = vi.fn(async () => ({
			data: {
				path: '/v',
				entries: [node('/v', 'dir'), node('/v/a.txt', 'file')],
				truncated: true
			},
			response: new Response()
		}));
		const q = backupSource('bk1', { GET } as unknown as ApiClient).query('/v');
		const listing = await q.queryFn({ signal: new AbortController().signal });
		expect(listing.truncated).toBe(true);
		expect(listing.entries.map((e) => e.path)).toEqual(['/v/a.txt']);
		expect(GET).toHaveBeenCalledWith(
			'/api/v1/backups/{backupId}/contents',
			expect.objectContaining({
				params: { path: { backupId: 'bk1' }, query: { path: '/v', limit: 500 } }
			})
		);
	});
});
