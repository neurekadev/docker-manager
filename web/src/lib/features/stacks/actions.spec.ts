import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/svelte-query';
import { createApiClient } from '$lib/api/client';
import {
	deployStack,
	etag,
	operateStack,
	patchStack,
	previewRename,
	renameStack,
	restartSource,
	runUpdate,
	validateStackFiles
} from './actions';
import { stackKeys, stackMetricsQuery } from './queries';

const base = 'http://localhost:8080';

interface Call {
	method: string;
	path: string;
	search: string;
	headers: Headers;
	body: unknown;
}

// A scripted manager: each handler answers a "METHOD /path" (the first
// matching prefix wins); every request is recorded.
function fakeManager(handlers: Record<string, (c: Call) => [number, unknown]>) {
	const calls: Call[] = [];
	const impl = async (input: RequestInfo | URL, init?: RequestInit) => {
		const req =
			input instanceof Request ? input : new Request(new URL(String(input), base), init);
		const url = new URL(req.url);
		const text = await req.text();
		const call: Call = {
			method: req.method,
			path: url.pathname,
			search: decodeURIComponent(url.search),
			headers: req.headers,
			body: text ? JSON.parse(text) : undefined
		};
		calls.push(call);
		const key = Object.keys(handlers).find((k) => `${call.method} ${call.path}`.startsWith(k));
		const [status, body] = key
			? handlers[key](call)
			: [
					404,
					{
						code: 'not_found',
						message: 'no',
						details: [],
						requestId: 'r',
						retryable: false
					}
				];
		return status === 204
			? new Response(null, { status })
			: new Response(JSON.stringify(body), {
					status,
					headers: { 'Content-Type': 'application/json' }
				});
	};
	return { client: createApiClient(impl as typeof fetch, base), calls };
}

const job = (id: string, state = 'queued') => ({
	id,
	state,
	kind: 'stack.deploy',
	items: [],
	targets: []
});

describe('stack actions', () => {
	it('deploys, pulls first or rebuilds, each with its own idempotency key', async () => {
		const m = fakeManager({ 'POST /api/v1/stacks/st-1/deployments': () => [202, job('j1')] });
		await deployStack('st-1', 'deploy', m.client);
		await deployStack('st-1', 'pull', m.client);
		await deployStack('st-1', 'build', m.client);
		expect(m.calls.map((c) => c.body)).toEqual([{}, { pull: 'always' }, { build: true }]);
		const keys = m.calls.map((c) => c.headers.get('Idempotency-Key'));
		expect(new Set(keys).size).toBe(3);
		expect(keys.every((k) => k && k.length > 10)).toBe(true);
	});

	it('operates on the whole stack or named services', async () => {
		const m = fakeManager({ 'POST /api/v1/stacks/st-1/operations': () => [202, job('j2')] });
		await operateStack('st-1', 'restart', undefined, m.client);
		await operateStack('st-1', 'stop', ['db'], m.client);
		expect(m.calls.map((c) => c.body)).toEqual([
			{ action: 'restart' },
			{ action: 'stop', services: ['db'] }
		]);
	});

	it('edits display metadata with If-Match of the loaded revision', async () => {
		const m = fakeManager({
			'PATCH /api/v1/stacks/st-1': () => [200, { id: 'st-1', revision: 5 }]
		});
		const out = await patchStack(
			{ id: 'st-1', revision: 4 },
			{ description: 'Personal cloud' },
			m.client
		);
		expect(out.revision).toBe(5);
		expect(m.calls[0].headers.get('If-Match')).toBe('"4"');
		expect(etag(undefined)).toBe('"0"');
	});

	it('refuses a stale edit with the API error (412)', async () => {
		const m = fakeManager({
			'PATCH /api/v1/stacks/st-1': () => [
				412,
				{
					code: 'precondition_failed',
					message: 'stale',
					details: [],
					requestId: 'r',
					retryable: false
				}
			]
		});
		await expect(patchStack({ id: 'st-1', revision: 1 }, {}, m.client)).rejects.toMatchObject({
			status: 412
		});
	});

	it('previews a rename, then renames with If-Match and an idempotency key', async () => {
		const m = fakeManager({
			'POST /api/v1/stacks/st-1/rename-previews': () => [
				200,
				{
					from: 'shop',
					to: 'store',
					fromDir: 'shop',
					toDir: 'store',
					running: [],
					volumes: [],
					containers: [],
					blockers: [],
					warnings: []
				}
			],
			'POST /api/v1/stacks/st-1/renames': () => [202, job('j4')]
		});
		const p = await previewRename('st-1', 'store', m.client);
		expect(p.toDir).toBe('store');
		expect(m.calls[0].headers.get('Idempotency-Key')).toBeNull();
		const j = await renameStack({ id: 'st-1', revision: 7 }, 'store', m.client);
		expect(j.id).toBe('j4');
		expect(m.calls.map((c) => c.body)).toEqual([{ name: 'store' }, { name: 'store' }]);
		expect(m.calls[1].headers.get('If-Match')).toBe('"7"');
		expect(m.calls[1].headers.get('Idempotency-Key')).toBeTruthy();
	});

	it("validates an existing stack's files on disk by its ID", async () => {
		const m = fakeManager({
			'POST /api/v1/stacks/st-1/validations': () => [
				200,
				{ valid: true, errors: [], warnings: [], services: [], binds: [] }
			]
		});
		const v = await validateStackFiles('st-1', m.client);
		expect(v.valid).toBe(true);
		expect(m.calls).toHaveLength(1);
		expect(m.calls[0].body).toBeUndefined();
	});

	it('runs exactly the previewed update', async () => {
		const m = fakeManager({
			'POST /api/v1/update-policies/p1/runs': () => [202, job('j3')]
		});
		await runUpdate('p1', 'fp-1', undefined, m.client);
		expect(m.calls[0].body).toEqual({ previewFingerprint: 'fp-1' });
	});
});

