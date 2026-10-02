// The file manager's operations panel (#15, docs/internal/web.md "Job
// progress after reload"): a running file job of the root comes back from
// the running jobs list (after a reload, or when the user returns to the
// Files tab), Cancel works for it, and its end is announced once.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import FileOperationsHarness from '../../../test/FileOperationsHarness.svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';

function job(id: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'files.copy',
		state: 'running',
		origin: 'manual',
		executor: 'agent',
		environmentId: 'e1',
		targets: [
			{ type: 'stack', id: 'st-1' },
			{ type: 'path', id: '/stack/st-1/config' },
			{ type: 'destination_path', id: '/stack/st-1/backup' }
		],
		attempt: 1,
		progress: { percent: 40 },
		items: [],
		locks: [],
		locksHeld: true,
		cancelRequested: false,
		cancellable: true,
		retryable: false,
		createdAt: '2026-09-28T10:00:00Z',
		updatedAt: '2026-09-28T10:00:00Z',
		...p
	} as Job;
}

let running: Job[] = [];
let details: Record<string, Job> = {};
let cancelled: string[] = [];

beforeEach(() => {
	running = [];
	details = {};
	cancelled = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname === '/api/v1/jobs')
			return json(200, { items: running, total: running.length });
		const cancel = url.pathname.match(/^\/api\/v1\/jobs\/([^/]+)\/cancellations$/)?.[1];
		if (cancel && req.method === 'POST') {
			cancelled.push(cancel);
			return json(202, { ...details[cancel], state: 'cancelling', cancelRequested: true });
		}
		const id = url.pathname.match(/^\/api\/v1\/jobs\/([^/]+)$/)?.[1];
		if (id && details[id]) return json(200, details[id]);
		return json(404, {
			code: 'not_found',
			message: 'no',
			details: [],
			requestId: 'r',
			retryable: false
		});
	});
});
afterEach(() => vi.unstubAllGlobals());

function mount(onfinish = vi.fn()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: FileOperationsHarness as unknown as Component<Record<string, unknown>>,
			props: {
				scope: { kind: 'stack', stackId: 'st-1', environmentId: 'e1' },
				rootLabel: 'silo',
				onfinish
			}
		}
	});
	return { client, onfinish };
}

describe('OperationsPanel', () => {
	it("restores the root's running file job from the running list and cancels it", async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		running = [
			job('0190-1'),
			// Another stack's copy and a deploy of this stack are not shown.
			job('0190-2', { targets: [{ type: 'stack', id: 'st-2' }] }),
			job('0190-3', { kind: 'stack.deploy', targets: [{ type: 'stack', id: 'st-1' }] })
		];
		details = Object.fromEntries(running.map((j) => [j.id, j]));
		mount();

		expect(
			await screen.findByRole('progressbar', { name: 'Copy Files in config progress' })
		).toBeInTheDocument();
		expect(screen.getByRole('region', { name: 'File Operations' })).toBeInTheDocument();
		expect(screen.getAllByRole('progressbar')).toHaveLength(1);

		await user.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(cancelled).toEqual(['0190-1']));
		expect(await screen.findByText('Cancelling: Copy Files in config')).toBeInTheDocument();
	});

	it('announces the end of a restored job once and keeps it until dismissed', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		running = [job('0190-1')];
		details = { '0190-1': job('0190-1', { state: 'succeeded', progress: { percent: 100 } }) };
		const { onfinish } = mount();

		const dismiss = await screen.findByRole('button', { name: 'Dismiss Copy Files in config' });
		expect(onfinish).toHaveBeenCalledOnce();
		expect(onfinish).toHaveBeenCalledWith(
			expect.objectContaining({ id: '0190-1', state: 'succeeded' }),
			'Copy Files in config'
		);
		expect(screen.queryByRole('button', { name: 'Cancel' })).not.toBeInTheDocument();

		await user.click(dismiss);
		await waitFor(() =>
			expect(
				screen.queryByRole('region', { name: 'File Operations' })
			).not.toBeInTheDocument()
		);
	});
});
