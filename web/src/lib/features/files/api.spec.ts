import { describe, expect, it } from 'vitest';
import type { ApiClient } from '$lib/api/client';
import { FilesApi, filesBase, filesCapability, liveScopeOf, scopeKey, type FileScope } from './api';

const stack: FileScope = { kind: 'stack', stackId: 's1', environmentId: 'e1' };
const volume: FileScope = { kind: 'volume', environmentId: 'e1', volume: 'data' };
const template: FileScope = { kind: 'template', templateId: 't1' };

describe('file scopes', () => {
	it('names every root', () => {
		expect(liveScopeOf(stack)).toEqual({ kind: 'stack', id: 's1' });
		expect(liveScopeOf(volume)).toEqual({ kind: 'volume', id: 'e1/data' });
		expect(liveScopeOf(template)).toEqual({ kind: 'template', id: 't1' });
		expect([stack, volume, template].map(scopeKey)).toEqual([
			'stack:s1',
			'volume:e1/data',
			'template:t1'
		]);
	});

	it('builds the routes and capabilities of each root', () => {
		expect(filesBase(template)).toBe('/api/v1/templates/t1/files');
		expect(filesBase(volume)).toBe('/api/v1/environments/e1/volumes/data/files');
		expect(filesCapability(template, 'write')).toBe('template.files.write');
		expect(new FilesApi(template).uploadUrl('.', 'a b.txt')).toBe(
			'/api/v1/templates/t1/files/uploads?path=.&name=a+b.txt'
		);
	});

	it('calls the template routes for a template scope', async () => {
		const calls: { path: string; params: unknown }[] = [];
		const client = {
			GET: (path: string, init: { params: unknown }) => {
				calls.push({ path, params: init.params });
				return Promise.resolve({
					data: { dir: {}, items: [], total: 0, truncated: false },
					response: new Response(null, { status: 200 })
				});
			}
		} as unknown as ApiClient;
		await new FilesApi(template, client).list('.', { sort: 'name', q: '', hidden: false });
		expect(calls).toHaveLength(1);
		expect(calls[0].path).toBe('/api/v1/templates/{templateId}/files');
		expect(calls[0].params).toMatchObject({ path: { templateId: 't1' }, query: { path: '.' } });
	});
});
