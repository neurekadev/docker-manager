// Container actions, cross-environment lists, permission checks and the
// job follow-up of the resource pages (#6, #17, #26, #32).
import { describe, expect, it, vi } from 'vitest';
import { ApiRequestError, createApiClient, type Job, type MyPermissions } from '$lib/api/client';
import type { JobWatcher } from '$lib/api/jobs.svelte';
import { acrossEnvironments, allPages, envTargets, type EnvTarget } from '$lib/api/multi-env';
import { containersQuery, queryKeys } from '$lib/api/queries';
import { Notices } from '$lib/shell/notices.svelte';
import { containerActions, runContainerAction } from './container-actions';
import { activeJobs, resourceKey, trackJob } from './jobs.svelte';
import { can, canInEnvironment, environmentsAllowing } from './permissions';

const base = 'http://localhost:8080';

function fakeFetch(respond: (req: Request) => { status: number; body: unknown }) {
	const calls: Request[] = [];
	const impl = async (input: RequestInfo | URL, init?: RequestInit) => {
		const req =
			input instanceof Request ? input : new Request(new URL(String(input), base), init);
		calls.push(req.clone());
		const { status, body } = respond(req);
		return new Response(JSON.stringify(body), {
			status,
			headers: { 'Content-Type': 'application/json' }
		});
	};
	return { impl: impl as typeof fetch, calls };
}

const all = [
	'container.start',
	'container.stop',
	'container.restart',
	'container.pause',
	'container.unpause',
	'container.remove'
];

describe('container actions (#6)', () => {
	it('offers the actions of the state that are granted (#17: hidden, not disabled)', () => {
		const verbs = (state: string, actions = all) =>
			containerActions({ state: state as 'running', actions }).map((a) => a.verb);
		expect(verbs('running')).toEqual(['stop', 'restart', 'pause', 'remove']);
		expect(verbs('paused')).toEqual(['unpause', 'stop', 'remove']);
		expect(verbs('exited')).toEqual(['start', 'remove']);
		expect(verbs('removing')).toEqual([]);
		expect(verbs('running', ['container.restart'])).toEqual(['restart']);
		expect(verbs('running', [])).toEqual([]);
	});

	it('sends each action to its route with an idempotency key', async () => {
		const f = fakeFetch(() => ({ status: 202, body: { id: 'j1', state: 'queued' } }));
		const client = createApiClient(f.impl, base);
		await runContainerAction('e1', 'web', 'stop', {}, client);
		await runContainerAction('e1', 'web', 'restart', { confirm: true }, client);
		await runContainerAction('e1', 'we b', 'remove', { force: true }, client);
		await runContainerAction('e1', 'web', 'unpause', {}, client);
		const [stop, restart, remove, unpause] = f.calls;
		expect(stop.method).toBe('POST');
		expect(new URL(stop.url).pathname).toBe('/api/v1/environments/e1/containers/web/stop');
		expect(stop.headers.get('Idempotency-Key')).toMatch(/.{8,}/);
		expect(await restart.json()).toEqual({ confirm: true });
		expect(remove.method).toBe('DELETE');
		expect(new URL(remove.url).pathname).toBe('/api/v1/environments/e1/containers/we%20b');
		expect(new URL(remove.url).searchParams.get('force')).toBe('true');
		expect(new URL(unpause.url).pathname).toBe(
			'/api/v1/environments/e1/containers/web/unpause'
		);
		expect(stop.headers.get('Idempotency-Key')).not.toBe(
			restart.headers.get('Idempotency-Key')
		);
	});

	it('surfaces the refusal of a protected container as the API error', async () => {
		const f = fakeFetch(() => ({
			status: 409,
			body: {
				code: 'protected',
				message: 'refused to stop a protected Docker Manager resource: the agent',
				requestId: 'r',
				retryable: false,
				details: []
			}
		}));
		const err = await runContainerAction(
			'e1',
			'docker-agent',
			'stop',
			{},
			createApiClient(f.impl, base)
		).catch((e) => e);
		expect(err).toBeInstanceOf(ApiRequestError);
		expect((err as ApiRequestError).apiError?.code).toBe('protected');
	});
});

