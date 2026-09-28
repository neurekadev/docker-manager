import { describe, expect, it, vi } from 'vitest';
import type { Job } from '$lib/api/client';
import type { JobWatcher } from '$lib/api/jobs.svelte';
import type { Container, Image, Network, Volume } from '$lib/api/queries';
import {
	bulkSummary,
	nameList,
	planContainers,
	planImageRemoval,
	planNetworkRemoval,
	planVolumeRemoval
} from './bulk';
import { runBulk } from './bulk-run';

const ALL = [
	'container.start',
	'container.stop',
	'container.restart',
	'container.remove',
	'image.remove',
	'volume.remove',
	'network.remove'
];

const container = (name: string, over: Partial<Container> = {}): Container => ({
	id: name,
	name,
	environmentId: 'e1',
	state: 'running',
	view: 'full',
	actions: ALL,
	...over
});

const protection = {
	role: 'agent' as const,
	reason: 'the Docker Agent of this environment',
	self: true,
	restartAllowed: false
};

describe('bulk plans (#6, #32)', () => {
	it("runs on the containers the action applies to and leaves Docker Manager's own out", () => {
		const web = container('web');
		const db = container('db', { state: 'exited' });
		const agent = container('docker-agent', { protection });
		const managed = container('shop-web', {
			stack: { project: 'shop', managed: true, stackId: 's1' }
		});
		const stop = planContainers([web, db, agent], 'stop');
		expect(stop.run).toEqual([web]);
		expect(stop.skipped).toEqual([db]);
		expect(stop.refused).toEqual([
			{ item: agent, reason: 'Part of Docker Manager, which never stops its own containers.' }
		]);
		const start = planContainers([web, db], 'start');
		expect(start.run).toEqual([db]);
		expect(start.skipped).toEqual([web]);
		const remove = planContainers([web, managed], 'remove');
		expect(remove.run).toEqual([web]);
		expect(remove.refused[0]).toEqual({
			item: managed,
			reason: 'Belongs to the stack shop: remove it through the stack.'
		});
		// A stop is fine on a stack's container (the stack shows it stopped).
		expect(planContainers([managed], 'stop').run).toEqual([managed]);
	});

	it('skips containers whose action is not granted (#17)', () => {
		const ro = container('ro', { actions: ['container.details.read'] });
		expect(planContainers([ro], 'restart')).toEqual({ run: [], refused: [], skipped: [ro] });
	});

	it('refuses images, volumes and networks in use, predefined or Docker Manager-owned', () => {
		const base = { environmentId: 'e1', actions: ALL };
		const used = {
			...base,
			id: 'i1',
			repoTags: ['nginx:1'],
			usedBy: [{ id: 'c', name: 'web' }]
		};
		const free = { ...base, id: 'i2', repoTags: [] };
		const own = {
			...base,
			id: 'i3',
			repoTags: ['docker-manager:edge'],
			protection: { ...protection, role: 'docker_manager_image' as const }
		};
		const images = planImageRemoval([used, free, own] as unknown as Image[]);
		expect(images.run.map((i) => i.id)).toEqual(['i2']);
		expect(images.refused.map((r) => r.reason)).toEqual([
			'Containers still use it.',
			'Docker Manager uses it (the Docker Agent of this environment), so it is never removed.'
		]);

		const volumes = planVolumeRemoval([
			{ ...base, name: 'data', inUse: true },
			{ ...base, name: 'old', inUse: false },
			{ ...base, name: 'ro', actions: [] }
		] as unknown as Volume[]);
		expect(volumes.run.map((v) => v.name)).toEqual(['old']);
		expect(volumes.refused[0].reason).toBe('Containers still mount it.');
		expect(volumes.skipped.map((v) => v.name)).toEqual(['ro']);

		const networks = planNetworkRemoval(
			[
				{ ...base, id: 'n1', name: 'bridge', builtin: true },
				{ ...base, id: 'n2', name: 'shop_default' },
				{ ...base, id: 'n3', name: 'spare' }
			] as unknown as Network[],
			new Set(['e1/shop_default'])
		);
		expect(networks.run.map((n) => n.name)).toEqual(['spare']);
		expect(networks.refused.map((r) => r.reason)).toEqual([
			'Docker needs its predefined networks.',
			'Containers are still attached to it.'
		]);
	});
});

