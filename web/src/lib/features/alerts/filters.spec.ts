import { describe, expect, it } from 'vitest';
import {
	applyListFilters,
	emptyFilterState,
	isFiltering,
	selectOptions,
	type ListFilterState
} from '$lib/features/resources/filters';
import {
	ALERTS_LIST,
	alertFilters,
	alertQuery,
	alertSearch,
	alertsPreset,
	alertsSummary,
	alertsView
} from './filters';
import { alertKeys, normalizeFilter } from './queries';
import { degradedArray, failingDisk, offlineEdge } from './test/samples';

const envs = [
	{ id: 'e2', name: 'edge' },
	{ id: 'e1', name: 'homelab' }
];
const state = (values: Record<string, string>, q = ''): ListFilterState => ({ q, values });

describe('alerts list filters (#159)', () => {
	it('offers state (Active by default), kind and, with every environment shown, environment', () => {
		const all = alertFilters({ envs });
		expect(all.map((f) => [f.id, f.label])).toEqual([
			['state', 'State'],
			['kind', 'Kind'],
			['environment', 'Environment']
		]);
		expect(selectOptions(all[0], undefined).map((o) => o.label)).toEqual([
			'Active',
			'Dismissed',
			'Resolved'
		]);
		expect(selectOptions(all[1], undefined).map((o) => o.label)).toEqual([
			'All Kinds',
			'Disk Health',
			'RAID',
			'Temperature',
			'Disk Space',
			'Memory',
			'Environment Offline',
			'Backups Paused',
			'Updates Available',
			'Failed Job'
		]);
		expect(selectOptions(all[2], undefined).map((o) => o.label)).toEqual([
			'All Environments',
			'edge',
			'homelab'
		]);
		// One environment: no environment filter.
		expect(alertFilters({ envs: [] }).map((f) => f.id)).toEqual(['state', 'kind']);
	});

	it('treats Active as the default: only other states, kinds or searches filter', () => {
		const defs = alertFilters({ envs });
		expect(isFiltering(defs, emptyFilterState())).toBe(false);
		expect(alertsView(defs, emptyFilterState())).toBe('active');
		expect(alertsView(defs, state({ state: 'resolved' }))).toBe('resolved');
		expect(alertsView(defs, state({ state: 'nonsense' }))).toBe('active');
		expect(isFiltering(defs, state({ state: 'dismissed' }))).toBe(true);
		expect(isFiltering(defs, state({ kind: 'raid' }))).toBe(true);
	});

	it('asks the server for the state, kind and environment; the switcher wins', () => {
		const defs = alertFilters({ envs });
		expect(alertQuery(defs, emptyFilterState(), null)).toEqual({
			state: 'active',
			kind: undefined,
			environmentId: undefined
		});
		expect(
			alertQuery(defs, state({ state: 'dismissed', kind: 'raid', environment: 'e2' }), null)
		).toEqual({ state: 'dismissed', kind: 'raid', environmentId: 'e2' });
		expect(alertQuery(defs, state({ environment: 'e2' }), 'e1').environmentId).toBe('e1');
		// One key per distinct filter.
		expect(alertKeys.list(alertQuery(defs, emptyFilterState(), null))).toEqual([
			'alerts',
			'list',
			{ state: 'active' }
		]);
		expect(normalizeFilter({ state: 'firing', kind: undefined, environmentId: '' })).toEqual({
			state: 'firing'
		});
	});

	it('searches titles, details, environments and kinds, and filters the loaded rows', () => {
		const defs = alertFilters({ envs });
		const names = new Map(envs.map((e) => [e.id, e.name]));
		const search = alertSearch((id) => names.get(id));
		const rows = [failingDisk, degradedArray, offlineEdge];
		const found = (s: ListFilterState) =>
			applyListFilters(rows, defs, s, search).map((a) => a.id);
		expect(found(state({}, 'reallocated'))).toEqual(['a1']);
		expect(found(state({}, 'EDGE'))).toEqual(['a3']);
		expect(found(state({}, 'raid'))).toEqual(['a2']);
		expect(found(state({ kind: 'disk_health' }))).toEqual(['a1']);
		expect(found(state({ environment: 'e1' }))).toEqual(['a1', 'a2']);
		expect(found(state({ state: 'dismissed' }))).toEqual([]);
	});

	it('counts what is shown', () => {
		expect(alertsSummary(3, 3)).toBe('3 alerts');
		expect(alertsSummary(1, 1)).toBe('1 alert');
		expect(alertsSummary(1, 4)).toBe('1 of 4 alerts');
	});

	it('presets the list for links: kind, state and environment', () => {
		expect(alertsPreset({ kind: 'disk_health' })).toEqual({
			filters: { list: ALERTS_LIST, values: { kind: 'disk_health' } }
		});
		expect(alertsPreset({ kind: 'raid', view: 'dismissed', environmentId: 'e1' })).toEqual({
			filters: {
				list: ALERTS_LIST,
				values: { state: 'dismissed', kind: 'raid', environment: 'e1' }
			}
		});
		// The switcher already shows the environment: no filter needed.
		expect(alertsPreset({ kind: 'raid', environmentId: 'e1' }, 'e1')).toEqual({
			filters: { list: ALERTS_LIST, values: { kind: 'raid' } }
		});
		// It shows another one: select this one.
		expect(alertsPreset({ kind: 'raid', environmentId: 'e1' }, 'e2')).toEqual({
			filters: { list: ALERTS_LIST, values: { kind: 'raid' } },
			select: 'e1'
		});
	});
});