describe('restartSource', () => {
	it('starts the stopped source containers in dependency order, one job at a time', async () => {
		const polls: Record<string, number> = {};
		const m = fakeManager({
			'GET /api/v1/environments/e1/containers': () => [
				200,
				{
					items: [
						{
							id: 'c-web',
							name: 'silo-web-1',
							state: 'exited',
							actions: ['container.start'],
							stack: { project: 'silo', service: 'web' }
						},
						{
							id: 'c-db',
							name: 'silo-db-1',
							state: 'exited',
							actions: ['container.start'],
							stack: { project: 'silo', service: 'db' }
						},
						{
							id: 'c-api',
							name: 'silo-api-1',
							state: 'running',
							actions: ['container.start'],
							stack: { project: 'silo', service: 'api' }
						},
						{
							id: 'c-x',
							name: 'silo-x-1',
							state: 'exited',
							actions: [],
							stack: { project: 'silo', service: 'x' }
						}
					]
				}
			],
			'POST /api/v1/environments/e1/containers/c-db/start': () => [202, job('j-db')],
			'POST /api/v1/environments/e1/containers/c-web/start': () => [202, job('j-web')],
			'GET /api/v1/jobs/j-db': () => {
				polls['j-db'] = (polls['j-db'] ?? 0) + 1;
				return [200, job('j-db', polls['j-db'] < 2 ? 'running' : 'succeeded')];
			},
			'GET /api/v1/jobs/j-web': () => [
				200,
				{
					...job('j-web', 'failed'),
					error: {
						class: 'step_failed',
						message: 'port is already allocated',
						recovery: ''
					}
				}
			]
		});
		const waits: number[] = [];
		const out = await restartSource(
			'e1',
			'silo',
			['db', 'api', 'web'],
			m.client,
			async (ms) => {
				waits.push(ms);
			}
		);
		// Filtered by project; running and not-startable containers skipped.
		expect(m.calls[0].search).toContain('stack=silo');
		const starts = m.calls.filter((c) => c.method === 'POST').map((c) => c.path);
		expect(starts).toEqual([
			'/api/v1/environments/e1/containers/c-db/start',
			'/api/v1/environments/e1/containers/c-web/start'
		]);
		// db's job finished before web started.
		const order = m.calls.map((c) => `${c.method} ${c.path}`);
		expect(order.indexOf('GET /api/v1/jobs/j-db')).toBeLessThan(
			order.indexOf('POST /api/v1/environments/e1/containers/c-web/start')
		);
		expect(out).toEqual({
			started: ['silo-db-1'],
			failed: [{ container: 'silo-web-1', message: 'port is already allocated' }]
		});
		expect(waits.every((w) => w === 1000)).toBe(true);
	});

	it('reports a start that is still running after the polls', async () => {
		const m = fakeManager({
			'GET /api/v1/environments/e1/containers': () => [
				200,
				{
					items: [
						{ id: 'c1', name: 'a-1', state: 'exited', actions: ['container.start'] }
					]
				}
			],
			'POST /api/v1/environments/e1/containers/c1/start': () => [202, job('j1')],
			'GET /api/v1/jobs/j1': () => [200, job('j1', 'running')]
		});
		const out = await restartSource('e1', 'a', [], m.client, async () => {}, 2);
		expect(out.failed[0].message).toMatch(/still running/);
	});
});

describe('stack queries', () => {
	it('asks for the three metric series comma-separated, as the API reads them', async () => {
		const m = fakeManager({
			'GET /api/v1/environments/e1/containers/': (c) => [
				200,
				{ container: c.path.split('/')[6], timestamps: [], series: [] }
			]
		});
		const q = stackMetricsQuery(
			'e1',
			['silo-web-1', 'silo-db-1'],
			m.client,
			() => new Date('2026-09-25T12:00:00Z')
		);
		const out = await new QueryClient().fetchQuery(q);
		expect(out.map((x) => x.container)).toEqual(['silo-web-1', 'silo-db-1']);
		expect(m.calls[0].search).toContain(
			'series=cpu.percent,memory.used_bytes,memory.limit_bytes'
		);
		expect(m.calls[0].search).toContain('from=2026-09-25T11:00:00.000Z');
		expect(q.queryKey).toEqual([
			'metrics',
			'item',
			'e1',
			'stack-containers',
			'silo-web-1,silo-db-1'
		]);
	});

	it('keys stack data by the live conventions', () => {
		expect(stackKeys.detail('st-1')).toEqual(['stacks', 'item', 'st-1']);
		expect(stackKeys.revisions('st-1')).toEqual(['stacks', 'item', 'st-1', 'revisions']);
		expect(stackKeys.services('st-1')).toEqual(['stacks', 'services', 'st-1']);
		expect(stackKeys.list(null)).toEqual(['stacks', 'list', '']);
		expect(stackKeys.jobs('st-1')).toEqual(['jobs', 'list', 'stack:st-1']);
		expect(stackKeys.updatePolicy('p1')).toEqual(['policies', 'item', 'p1']);
	});
});
