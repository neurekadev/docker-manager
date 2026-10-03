import { describe, expect, it } from 'vitest';
import type { Container, Image, Network, Volume } from '$lib/api/queries';
import {
	activeValue,
	applyListFilters,
	containerFilters,
	containerSearch,
	distinctOptions,
	imageFilters,
	imageSearch,
	isFiltering,
	labelTexts,
	listSummary,
	networkFilters,
	networkSearch,
	parseFilterState,
	SWITCH_ON,
	selectOptions,
	serializeFilterState,
	visibleFilters,
	volumeFilters,
	volumeSearch,
	type ListFilter,
	type ListFilterState
} from './filters';

const state = (values: Record<string, string> = {}, q = ''): ListFilterState => ({ q, values });
const names = (rows: readonly { name: string }[]) => rows.map((r) => r.name);
const byId = <T>(filters: ListFilter<T>[], id: string) => {
	const f = filters.find((x) => x.id === id);
	if (!f) throw new Error(`no filter ${id}`);
	return f;
};
const envs = [
	{ id: 'e2', name: 'nas' },
	{ id: 'e1', name: 'homelab' }
];

const protection = {
	role: 'agent' as const,
	reason: 'the Docker Agent',
	self: true,
	restartAllowed: false
};

describe('the filter model', () => {
	const status: ListFilter<{ name: string; s: string }> = {
		id: 'status',
		label: 'Status',
		all: 'All Statuses',
		options: [{ value: 'running', label: 'Running' }],
		match: (r, v) => r.s === v
	};
	const project: ListFilter<{ name: string; s: string }> = {
		id: 'project',
		label: 'Project',
		all: 'All Projects',
		dynamic: true,
		options: [],
		match: (r, v) => r.name.startsWith(v)
	};
	const rows = [
		{ name: 'web', s: 'running' },
		{ name: 'db', s: 'exited' }
	];

	it('ignores values a static filter does not offer, keeps those of a dynamic one', () => {
		expect(activeValue(status, 'running')).toBe('running');
		expect(activeValue(status, 'bogus')).toBe('');
		expect(activeValue(status, undefined)).toBe('');
		expect(activeValue(project, 'gone')).toBe('gone');
		expect(
			applyListFilters(rows, [status], state({ status: 'bogus' }), (r) => [r.name])
		).toEqual(rows);
		expect(isFiltering([status], state({ status: 'bogus' }))).toBe(false);
		expect(isFiltering([status], state({ other: 'x' }))).toBe(false);
	});

	it('combines the case-insensitive search with every active filter', () => {
		const search = (r: { name: string }) => [r.name];
		expect(names(applyListFilters(rows, [status], state({}, ' WE '), search))).toEqual(['web']);
		expect(
			names(applyListFilters(rows, [status], state({ status: 'running' }), search))
		).toEqual(['web']);
		expect(
			applyListFilters(rows, [status], state({ status: 'running' }, 'db'), search)
		).toEqual([]);
		expect(isFiltering([status], state({}, '  '))).toBe(false);
		expect(isFiltering([status], state({}, 'x'))).toBe(true);
	});

	it('lists "all" first and keeps a stored value that is no longer offered', () => {
		expect(selectOptions(status, '')).toEqual([
			{ value: '', label: 'All Statuses' },
			{ value: 'running', label: 'Running' }
		]);
		expect(selectOptions(project, 'gone').at(-1)).toEqual({ value: 'gone', label: 'gone' });
		expect(selectOptions(status, 'bogus')).toHaveLength(2);
	});

	it('hides filters built from the rows while they offer fewer than two choices, unless set', () => {
		expect(visibleFilters([status, project], state()).map((f) => f.id)).toEqual(['status']);
		expect(visibleFilters([status, project], state({ project: 'x' })).map((f) => f.id)).toEqual(
			['status', 'project']
		);
	});

	it('applies a switch only while it is on', () => {
		const unused: ListFilter<{ name: string; s: string }> = {
			id: 'unused',
			label: 'Unused',
			kind: 'switch',
			all: 'Only unused things',
			match: (r) => r.s === 'exited'
		};
		expect(activeValue(unused, SWITCH_ON)).toBe(SWITCH_ON);
		expect(activeValue(unused, 'yes')).toBe('');
		expect(isFiltering([unused], state())).toBe(false);
		expect(names(applyListFilters(rows, [unused], state({ unused: 'on' }), () => []))).toEqual([
			'db'
		]);
	});

	it('builds sorted distinct options and the count text', () => {
		expect(distinctOptions(['b', undefined, 'a', 'b', ''])).toEqual([
			{ value: 'a', label: 'a' },
			{ value: 'b', label: 'b' }
		]);
		expect(listSummary(40, 40, false, 'container', 'containers')).toBe('40 containers');
		expect(listSummary(1, 1, false, 'container', 'containers')).toBe('1 container');
		expect(listSummary(3, 40, true, 'container', 'containers')).toBe('3 of 40 containers');
	});
});

