// ActiveJobs (docs/internal/web.md, "Job progress after reload"): running
// jobs come back from the running list, at most MAX_JOB_STREAMS follow
// their own stream (the rest are compact rows), jobs started here show at
// once, and an ended job keeps its outcome until dismissed.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import type { Job } from '$lib/api/client';
import ActiveJobs from './ActiveJobs.svelte';
import { TrackedJobs } from './tracked.svelte';

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

function job(id: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'container.restart',
		state: 'running',
		origin: 'manual',
		executor: 'agent',
		environmentId: 'e1',
		targets: [{ type: 'container', id: `web${id}` }],
		attempt: 1,
		progress: { percent: 10 },
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

/** Serves the running list (`running()`) and each job's detail (`detail`). */
function stub(running: () => Job[], detail: Record<string, Job>) {
	const fetched: string[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request | string) => {
			const req = input instanceof Request ? input : new Request(input);
			const url = new URL(req.url);
			fetched.push(url.pathname + url.search);
			if (url.pathname === '/api/v1/jobs') {
				const items = running();
				return json({ items, total: items.length });
			}
			const id = decodeURIComponent(url.pathname.split('/').pop() ?? '');
			if (detail[id]) return json(detail[id]);
			return json({ code: 'not_found', message: 'not found' }, 404);
		})
	);
	return fetched;
}

function mount(props: ComponentProps<typeof ActiveJobs>) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness<ComponentProps<typeof ActiveJobs>>, {
		props: { client, component: ActiveJobs, props }
	});
	return client;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('ActiveJobs', () => {
	it('restores running jobs from the running list and caps the per-job streams', async () => {
		const jobs = ['5', '4', '3', '2', '1'].map((n) => job(`0190-${n}`));
		const fetched = stub(() => jobs, Object.fromEntries(jobs.map((j) => [j.id, j])));
		mount({ filter: { environmentId: 'e1' }, max: 3 });

		await waitFor(() => expect(screen.getAllByRole('progressbar')).toHaveLength(5));
		const list = fetched.find((u) => u.startsWith('/api/v1/jobs?')) ?? '';
		expect(decodeURIComponent(list)).toContain(
			'state=queued,blocked,dispatched,running,cancelling'
		);
		expect(list).toContain('limit=200');
		expect(screen.getByRole('region', { name: 'Running Jobs' })).toBeInTheDocument();
		// The two oldest are compact rows (a link to the job, no stream).
		expect(screen.getAllByRole('link').map((a) => a.getAttribute('href'))).toEqual([
			'/jobs/0190-2',
			'/jobs/0190-1'
		]);
		await waitFor(() =>
			expect(fetched.filter((u) => u.startsWith('/api/v1/jobs/')).sort()).toEqual(
				expect.arrayContaining([
					'/api/v1/jobs/0190-3',
					'/api/v1/jobs/0190-4',
					'/api/v1/jobs/0190-5'
				])
			)
		);
		expect(
			fetched.some((u) => u === '/api/v1/jobs/0190-1' || u === '/api/v1/jobs/0190-2')
		).toBe(false);
		expect(screen.getByText('Restart Container web0190-5')).toBeInTheDocument();
	});

	it('keeps an ended job until it is dismissed', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		let running = [job('0190-1')];
		stub(() => running, { '0190-1': job('0190-1', { state: 'succeeded' }) });
		const onfinish = vi.fn();
		const client = mount({ filter: { environmentId: 'e1' }, onfinish });

		const dismiss = await screen.findByRole('button', {
			name: 'Dismiss Restart Container web0190-1'
		});
		expect(onfinish).toHaveBeenCalledWith(expect.objectContaining({ state: 'succeeded' }));
		// Gone from the running list: the outcome stays.
		running = [];
		await client.invalidateQueries({ queryKey: ['jobs'] });
		expect(screen.getByText('Restart Container web0190-1')).toBeInTheDocument();

		await user.click(dismiss);
		await waitFor(() =>
			expect(screen.queryByText('Restart Container web0190-1')).not.toBeInTheDocument()
		);
		expect(screen.queryByRole('region', { name: 'Running Jobs' })).not.toBeInTheDocument();
	});

	it('shows a job started here at once, with its title', async () => {
		const started = job('0190-9', { kind: 'image.pull', state: 'queued', targets: [] });
		stub(() => [], { '0190-9': started });
		const tracked = new TrackedJobs(
			() => [],
			() => ({ environmentId: 'e1' })
		);
		mount({ jobs: tracked });
		expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();

		tracked.add(started, 'Pull nginx:1.27');
		expect(await screen.findByText('Pull nginx:1.27')).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Pull nginx:1.27 progress' })
		).toBeInTheDocument();
	});
});