describe('bulk summary', () => {
	const noun = { one: 'container', many: 'containers' };

	it('names lists in a sentence', () => {
		expect(nameList([])).toBe('');
		expect(nameList(['a'])).toBe('a');
		expect(nameList(['a', 'b', 'c'])).toBe('a, b and c');
		expect(nameList(['a', 'b', 'c', 'd', 'e'])).toBe('a, b, c and 2 more');
	});

	it('repeats the action with the count, then what did not happen and why', () => {
		expect(
			bulkSummary('stop', noun, {
				succeeded: ['a', 'b'],
				failed: [],
				refused: [],
				skipped: 0
			})
		).toEqual({ tone: 'success', title: 'Stopped 2 containers', body: undefined });
		expect(
			bulkSummary('restart', noun, {
				succeeded: ['a'],
				failed: ['b'],
				refused: [{ name: 'docker-agent', reason: 'Part of Docker Manager.' }],
				skipped: 2
			})
		).toEqual({
			tone: 'warn',
			title: 'Restarted 1 container',
			body: '1 failed: b. Left out docker-agent. Part of Docker Manager. 2 skipped: the action does not apply to them.'
		});
		expect(
			bulkSummary('remove', noun, { succeeded: [], failed: ['a'], refused: [], skipped: 0 })
		).toEqual({ tone: 'error', title: 'No containers were removed.', body: '1 failed: a.' });
	});
});

describe('runBulk', () => {
	function watchers() {
		const finishers = new Map<string, (j: Job) => void>();
		const factory = (id: string, onfinish: (j: Job) => void) => {
			finishers.set(id, onfinish);
			return { start: vi.fn() } as unknown as JobWatcher;
		};
		return {
			factory,
			finish: (id: string, state: Job['state']) => finishers.get(id)!({ id, state } as Job)
		};
	}

	it('sends one request per object and shows one toast once every job has ended', async () => {
		const w = watchers();
		const toast = { success: vi.fn(), warn: vi.fn(), error: vi.fn() };
		const send = vi.fn(async (c: Container) => {
			if (c.name === 'bad') throw new Error('refused');
			return { id: `job-${c.name}` };
		});
		const web = container('web');
		const db = container('db');
		const bad = container('bad');
		const agent = container('docker-agent', { protection });
		await runBulk({
			plan: { run: [web, db, bad], refused: [{ item: agent, reason: 'Ours.' }], skipped: [] },
			verb: 'stop',
			noun: { one: 'container', many: 'containers' },
			name: (c) => c.name,
			send,
			ctx: (c) => ({ kind: 'container', name: c.name, verb: 'stop' }),
			toast,
			watcher: w.factory
		});
		expect(send).toHaveBeenCalledTimes(3);
		// No toast per object: only the summary, after the last job.
		w.finish('job-web', 'succeeded');
		expect(toast.success).not.toHaveBeenCalled();
		expect(toast.warn).not.toHaveBeenCalled();
		w.finish('job-db', 'failed');
		expect(toast.warn).toHaveBeenCalledTimes(1);
		expect(toast.warn).toHaveBeenCalledWith('Stopped 1 container', {
			body: '2 failed: bad and db. Left out docker-agent. Ours.'
		});
	});

	it('reports at once when nothing was accepted', async () => {
		const toast = { success: vi.fn(), warn: vi.fn(), error: vi.fn() };
		await runBulk({
			plan: { run: [], refused: [], skipped: [container('web')] },
			verb: 'start',
			noun: { one: 'container', many: 'containers' },
			name: (c) => c.name,
			send: vi.fn(),
			ctx: (c) => ({ kind: 'container', name: c.name, verb: 'start' }),
			toast
		});
		expect(toast.error).toHaveBeenCalledWith('No containers were started.', {
			body: '1 skipped: the action does not apply to it.'
		});
	});
});
