// The container page after a reload (docs/internal/web.md, "Job progress
// after reload"): a job running on the container comes back from the
// running list, and jobs of other containers stay off the page.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { createRawSnippet } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import type { Job } from '$lib/api/client';
import type { Container } from '$lib/api/queries';
import ContainerLayout from '../../../routes/(app)/containers/[environmentId]/[containerId]/+layout.svelte';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));
vi.mock('$app/state', () => ({
	page: {
		params: { environmentId: 'e1', containerId: 'web' },
		url: new URL('http://localhost/containers/e1/web')
	}
}));

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

const web: Container = {
	id: 'c1',
	name: 'web',
	environmentId: 'e1',
	image: 'nginx:latest',
	state: 'running',
	view: 'full',
	actions: ['container.start', 'container.stop', 'container.restart']
} as Container;

function job(id: string, container: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'container.restart',
		state: 'running',
		origin: 'manual',
		executor: 'agent',
		environmentId: 'e1',
		targets: [{ type: 'container', id: container }],
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

/** Serves the container, the running list and each job (JobWatcher polls in jsdom). */
function stub(running: Job[]) {
	const fetched: string[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request | string) => {
			const req = input instanceof Request ? input : new Request(input);
			const url = new URL(req.url);
			fetched.push(url.pathname + url.search);
			if (url.pathname === '/api/v1/environments/e1/containers/web') return json(web);
			if (url.pathname === '/api/v1/jobs')
				return json({ items: running, total: running.length });
			if (url.pathname.startsWith('/api/v1/jobs/')) {
				const id = decodeURIComponent(url.pathname.split('/').pop() ?? '');
				const j = running.find((x) => x.id === id);
				if (j) return json(j);
			}
			return json({ code: 'not_found', message: 'not found' }, 404);
		})
	);
	return fetched;
}

function mount() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const children = createRawSnippet(() => ({ render: () => '<p>Overview tab</p>' }));
	render(QueryHarness, {
		props: { client, component: ContainerLayout as never, props: { children } }
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('container page', () => {
	it('shows the running job of the container again after a reload', async () => {
		const fetched = stub([job('0190-1', 'web'), job('0190-2', 'db')]);
		mount();

		expect(await screen.findByText('Overview tab')).toBeInTheDocument();
		const region = await screen.findByRole('region', { name: 'Running Jobs of web' });
		expect(region).toHaveTextContent('Restart Container web');
		expect(
			screen.getByRole('progressbar', { name: 'Restart Container web progress' })
		).toBeInTheDocument();
		// The other container's job is not shown here.
		expect(screen.queryByText('Restart Container db')).not.toBeInTheDocument();
		const list = fetched.find((u) => u.startsWith('/api/v1/jobs?')) ?? '';
		expect(decodeURIComponent(list)).toContain(
			'state=queued,blocked,dispatched,running,cancelling'
		);
		// The page follows the job itself (polling in jsdom).
		await waitFor(() => expect(fetched).toContain('/api/v1/jobs/0190-1'));
	});

	it('shows no job progress when nothing runs on the container', async () => {
		const fetched = stub([job('0190-2', 'db')]);
		mount();

		expect(await screen.findByText('Overview tab')).toBeInTheDocument();
		await waitFor(() => expect(fetched.some((u) => u.startsWith('/api/v1/jobs?'))).toBe(true));
		await new Promise((r) => setTimeout(r, 0));
		expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
		expect(
			screen.queryByRole('region', { name: 'Running Jobs of web' })
		).not.toBeInTheDocument();
	});
});
