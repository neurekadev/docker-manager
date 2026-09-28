import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { applyListFilters, type ListFilterState } from '$lib/features/resources/filters';
import { jobFilters, jobQuery, jobSearch, jobsSearchedText, jobsSummary } from './filters';
import { stackNames } from './labels';

const state = (values: Record<string, string> = {}, q = ''): ListFilterState => ({ q, values });
const job = (x: Partial<Job> & { id: string }): Job =>
	({
		kind: 'stack.deploy',
		state: 'succeeded',
		origin: 'manual',
		createdAt: '2026-09-27T10:00:00Z',
		...x
	}) as Job;

const SILO = '0190a6e0-0000-7000-8000-000000000001';
const rows = [
	job({
		id: 'j1',
		environmentId: 'e1',
		targets: [{ type: 'stack', id: SILO }]
	}),
	job({
		id: 'j2',
		kind: 'image.pull',
		state: 'failed',
		environmentId: 'e2',
		origin: 'scheduled',
		targets: [{ type: 'image', id: 'nginx:1.27' }]
	}),
	job({ id: 'j3', kind: 'manager.backup', state: 'running' })
];
const envs = [
	{ id: 'e1', name: 'homelab' },
	{ id: 'e2', name: 'nas' }
];
const names = new Map(envs.map((e) => [e.id, e.name]));
const search = jobSearch(stackNames([{ id: SILO, name: 'silo', displayName: 'Silo' }]), (id) =>
	names.get(id)
);
const ids = (values: Record<string, string>, q = '', filters = jobFilters({ envs: [] })) =>
	applyListFilters(rows, filters, state(values, q), search).map((j) => j.id);

describe('job filters (#26)', () => {
	it('filters by state group and kind; the environment only while all are shown', () => {
		expect(ids({ state: 'problems' })).toEqual(['j2']);
		expect(ids({ state: 'active' })).toEqual(['j3']);
		expect(ids({ kind: 'image.pull' })).toEqual(['j2']);
		expect(jobFilters({ envs: [] }).map((f) => f.id)).toEqual(['state', 'kind']);
		const all = jobFilters({ envs });
		expect(all.map((f) => f.id)).toEqual(['state', 'kind', 'environment']);
		expect(ids({ environment: 'e2' }, '', all)).toEqual(['j2']);
	});

	it('searches the words a job shows: kind, target, environment, origin, state', () => {
		expect(ids({}, 'deploy')).toEqual(['j1']);
		expect(ids({}, 'silo')).toEqual(['j1']);
		expect(ids({}, 'NGINX')).toEqual(['j2']);
		expect(ids({}, 'nas')).toEqual(['j2']);
		expect(ids({}, 'scheduled')).toEqual(['j2']);
		expect(ids({}, 'docker manager')).toEqual(['j3']);
		expect(ids({}, 'failed')).toEqual(['j2']);
	});

	it('turns the list state into the server query; the switcher wins', () => {
		const all = jobFilters({ envs });
		expect(jobQuery(all, state({ state: 'problems', kind: 'image.pull' }), null)).toEqual({
			states: ['failed', 'partial', 'interrupted'],
			kind: 'image.pull',
			environmentId: '',
			policyId: ''
		});
		expect(jobQuery(all, state({ environment: 'e2' }), null).environmentId).toBe('e2');
		expect(jobQuery(all, state({ environment: 'e2' }), 'e1').environmentId).toBe('e1');
		expect(jobQuery(all, state({ state: 'bogus', kind: 'nope' }), null)).toEqual({
			states: [],
			kind: '',
			environmentId: '',
			policyId: ''
		});
	});

	it('narrows to one policy while its filter is set (?policyId=)', () => {
		const withPolicy = jobFilters({ envs: [], policy: { id: 'mp-1', name: 'Weekly prune' } });
		expect(withPolicy.map((f) => f.id)).toEqual(['state', 'kind', 'policy']);
		expect(withPolicy[2].options).toEqual([{ value: 'mp-1', label: 'Weekly prune' }]);
		expect(jobQuery(withPolicy, state({ policy: 'mp-1' }), null).policyId).toBe('mp-1');
		// Without the filter a stored value is ignored.
		expect(jobQuery(jobFilters({ envs: [] }), state({ policy: 'mp-1' }), null).policyId).toBe(
			''
		);
		const mine = [job({ id: 'p1', policyId: 'mp-1' }), job({ id: 'p2', policyId: 'mp-2' })];
		expect(
			applyListFilters(mine, withPolicy, state({ policy: 'mp-1' }), search).map((j) => j.id)
		).toEqual(['p1']);
	});

	it('counts the jobs, with the total the server knows', () => {
		const base = { searching: false, filtered: false };
		expect(jobsSummary({ ...base, shown: 50, loaded: 50, total: 1234, more: true })).toBe(
			'50 of 1,234 jobs'
		);
		expect(jobsSummary({ ...base, shown: 50, loaded: 50, more: true })).toBe('50 jobs loaded');
		expect(jobsSummary({ ...base, shown: 40, loaded: 40, total: 40, more: false })).toBe(
			'40 jobs'
		);
		expect(jobsSummary({ ...base, shown: 1, loaded: 1, more: false })).toBe('1 job');
		expect(
			jobsSummary({
				shown: 3,
				loaded: 50,
				total: 1234,
				more: true,
				searching: true,
				filtered: true
			})
		).toBe('3 of the 50 loaded jobs (1,234 in all)');
		expect(
			jobsSummary({ shown: 3, loaded: 50, more: true, searching: true, filtered: true })
		).toBe('3 of the 50 loaded jobs');
		expect(
			jobsSummary({ shown: 12, loaded: 40, more: false, searching: true, filtered: true })
		).toBe('12 of 40 jobs');
		expect(jobsSearchedText(50, 1234)).toBe('Searched 50 of the 1,234 jobs.');
		expect(jobsSearchedText(50)).toBe('Searched the 50 loaded jobs.');
		expect(jobsSearchedText(50, 50)).toBe('Searched the 50 loaded jobs.');
	});
});
