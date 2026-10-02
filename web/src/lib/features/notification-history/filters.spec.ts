import { describe, expect, it } from 'vitest';
import {
	applyListFilters,
	emptyFilterState,
	selectOptions,
	type ListFilterState
} from '$lib/features/resources/filters';
import { keysForInvalidate } from '$lib/live/keys';
import { notificationFilters, notificationQuery, notificationSearch } from './filters';
import { notificationKeys } from './queries';
import { failedUpdate, nightlyBackup, weeklyPrune } from './test/samples';

const envs = [
	{ id: 'e2', name: 'edge' },
	{ id: 'e1', name: 'homelab' }
];
const state = (values: Record<string, string>, q = ''): ListFilterState => ({ q, values });

describe('notifications list filters', () => {
	it('offers kind, outcome and, with every environment shown, environment', () => {
		const all = notificationFilters({ envs });
		expect(all.map((f) => [f.id, f.label])).toEqual([
			['kind', 'Kind'],
			['outcome', 'Outcome'],
			['environment', 'Environment']
		]);
		expect(selectOptions(all[0], undefined).map((o) => o.label)).toEqual([
			'All Kinds',
			'Backups',
			'Restores',
			'Prune',
			'Image Updates'
		]);
		expect(selectOptions(all[1], undefined).map((o) => o.label)).toEqual([
			'All Outcomes',
			'Done',
			'Warning',
			'Failed'
		]);
		expect(notificationFilters({ envs: [] }).map((f) => f.id)).toEqual(['kind', 'outcome']);
	});

	it('asks the server for the kind, outcome and environment; the switcher wins', () => {
		const defs = notificationFilters({ envs });
		expect(notificationQuery(defs, emptyFilterState(), null)).toEqual({
			kind: undefined,
			outcome: undefined,
			environmentId: undefined
		});
		expect(
			notificationQuery(
				defs,
				state({ kind: 'prune', outcome: 'failure', environment: 'e2' }),
				null
			)
		).toEqual({ kind: 'prune', outcome: 'failure', environmentId: 'e2' });
		expect(notificationQuery(defs, state({ environment: 'e2' }), 'e1').environmentId).toBe(
			'e1'
		);
		// Unknown values filter nothing.
		expect(notificationQuery(defs, state({ kind: 'nope' }), null).kind).toBeUndefined();
	});

	it('keys one list per distinct filter, refreshed by every new notification', () => {
		const defs = notificationFilters({ envs });
		const key = notificationKeys.pages(notificationQuery(defs, state({ kind: 'prune' }), null));
		expect(key).toEqual(['notifications', 'list', 'pages', { kind: 'prune' }]);
		const refreshed = keysForInvalidate({
			topic: 'alerts',
			kind: 'notification',
			resourceId: 'n1',
			action: 'created',
			at: ''
		}).map((i) => i.key);
		// The live client invalidates by prefix.
		expect(refreshed).toEqual([notificationKeys.all]);
		expect(key.slice(0, notificationKeys.all.length)).toEqual(notificationKeys.all);
	});

	it('searches titles, details, environments, kinds, outcomes and values', () => {
		const defs = notificationFilters({ envs });
		const names = new Map(envs.map((e) => [e.id, e.name]));
		const search = notificationSearch((id) => names.get(id));
		const rows = [nightlyBackup, weeklyPrune, failedUpdate];
		const found = (s: ListFilterState) =>
			applyListFilters(rows, defs, s, search).map((n) => n.id);
		expect(found(state({}, '12.4 GiB'))).toEqual(['n1']);
		expect(found(state({}, 'EDGE'))).toEqual(['n2']);
		expect(found(state({}, 'image updates'))).toEqual(['n3']);
		expect(found(state({}, 'worker'))).toEqual(['n3']);
		expect(found(state({ kind: 'backup' }))).toEqual(['n1']);
		expect(found(state({ outcome: 'failure' }))).toEqual(['n3']);
		expect(found(state({ environment: 'e1' }))).toEqual(['n1', 'n3']);
	});
});