describe('lists across environments (#22 switcher, #6 offline hosts)', () => {
	const targets: EnvTarget[] = [
		{ id: 'e1', name: 'homelab', online: true },
		{ id: 'e2', name: 'nas', online: true },
		{ id: 'e3', name: 'edge', online: false }
	];

	it('selects the chosen environment or every active one', () => {
		const envs = [
			{ id: 'e1', name: 'homelab', online: true, status: 'active' },
			{ id: 'e2', name: 'old', online: false, status: 'archived' }
		] as Parameters<typeof envTargets>[0];
		expect(envTargets(envs, null).map((t) => t.id)).toEqual(['e1']);
		expect(envTargets(envs, 'e1').map((t) => t.id)).toEqual(['e1']);
		expect(envTargets(envs, 'e9')).toEqual([]);
	});

	it('merges online environments and reports offline and failing ones', async () => {
		const offline503 = new ApiRequestError('x', 503, {
			code: 'environment_offline',
			message: 'x',
			requestId: '',
			retryable: true,
			details: []
		});
		const out = await acrossEnvironments(targets, async (env) => {
			if (env.id === 'e2') throw offline503;
			return [`${env.name}-a`];
		});
		expect(out.items).toEqual(['homelab-a']);
		expect(out.unavailable.map((u) => [u.environment.name, u.offline])).toEqual([
			['nas', true],
			['edge', true]
		]);
		const failing = await acrossEnvironments(targets, async (env) => {
			if (env.id === 'e2') throw new Error('agent timeout');
			return [env.id];
		});
		expect(failing.unavailable.find((u) => u.environment.id === 'e2')?.error).toBeInstanceOf(
			Error
		);
	});

	it('fails as a whole only when every environment failed', async () => {
		await expect(
			acrossEnvironments(targets.slice(0, 2), async () => {
				throw new Error('manager down');
			})
		).rejects.toThrow('manager down');
		expect((await acrossEnvironments([], async () => ['x'])).items).toEqual([]);
	});

	it('follows cursors to the last page', async () => {
		const pages: Record<string, { items: number[]; nextCursor?: string }> = {
			'': { items: [1, 2], nextCursor: 'c1' },
			c1: { items: [3] }
		};
		expect(await allPages(async (c) => pages[c ?? ''])).toEqual([1, 2, 3]);
	});

	it('keys container lists by their environments so live events refresh them (#23)', async () => {
		const f = fakeFetch((req) => ({
			status: 200,
			body: { items: [{ name: new URL(req.url).pathname.split('/')[4] }] }
		}));
		const q = containersQuery(targets.slice(0, 2), createApiClient(f.impl, base));
		expect(q.queryKey).toEqual(['containers', 'list', 'e1,e2']);
		expect(queryKeys.containers.detail('e1', 'web')).toEqual([
			'containers',
			'item',
			'e1',
			'web'
		]);
		const fn = q.queryFn as (ctx: unknown) => Promise<{ items: { name: string }[] }>;
		const data = await fn({ signal: new AbortController().signal });
		expect(data.items.map((c) => c.name)).toEqual(['e1', 'e2']);
		expect(new URL(f.calls[0].url).searchParams.get('limit')).toBe('200');
	});
});

describe('permissions (#17)', () => {
	const perms = (entries: MyPermissions['entries'], owner = false) =>
		({ owner, entries, environments: [], catalogVersion: 1 }) as MyPermissions;

	it('reads object actions and environment-scoped create rights', () => {
		expect(can(['container.stop'], 'container.stop')).toBe(true);
		expect(can(undefined, 'container.stop')).toBe(false);
		const p = perms([
			{
				capability: 'container.create',
				allowed: true,
				reason: '',
				source: 'group_rule',
				scope: { kind: 'environment', environmentId: 'e1' }
			},
			{
				capability: 'volume.create',
				allowed: true,
				reason: '',
				source: 'group_rule',
				scope: { kind: 'instance' }
			},
			{
				capability: 'image.pull',
				allowed: false,
				reason: '',
				source: 'user_rule',
				scope: { kind: 'instance' }
			}
		]);
		expect(canInEnvironment(p, 'container.create', 'e1')).toBe(true);
		expect(canInEnvironment(p, 'container.create', 'e2')).toBe(false);
		expect(canInEnvironment(p, 'volume.create', 'e2')).toBe(true);
		expect(canInEnvironment(p, 'image.pull', 'e1')).toBe(false);
		expect(canInEnvironment(perms([], true), 'image.pull', 'e1')).toBe(true);
		expect(canInEnvironment(undefined, 'image.pull', 'e1')).toBe(false);
		expect(environmentsAllowing(p, 'container.create', [{ id: 'e1' }, { id: 'e2' }])).toEqual([
			{ id: 'e1' }
		]);
	});
});

describe('job follow-up (#26)', () => {
	function stubWatcher() {
		let finish: (j: Job) => void = () => {};
		const factory = (_id: string, onfinish: (j: Job) => void) => {
			finish = onfinish;
			return { start: vi.fn() } as unknown as JobWatcher;
		};
		return { factory, finish: (j: Job) => finish(j) };
	}

	it('repeats the action when it succeeds and refreshes the lists', () => {
		const s = stubWatcher();
		const toast = { success: vi.fn(), error: vi.fn(), warn: vi.fn() };
		const notices = new Notices(() => 1);
		const queryClient = { invalidateQueries: vi.fn() };
		const key = resourceKey('container', 'e1', 'web');
		trackJob(
			{ id: 'j1' },
			{
				ctx: { kind: 'container', name: 'web', verb: 'stop' },
				key,
				watcher: s.factory,
				toast,
				notices,
				queryClient: queryClient as never,
				invalidate: [queryKeys.containers.all]
			}
		);
		expect(activeJobs.byKey[key]).toBeDefined();
		s.finish({ id: 'j1', state: 'succeeded' } as Job);
		expect(toast.success).toHaveBeenCalledWith('Stopped web');
		expect(notices.items[0]).toMatchObject({
			title: 'Stopped web',
			tone: 'ok',
			href: '/jobs/j1'
		});
		expect(queryClient.invalidateQueries).toHaveBeenCalledWith({ queryKey: ['containers'] });
		expect(activeJobs.byKey[key]).toBeUndefined();
	});

	it('says why a job failed, with the recovery advice', () => {
		const s = stubWatcher();
		const toast = { success: vi.fn(), error: vi.fn(), warn: vi.fn() };
		trackJob(
			{ id: 'j2' },
			{
				ctx: { kind: 'container', name: 'web', verb: 'start' },
				watcher: s.factory,
				toast,
				notices: null
			}
		);
		s.finish({
			id: 'j2',
			state: 'failed',
			error: {
				class: 'step_failed',
				message: 'port is already allocated',
				recovery: 'Free the port and start it again.'
			}
		} as Job);
		expect(toast.error).toHaveBeenCalledWith("web couldn't be started.", {
			body: 'Port is already allocated. Free the port and start it again.'
		});
	});
});
