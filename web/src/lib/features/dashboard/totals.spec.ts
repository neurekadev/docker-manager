import { describe, expect, it } from 'vitest';
import type { Job, Schema, Stack, UpdatePolicy } from '$lib/api/client';
import { attentionItems, dashboardTotals, pendingChanges, perEnvironment } from './totals';

type Env = Schema<'OverviewEnvironment'>;

const env = (id: string, online: boolean, extra: Partial<Env> = {}): Env => ({
	id,
	name: id,
	online,
	view: 'full',
	actions: [],
	...extra
});
const usage = (cpu: number, used: number, total: number) => ({
	sampledAt: '2026-09-25T12:00:00Z',
	cpuPercent: cpu,
	memoryUsedBytes: used,
	memoryTotalBytes: total
});
const docker = (running: number, total: number) => ({
	containers: total,
	containersRunning: running,
	containersPaused: 0,
	containersStopped: total - running,
	images: 1,
	volumes: 1,
	networks: 1
});
const job = (id: string, state: Job['state'], createdAt: string, kind = 'stack.deploy') =>
	({ id, state, createdAt, kind }) as Job;

const NOW = Date.parse('2026-09-25T12:00:00Z');

describe('dashboard totals (#5 units)', () => {
	it('sums containers and memory of online environments and averages CPU', () => {
		const t = dashboardTotals(
			[
				env('homelab', true, { usage: usage(20, 2e9, 8e9), docker: docker(8, 9) }),
				env('nas', true, { usage: usage(4, 1e9, 16e9), docker: docker(2, 3) }),
				// Offline: last known usage is not "in use now", counts still are.
				env('edge', false, { usage: usage(90, 3e9, 4e9), docker: docker(1, 1) })
			],
			[],
			NOW
		);
		expect(t).toMatchObject({
			online: 2,
			offline: 1,
			counted: 3,
			containers: 13,
			running: 11,
			paused: 0,
			cpuAverage: 12,
			cpuBusiest: { name: 'homelab', value: 20 },
			memUsed: 3e9,
			memTotal: 24e9
		});
	});

	it('says nothing about usage without samples (never 0 B / 0 B)', () => {
		const t = dashboardTotals([env('a', true), env('b', false)], [], NOW);
		expect(t.cpuAverage).toBeNull();
		expect(t.cpuBusiest).toBeNull();
		expect(t.memTotal).toBe(0);
		expect(t.counted).toBe(0);
	});

	it('does not name a busiest environment when there is only one', () => {
		const t = dashboardTotals([env('a', true, { usage: usage(5, 1, 2) })], [], NOW);
		expect(t.cpuAverage).toBe(5);
		expect(t.cpuBusiest).toBeNull();
	});

	it('counts failed, partly failed and interrupted jobs of the last 24 hours', () => {
		const t = dashboardTotals(
			[],
			[
				job('j5', 'running', '2026-09-25T11:59:00Z'),
				job('j4', 'partial', '2026-09-25T11:00:00Z', 'prune.run'),
				job('j3', 'failed', '2026-09-25T10:00:00Z'),
				job('j2', 'succeeded', '2026-09-25T09:00:00Z'),
				job('j1', 'interrupted', '2026-09-24T11:00:00Z')
			],
			NOW
		);
		expect(t.failures).toBe(2);
		expect(t.lastFailure?.id).toBe('j4');
	});
});

describe('per-environment counts', () => {
	it('counts stacks, undeployed changes and available updates', () => {
		const stacks = [
			{ environmentId: 'e1', undeployedChanges: true },
			{ environmentId: 'e1' },
			{ environmentId: 'e2', undeployedChanges: false }
		] as Stack[];
		const policies = [
			{ environmentId: 'e1', summary: { available: 2 } },
			{ environmentId: 'e3', summary: { available: 1 } },
			{ environmentId: 'e2' }
		] as UpdatePolicy[];
		const m = perEnvironment(stacks, policies);
		expect(m.get('e1')).toEqual({ stacks: 2, undeployed: 1, updates: 2 });
		expect(m.get('e2')).toEqual({ stacks: 1, undeployed: 0, updates: 0 });
		expect(m.get('e3')).toEqual({ stacks: 0, undeployed: 0, updates: 1 });
		expect(perEnvironment(undefined, undefined).size).toBe(0);
	});
});

describe('needs attention (#22 dashboard)', () => {
	const calm = { offline: 0, failures: 0, counted: 1, containers: 4, running: 4, paused: 0 };

	it('is empty when everything runs', () => {
		expect(attentionItems(calm, { undeployed: 0, updates: 0 })).toEqual([]);
		expect(attentionItems({ ...calm, counted: 0, containers: 0, running: 0 })).toEqual([]);
	});

	it('lists every problem, most urgent first, each linking to its filtered list', () => {
		const items = attentionItems(
			{ offline: 1, failures: 2, counted: 2, containers: 9, running: 6, paused: 0 },
			{ undeployed: 1, updates: 3 }
		);
		expect(items.map((i) => [i.label, i.href])).toEqual([
			['1 environment is offline', '/environments'],
			['2 jobs failed in the last 24 hours', '/jobs'],
			['3 containers are not running', '/containers'],
			['1 stack has undeployed changes', '/stacks'],
			['3 updates are available', '/updates']
		]);
		expect(items.find((i) => i.id === 'failed-jobs')?.filters).toEqual({
			list: 'jobs',
			values: { state: 'problems' }
		});
		expect(items.find((i) => i.id === 'stopped-containers')?.filters).toEqual({
			list: 'containers',
			values: { status: 'exited' }
		});
		expect(items.find((i) => i.id === 'undeployed')?.filters).toEqual({
			list: 'stacks',
			values: { changes: 'undeployed' }
		});
		expect(items.find((i) => i.id === 'offline')?.filters).toBeUndefined();
	});

	it('opens the whole container list when some are paused', () => {
		const [item] = attentionItems({ ...calm, running: 2, paused: 1 });
		expect(item.label).toBe('2 containers are not running');
		expect(item.filters).toBeUndefined();
	});

	it('sums pending changes of the shown environments only', () => {
		const counts = new Map([
			['e1', { stacks: 2, undeployed: 1, updates: 2 }],
			['e2', { stacks: 1, undeployed: 1, updates: 5 }]
		]);
		expect(pendingChanges(counts, ['e1'])).toEqual({ undeployed: 1, updates: 2 });
		expect(pendingChanges(counts, ['e1', 'e2', 'e3'])).toEqual({ undeployed: 2, updates: 7 });
	});
});
