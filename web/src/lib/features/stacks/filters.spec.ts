import { describe, expect, it } from 'vitest';
import { applyListFilters } from '$lib/features/resources/filters';
import { stackFilters, stackSearch } from './filters';
import type { Stack } from './queries';

const stack = (x: Partial<Stack> & { id: string }): Stack => ({
	name: x.id,
	environmentId: 'e1',
	status: 'deployed',
	view: 'full',
	actions: [],
	...x
});

const rows = [
	stack({
		id: 'silo',
		displayName: 'Silo',
		description: 'Photos and files',
		engine: { state: 'running', services: [] },
		undeployedChanges: true
	}),
	stack({ id: 'shop', engine: { state: 'partial', services: [] }, environmentId: 'e2' }),
	stack({ id: 'wiki', status: 'undeployed' }),
	stack({ id: 'media', status: 'failed' }),
	stack({ id: 'docs', engine: { state: 'stopped', services: [] } }),
	stack({ id: 'blog', status: 'down' })
];
const updates = new Map([['shop', 'update_available']]);

const run = (values: Record<string, string>, q = '', envs: { id: string; name: string }[] = []) =>
	applyListFilters(rows, stackFilters({ envs, updates }), { q, values }, stackSearch).map(
		(s) => s.id
	);

describe('stack list filters (#7)', () => {
	it('searches the project name, display name and description', () => {
		expect(run({}, 'photos')).toEqual(['silo']);
		expect(run({}, 'SHOP')).toEqual(['shop']);
	});

	it('filters by the status the badge shows', () => {
		expect(run({ status: 'running' })).toEqual(['silo']);
		expect(run({ status: 'partial' })).toEqual(['shop']);
		expect(run({ status: 'undeployed' })).toEqual(['wiki']);
		expect(run({ status: 'failed' })).toEqual(['media']);
		// Stopped also finds a stack whose Stop removed its containers.
		expect(run({ status: 'stopped' })).toEqual(['docs', 'blog']);
	});

	it('filters by pending changes: undeployed, update available or none', () => {
		expect(run({ changes: 'undeployed' })).toEqual(['silo']);
		expect(run({ changes: 'update' })).toEqual(['shop']);
		expect(run({ changes: 'none' })).toEqual(['wiki', 'media', 'docs', 'blog']);
	});

	it('offers the environment only while every environment is shown', () => {
		const ids = (envs: { id: string; name: string }[]) =>
			stackFilters({ envs, updates }).map((f) => f.id);
		expect(ids([])).toEqual(['status', 'changes']);
		expect(ids([{ id: 'e1', name: 'homelab' }])).toEqual(['status', 'changes', 'environment']);
		expect(run({ environment: 'e2' }, '', [{ id: 'e2', name: 'nas' }])).toEqual(['shop']);
	});
});