describe('stored list state (never trusted)', () => {
	it('round-trips the search and set filters, dropping empty ones', () => {
		const raw = serializeFilterState(state({ status: 'running', stack: '' }, 'web'));
		expect(raw).not.toBeNull();
		expect(parseFilterState(raw)).toEqual(state({ status: 'running' }, 'web'));
		expect(serializeFilterState(state({ stack: '' }))).toBeNull();
	});

	it('yields the empty state for corrupt or foreign input', () => {
		for (const raw of [null, '', '{', '[]', '"x"', '42', 'null', '{"q":7,"values":[]}'])
			expect(parseFilterState(raw)).toEqual(state());
	});

	it('drops values that are not short strings under plain IDs', () => {
		const parsed = parseFilterState(
			JSON.stringify({
				q: 'x'.repeat(201),
				values: {
					status: 'running',
					count: 3,
					'Bad Key': 'x',
					empty: '',
					long: 'y'.repeat(201),
					nested: { a: 1 }
				}
			})
		);
		expect(parsed).toEqual(state({ status: 'running' }));
	});
});

describe('container filters (#6)', () => {
	const c = (x: Partial<Container> & { name: string }): Container => ({
		id: x.name,
		environmentId: 'e1',
		state: 'running',
		view: 'full',
		actions: [],
		...x
	});
	const rows = [
		c({
			name: 'web',
			image: 'nginx',
			stack: { project: 'silo', service: 'web', managed: true },
			labels: { tier: 'front' },
			update: 'update_available',
			imageId: 'sha256:4f2a',
			networks: [{ name: 'silo_default', ipAddress: '172.18.0.5' }]
		}),
		c({
			name: 'db',
			image: 'postgres:16',
			state: 'exited',
			stack: { project: 'silo', service: 'db', managed: true },
			update: 'up_to_date'
		}),
		c({
			name: 'pihole',
			image: 'pihole/pihole',
			health: 'unhealthy',
			labels: { 'traefik.enable': 'true' },
			update: 'check_failed',
			environmentId: 'e2'
		}),
		c({ name: 'docker-agent', protection, update: 'ineligible' })
	];
	const filters = containerFilters(rows, { envs: [] });
	const run = (values: Record<string, string>, q = '') =>
		names(applyListFilters(rows, filters, state(values, q), containerSearch));

	it('searches name, image, digest, labels, networks, addresses and Compose project', () => {
		expect(run({}, 'POSTGRES')).toEqual(['db']);
		expect(run({}, 'silo')).toEqual(['web', 'db']);
		expect(run({}, 'sha256:4f2')).toEqual(['web']);
		expect(run({}, 'tier')).toEqual(['web']);
		expect(run({}, 'traefik.enable=true')).toEqual(['pihole']);
		expect(run({}, 'tier=back')).toEqual([]);
		expect(run({}, '172.18.0.5')).toEqual(['web']);
		expect(run({}, 'silo_default')).toEqual(['web']);
	});

	it('filters by status (unhealthy and not running are their own choices), stack and available updates', () => {
		expect(run({ status: 'running' })).toEqual(['web', 'pihole', 'docker-agent']);
		expect(run({ status: 'unhealthy' })).toEqual(['pihole']);
		expect(run({ status: 'exited' })).toEqual(['db']);
		expect(run({ status: 'not_running' })).toEqual(['db']);
		expect(run({ stack: 'silo' })).toEqual(['web', 'db']);
		expect(run({ stack: '-' })).toEqual(['pihole', 'docker-agent']);
		expect(run({ updates: 'on' })).toEqual(['web']);
		expect(byId(filters, 'updates').kind).toBe('switch');
		expect(filters.map((f) => f.id)).toEqual(['status', 'stack', 'updates']);
	});

	it('offers the environment only while every environment is shown', () => {
		expect(filters.some((f) => f.id === 'environment')).toBe(false);
		const all = containerFilters(rows, { envs });
		expect(byId(all, 'environment').options?.map((o) => o.label)).toEqual(['homelab', 'nas']);
		expect(
			names(applyListFilters(rows, all, state({ environment: 'e2' }), containerSearch))
		).toEqual(['pihole']);
		expect(byId(filters, 'stack').options?.map((o) => o.value)).toEqual(['-', 'silo']);
	});

	it('turns labels into key=value texts', () => {
		expect(labelTexts({ a: '1', b: '' })).toEqual(['a=1', 'b=']);
		expect(labelTexts(undefined)).toEqual([]);
	});
});

