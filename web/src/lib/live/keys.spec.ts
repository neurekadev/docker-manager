import { describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import { keyInEnvironment, keysForFiles, keysForInvalidate, liveKeys } from './keys';
import { CriticalWork } from './critical.svelte';

describe('invalidation map', () => {
	it('maps file changes to the listings, metadata and content they affect', () => {
		const keys = keysForFiles({
			scope: { kind: 'stack', id: 's1', environmentId: 'e1' },
			paths: ['config/app.env', 'compose.yaml'],
			overflow: false,
			at: ''
		}).map((i) => JSON.stringify(i.key));
		const s = { kind: 'stack', id: 's1' } as const;
		for (const k of [
			liveKeys.files(s, 'list', 'config'),
			liveKeys.files(s, 'list', 'config/app.env'),
			liveKeys.files(s, 'stat', 'config/app.env'),
			liveKeys.files(s, 'content', 'config/app.env'),
			liveKeys.files(s, 'list', '.'),
			liveKeys.files(s, 'content', 'compose.yaml')
		]) {
			expect(keys).toContain(JSON.stringify(k));
		}
		expect(new Set(keys).size).toBe(keys.length);
	});

	it('refreshes a whole scope on overflow and every file view on environment resets', () => {
		const vol = keysForFiles({
			scope: { kind: 'volume', id: 'pgdata', environmentId: 'e1' },
			paths: [],
			overflow: true,
			at: ''
		});
		expect(vol.map((i) => i.key)).toEqual([['files', 'volume', 'e1/pgdata']]);
		const env = keysForFiles({
			scope: { kind: 'environment', id: 'e1', environmentId: 'e1' },
			paths: [],
			overflow: true,
			at: ''
		});
		expect(env.map((i) => i.key)).toEqual([['files']]);
	});

	it('scopes Docker objects by environment and instance resources by ID', () => {
		const inv = (topic: string, kind: string, id: string, env?: string) =>
			keysForInvalidate({
				topic,
				kind,
				resourceId: id,
				environmentId: env,
				action: 'updated',
				at: ''
			}).map((i) => i.key);
		expect(inv('volumes', 'volume', 'data', 'e1')).toContainEqual([
			'volumes',
			'item',
			'e1',
			'data'
		]);
		expect(inv('stacks', 'stack', 's1', 'e1')).toContainEqual(['stacks', 'item', 's1']);
		expect(inv('policies', 'backup_policy', 'p1')).toEqual([
			['policies', 'list'],
			['policies', 'item', 'p1']
		]);
		expect(inv('permissions', 'group', 'g1')).toContainEqual(['me', 'permissions']);
		// New samples refresh the environment's charts and the dashboard's
		// overview (its latest CPU and memory), throttled as metrics.
		expect(inv('metrics', 'metrics', 'e1', 'e1')).toEqual([
			['metrics', 'item', 'e1'],
			['overview']
		]);
		expect(
			keysForInvalidate({
				topic: 'metrics',
				kind: 'metrics',
				resourceId: 'e1',
				environmentId: 'e1',
				action: 'updated',
				at: ''
			}).map((i) => i.class)
		).toEqual(['metrics', 'metrics']);
		// Live values (about every second) refresh only the current CPU and
		// memory, never the charts.
		expect(inv('metrics', 'live_metrics', 'e1', 'e1')).toEqual([
			['metrics', 'item', 'e1', 'containers-latest'],
			['metrics', 'item', 'e1', 'capacity'],
			['overview']
		]);
		expect(inv('environments', 'inventory', 'e1', 'e1')).toContainEqual([
			'environments',
			'item',
			'e1'
		]);
	});

	it('finds the keys of one environment', () => {
		expect(keyInEnvironment(['containers', 'item', 'e1', 'web'], 'e1')).toBe(true);
		expect(keyInEnvironment(['containers', 'item', 'e2', 'web'], 'e1')).toBe(false);
		expect(keyInEnvironment(['jobs', 'item', 'j1'], 'e1')).toBe(false);
		expect(keyInEnvironment(['jobs', 'list'], 'e1')).toBe(true);
		expect(keyInEnvironment(['files', 'volume', 'e2/x', 'list', '.'], 'e1')).toBe(false);
	});
});

describe('critical work registry', () => {
	it('tracks open work and releases idempotently', () => {
		const w = new CriticalWork();
		expect(w.active).toBe(false);
		const a = w.register('unsaved-edit', 'compose.yaml');
		const b = w.register('terminal', 'web-1');
		flushSync();
		expect(w.active).toBe(true);
		expect(w.items.map((i) => i.label)).toEqual(['compose.yaml', 'web-1']);
		a();
		a();
		flushSync();
		expect(w.items.map((i) => i.kind)).toEqual(['terminal']);
		b();
		flushSync();
		expect(w.active).toBe(false);
	});
});
