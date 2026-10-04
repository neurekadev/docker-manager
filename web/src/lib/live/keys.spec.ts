import { describe, expect, it } from 'vitest';
import { flushSync } from 'svelte';
import {
	IN_APP_CHANNEL_ID,
	TOPICS,
	keyInEnvironment,
	keysForFiles,
	keysForInvalidate,
	liveKeys
} from './keys';
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
		// A change of the In App channel changes what the bell lists.
		expect(inv('settings', 'notification_channel', IN_APP_CHANNEL_ID)).toEqual([
			['settings', 'list'],
			['settings', 'item', IN_APP_CHANNEL_ID],
			['alerts', 'list'],
			['notifications', 'list']
		]);
		expect(inv('settings', 'notification_channel', 'c-1')).toEqual([
			['settings', 'list'],
			['settings', 'item', 'c-1']
		]);
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

		// The move to a new server: the owner's move (and its form's
		// defaults, by prefix), or everyone's session for the move lock.
		expect(inv('manager', 'manager_move', 'mv-1')).toEqual([['manager', 'item', 'move']]);
		expect(inv('manager', 'manager_move_lock', 'instance')).toEqual([['session']]);
		expect(liveKeys.managerMove('defaults')).toEqual(['manager', 'item', 'move', 'defaults']);

		// Alerts (#159): any change refreshes every alerts list (the page,
		// the bell, the dashboard and the environment page), by prefix.
		expect(inv('alerts', 'alert', 'a1', 'e1')).toEqual([
			['alerts', 'list'],
			['alerts', 'item', 'a1']
		]);
		expect(liveKeys.alerts()).toEqual(['alerts', 'list']);
		expect(liveKeys.alerts({ state: 'active' })).toEqual([
			'alerts',
			'list',
			{ state: 'active' }
		]);
		expect(TOPICS).toContain('alerts');

		// A new notification (topic alerts, kind notification) refreshes
		// every notifications list, and no alerts list.
		expect(inv('alerts', 'notification', 'n1', 'e1')).toEqual([['notifications', 'list']]);
		expect(liveKeys.notifications({ kind: 'prune' })).toEqual([
			'notifications',
			'list',
			{ kind: 'prune' }
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
