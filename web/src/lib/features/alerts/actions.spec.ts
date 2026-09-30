import { describe, expect, it, vi } from 'vitest';
import { QueryClient } from '@tanstack/svelte-query';
import { createApiClient } from '$lib/api/client';
import { dismissMany, dismissOne } from './actions';
import { MAX_DISMISSALS, alertKeys, alertsQuery } from './queries';
import { failingDisk } from './test/samples';

const base = 'http://localhost:8080';

interface Call {
	method: string;
	path: string;
	search: URLSearchParams;
	body: unknown;
}

// A scripted manager: each handler answers a "METHOD /path"; every request is recorded.
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
			search: url.searchParams,
			body: text ? JSON.parse(text) : undefined
		};
		calls.push(call);
		const handler = handlers[`${call.method} ${call.path}`];
		const [status, body] = handler
			? handler(call)
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
		return new Response(JSON.stringify(body), {
			status,
			headers: { 'Content-Type': 'application/json' }
		});
	};
	return { client: createApiClient(impl as typeof fetch, base), calls };
}

const toasts = () => ({ success: vi.fn(), error: vi.fn() });

describe('alert queries (#159)', () => {
	it('loads every page of the filtered list', async () => {
		const { client, calls } = fakeManager({
			'GET /api/v1/alerts': (c) =>
				c.search.get('cursor')
					? [200, { items: [{ ...failingDisk, id: 'a2' }] }]
					: [200, { items: [failingDisk], nextCursor: 'next' }]
		});
		const q = alertsQuery({ state: 'active', environmentId: 'e1' }, client);
		expect(q.queryKey).toEqual(['alerts', 'list', { state: 'active', environmentId: 'e1' }]);
		const rows = await q.queryFn!({ signal: new AbortController().signal } as never);
		expect(rows.map((a) => a.id)).toEqual(['a1', 'a2']);
		expect(calls[0].search.get('state')).toBe('active');
		expect(calls[0].search.get('environmentId')).toBe('e1');
		expect(calls[0].search.get('limit')).toBe('200');
		expect(calls[1].search.get('cursor')).toBe('next');
	});
});

describe('dismissing alerts (#159)', () => {
	it('dismisses one, says so and refreshes every alerts list', async () => {
		const { client, calls } = fakeManager({
			'POST /api/v1/alerts/a1/dismissals': () => [200, { ...failingDisk, dismissed: true }]
		});
		const queryClient = new QueryClient();
		const spy = vi.spyOn(queryClient, 'invalidateQueries');
		const toast = toasts();
		expect(await dismissOne(failingDisk, { client, queryClient, toast })).toBe(true);
		expect(calls.map((c) => [c.method, c.path, c.body])).toEqual([
			['POST', '/api/v1/alerts/a1/dismissals', undefined]
		]);
		expect(toast.success).toHaveBeenCalledWith('Dismissed Disk /dev/sda on homelab is failing');
		expect(spy).toHaveBeenCalledWith({ queryKey: alertKeys.all });
		// Quietly (the bell): no toast.
		const quiet = toasts();
		await dismissOne(failingDisk, { client, toast: quiet, quiet: true });
		expect(quiet.success).not.toHaveBeenCalled();
	});

	it('says why a dismissal failed', async () => {
		const { client } = fakeManager({
			'POST /api/v1/alerts/a1/dismissals': () => [
				409,
				{
					code: 'alert_not_firing',
					message: 'The alert is resolved.',
					details: [],
					requestId: 'r',
					retryable: false
				}
			]
		});
		const toast = toasts();
		expect(await dismissOne(failingDisk, { client, toast })).toBe(false);
		expect(toast.error).toHaveBeenCalledWith(
			'Couldn’t dismiss Disk /dev/sda on homelab is failing',
			{ body: 'It was resolved in the meantime, so there is nothing to dismiss.' }
		);
	});

	it('dismisses several in one request and never sends an empty list', async () => {
		const { client, calls } = fakeManager({
			'POST /api/v1/alerts/dismissals': (c) => [
				200,
				{
					dismissed: (c.body as { alertIds: string[] }).alertIds.length,
					alertIds: (c.body as { alertIds: string[] }).alertIds
				}
			]
		});
		const toast = toasts();
		expect(await dismissMany(['a1', 'a2', 'a3'], { client, toast })).toBe(3);
		expect(calls.map((c) => c.body)).toEqual([{ alertIds: ['a1', 'a2', 'a3'] }]);
		expect(toast.success).toHaveBeenCalledWith('Dismissed 3 alerts');
		// Without IDs the manager would dismiss every active alert: nothing is sent.
		expect(await dismissMany([], { client, toast })).toBe(0);
		expect(calls).toHaveLength(1);
		// More than one request takes: split.
		const many = Array.from({ length: MAX_DISMISSALS + 1 }, (_, i) => `a${i}`);
		expect(await dismissMany(many, { client, toast, quiet: true })).toBe(MAX_DISMISSALS + 1);
		expect(
			calls.slice(1).map((c) => (c.body as { alertIds: string[] }).alertIds.length)
		).toEqual([MAX_DISMISSALS, 1]);
	});

	it('throws for the confirmation dialog, or reports the failure itself', async () => {
		const { client } = fakeManager({});
		await expect(dismissMany(['a1'], { client, toast: toasts() })).rejects.toThrow();
		const toast = toasts();
		expect(await dismissMany(['a1'], { client, toast, report: true })).toBeNull();
		expect(toast.error).toHaveBeenCalledWith('Couldn’t dismiss the alerts', {
			body: 'It is gone, or you can no longer see it.'
		});
	});
});