describe('image filters (#6)', () => {
	const im = (x: Partial<Image> & { id: string }): Image => ({
		environmentId: 'e1',
		repoTags: [],
		view: 'full',
		actions: [],
		...x
	});
	const rows = [
		im({ id: 'sha256:aaa', repoTags: ['nginx:1.27'], inUse: true }),
		im({ id: 'sha256:bbb', inUse: false, repoDigests: ['nginx@sha256:ddd'] }),
		im({ id: 'sha256:ccc', repoTags: ['ghcr.io/x/manager:edge'], inUse: true, protection })
	];
	const filters = imageFilters({ envs: [] });
	const run = (values: Record<string, string>, q = '') =>
		applyListFilters(rows, filters, state(values, q), imageSearch).map((i) => i.id);

	it('searches tags, IDs and digests and switches to unused images', () => {
		expect(run({}, 'NGINX')).toEqual(['sha256:aaa', 'sha256:bbb']);
		expect(run({}, 'sha256:ddd')).toEqual(['sha256:bbb']);
		expect(run({ unused: 'on' })).toEqual(['sha256:bbb']);
		expect(filters.map((f) => f.id)).toEqual(['unused']);
		expect(imageFilters({ envs }).map((f) => f.id)).toEqual(['unused', 'environment']);
	});
});

describe('volume filters (#6)', () => {
	const vol = (x: Partial<Volume> & { name: string }): Volume => ({
		environmentId: 'e1',
		inUse: false,
		view: 'full',
		actions: [],
		driver: 'local',
		...x
	});
	const rows = [
		vol({ name: 'silo_data', inUse: true, stack: { project: 'silo', managed: true } }),
		vol({ name: 'media', driver: 'local', options: { type: 'nfs', o: 'addr=10.0.0.2' } }),
		vol({ name: 'rclone', driver: 'rclone' }),
		vol({ name: 'manager_data', inUse: true, protection }),
		vol({ name: 'other_data', stack: { project: 'other' } })
	];
	const filters = volumeFilters(rows, { envs: [] });
	const run = (values: Record<string, string>, q = '') =>
		names(applyListFilters(rows, filters, state(values, q), volumeSearch));

	it('searches and filters by driver, stack, unused and managed', () => {
		expect(run({}, 'SILO')).toEqual(['silo_data']);
		expect(run({}, 'rclone')).toEqual(['rclone']);
		expect(run({ unused: 'on' })).toEqual(['media', 'rclone', 'other_data']);
		expect(run({ stack: 'silo' })).toEqual(['silo_data']);
		expect(run({ driver: 'rclone' })).toEqual(['rclone']);
		expect(run({ managed: 'on' })).toEqual(['silo_data', 'manager_data']);
		expect(run({ managed: 'on', unused: 'on' })).toEqual([]);
		expect(byId(filters, 'driver').options?.map((o) => o.value)).toEqual(['local', 'rclone']);
		expect(filters.map((f) => [f.id, f.kind ?? 'select'])).toEqual([
			['driver', 'select'],
			['stack', 'select'],
			['unused', 'switch'],
			['managed', 'switch']
		]);
	});
});

describe('network filters (#6)', () => {
	const net = (x: Partial<Network> & { name: string }): Network => ({
		id: x.name,
		environmentId: 'e1',
		view: 'full',
		actions: [],
		driver: 'bridge',
		scope: 'local',
		...x
	});
	const rows = [
		net({ name: 'bridge', builtin: true, subnets: ['172.17.0.0/16'] }),
		net({ name: 'host', builtin: true, driver: 'host' }),
		net({ name: 'silo_default', stack: { project: 'silo', managed: true }, internal: true }),
		net({ name: 'proxy', attachable: true, enableIpv6: true, scope: 'swarm' }),
		net({ name: 'docker-manager', protection })
	];
	const used = new Set(['e1/bridge', 'e1/silo_default', 'e1/docker-manager']);
	const filters = networkFilters(rows, { envs: [], used });
	const run = (values: Record<string, string>, q = '') =>
		names(applyListFilters(rows, filters, state(values, q), networkSearch));

	it('searches name and subnet and filters by driver, stack, unused and managed', () => {
		expect(run({}, '172.17')).toEqual(['bridge']);
		expect(run({ stack: 'silo' })).toEqual(['silo_default']);
		expect(run({ driver: 'host' })).toEqual(['host']);
		expect(run({ unused: 'on' })).toEqual(['host', 'proxy']);
		expect(run({ managed: 'on' })).toEqual(['silo_default', 'docker-manager']);
	});

	it('offers no Unused switch without the attachments', () => {
		expect(networkFilters(rows, { envs: [] }).map((f) => f.id)).toEqual([
			'driver',
			'stack',
			'managed'
		]);
	});
});
