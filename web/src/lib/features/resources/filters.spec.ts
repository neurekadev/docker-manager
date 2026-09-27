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
	labelMatches,
	listSummary,
	networkFilters,
	networkSearch,
	parseFilterState,
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
		all: 'All statuses',
		options: [{ value: 'running', label: 'Running' }],
		match: (r, v) => r.s === v
	};
	const project: ListFilter<{ name: string; s: string }> = {
		id: 'project',
		label: 'Project',
		all: 'All projects',
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
			{ value: '', label: 'All statuses' },
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
			update: 'update_available'
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

	it('searches name, image and Compose project', () => {
		expect(run({}, 'POSTGRES')).toEqual(['db']);
		expect(run({}, 'silo')).toEqual(['web', 'db']);
	});

	it('filters by status (unhealthy is its own choice), stack, update, system and label', () => {
		expect(run({ status: 'running' })).toEqual(['web', 'pihole', 'docker-agent']);
		expect(run({ status: 'unhealthy' })).toEqual(['pihole']);
		expect(run({ stack: 'silo' })).toEqual(['web', 'db']);
		expect(run({ stack: '-' })).toEqual(['pihole', 'docker-agent']);
		expect(run({ update: 'available' })).toEqual(['web']);
		expect(run({ update: 'current' })).toEqual(['db']);
		expect(run({ update: 'problem' })).toEqual(['pihole']);
		expect(run({ update: 'none' })).toEqual(['docker-agent']);
		expect(run({ system: 'only' })).toEqual(['docker-agent']);
		expect(run({ system: 'hide' })).toEqual(['web', 'db', 'pihole']);
		expect(run({ label: 'tier' })).toEqual(['web']);
		expect(run({ label: 'traefik.enable=true' })).toEqual(['pihole']);
		expect(run({ label: 'tier=back' })).toEqual([]);
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

	it('matches labels by key or key=value', () => {
		expect(labelMatches({ a: '1' }, ' a ')).toBe(true);
		expect(labelMatches({ a: '1' }, 'a = 1')).toBe(true);
		expect(labelMatches({ a: '1' }, 'a=2')).toBe(false);
		expect(labelMatches(undefined, 'a')).toBe(false);
		expect(labelMatches(undefined, '')).toBe(true);
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
		im({ id: 'sha256:bbb', inUse: false }),
		im({ id: 'sha256:ccc', repoTags: ['ghcr.io/x/manager:edge'], inUse: true, protection })
	];
	const filters = imageFilters({ envs: [] });
	const run = (values: Record<string, string>, q = '') =>
		applyListFilters(rows, filters, state(values, q), imageSearch).map((i) => i.id);

	it('searches tags and IDs and filters by usage, tags and system', () => {
		expect(run({}, 'NGINX')).toEqual(['sha256:aaa']);
		expect(run({}, 'bbb')).toEqual(['sha256:bbb']);
		expect(run({ usage: 'unused' })).toEqual(['sha256:bbb']);
		expect(run({ usage: 'used' })).toEqual(['sha256:aaa', 'sha256:ccc']);
		expect(run({ tags: 'untagged' })).toEqual(['sha256:bbb']);
		expect(run({ tags: 'tagged', system: 'hide' })).toEqual(['sha256:aaa']);
	});
});

describe('volume filters (#6, #28)', () => {
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
		vol({ name: 'manager_data', inUse: true, protection })
	];
	const filters = volumeFilters(rows, { envs: [] });
	const run = (values: Record<string, string>, q = '') =>
		names(applyListFilters(rows, filters, state(values, q), volumeSearch));

	it('filters by usage, stack, file access, driver and system', () => {
		expect(run({}, 'SILO')).toEqual(['silo_data']);
		expect(run({ usage: 'unused' })).toEqual(['media', 'rclone']);
		expect(run({ stack: 'silo' })).toEqual(['silo_data']);
		expect(run({ files: 'readonly' })).toEqual(['media', 'rclone']);
		expect(run({ files: 'local' })).toEqual(['silo_data', 'manager_data']);
		expect(run({ driver: 'rclone' })).toEqual(['rclone']);
		expect(run({ system: 'only' })).toEqual(['manager_data']);
		expect(byId(filters, 'driver').options?.map((o) => o.value)).toEqual(['local', 'rclone']);
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
	const filters = networkFilters(rows, { envs: [] });
	const run = (values: Record<string, string>, q = '') =>
		names(applyListFilters(rows, filters, state(values, q), networkSearch));

	it('searches name and subnet and filters by stack, driver, access, scope, predefined and system', () => {
		expect(run({}, '172.17')).toEqual(['bridge']);
		expect(run({ stack: 'silo' })).toEqual(['silo_default']);
		expect(run({ driver: 'host' })).toEqual(['host']);
		expect(run({ access: 'internal' })).toEqual(['silo_default']);
		expect(run({ access: 'ipv6' })).toEqual(['proxy']);
		expect(run({ access: 'default' })).toEqual(['bridge', 'host', 'docker-manager']);
		expect(run({ scope: 'swarm' })).toEqual(['proxy']);
		expect(run({ predefined: 'hide' })).toEqual(['silo_default', 'proxy', 'docker-manager']);
		expect(run({ predefined: 'only', driver: 'bridge' })).toEqual(['bridge']);
		expect(run({ system: 'only' })).toEqual(['docker-manager']);
	});
});
