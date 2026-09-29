// The stack page's jobs after a reload (docs/internal/web.md, "Job progress
// after reload"): the layout's tray adopts the stack's running jobs from
// the running list, so a deploy started by Create stack (the ?job= handoff,
// dropped from the URL) still shows after the page is reloaded. A job
// leaves the tray when it ends; a toast reports the outcome.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { createRawSnippet, type Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import StackLayout from '../../../routes/(app)/stacks/[stackId]/+layout.svelte';
import { toast } from '$lib/ui';
import { stackJobCopy } from './adopt';
import JobTrayView from './JobTrayView.svelte';
import type { Stack } from './queries';
import { JobTray } from './tray.svelte';

const nav = vi.hoisted(() => ({
	page: { params: { stackId: 'st-1' }, url: new URL('http://localhost/stacks/st-1') },
	goto: vi.fn()
}));
vi.mock('$app/state', () => ({ page: nav.page }));
vi.mock('$app/navigation', () => ({ goto: nav.goto, beforeNavigate: vi.fn() }));

function job(id: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'stack.deploy',
		state: 'running',
		origin: 'manual',
		executor: 'agent',
		environmentId: 'env-1',
		targets: [{ type: 'stack', id: 'st-1' }],
		attempt: 1,
		progress: { percent: 30 },
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

const stack = {
	id: 'st-1',
	environmentId: 'env-1',
	name: 'silo',
	displayName: 'Silo',
	description: 'Personal cloud',
	status: 'deployed',
	view: 'full',
	actions: ['stack.read', 'stack.deploy'],
	revision: 3,
	createdAt: '2026-09-04T10:00:00Z',
	environmentOnline: true,
	location: {
		root: 'stacks',
		dir: 'silo',
		hostPath: '/var/lib/docker/volumes/docker-manager_stacks/_data/silo'
	},
	services: [],
	engine: { state: 'running', services: [] }
} as unknown as Stack;

let running: Job[] = [];
let details: Record<string, Job> = {};

beforeEach(() => {
	running = [];
	details = {};
	nav.goto.mockClear();
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname === '/api/v1/stacks/st-1') return json(200, stack);
		if (url.pathname === '/api/v1/jobs')
			return json(200, { items: running, total: running.length });
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

function openStackPage(href: string) {
	nav.page.url = new URL(href);
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: StackLayout as unknown as Component<Record<string, unknown>>,
			props: { children: createRawSnippet(() => ({ render: () => '<p>Overview</p>' })) }
		}
	});
}

describe('stack page tray after a reload', () => {
	it("shows the stack's running jobs, but not its update checks", async () => {
		running = [
			job('0190-2', { kind: 'update.check' }),
			job('0190-1', { kind: 'stack.deploy' })
		];
		details = { '0190-1': running[1] };
		openStackPage('http://localhost/stacks/st-1');

		const tray = await screen.findByRole('region', { name: 'Jobs started here' });
		await waitFor(() => expect(tray).toHaveTextContent('Deploy Silo'));
		expect(tray).not.toHaveTextContent('Check for updates');
		expect(screen.getAllByRole('progressbar')).toHaveLength(1);
	});

	it('keeps the deploy Create stack handed over when the page is reloaded', async () => {
		details = { 'job-new': job('job-new', { state: 'queued' }) };
		// Arriving from Create stack: the job is in the URL, not yet listed.
		openStackPage('http://localhost/stacks/st-1?job=job-new&kind=deploy');
		const tray = await screen.findByRole('region', { name: 'Jobs started here' });
		await waitFor(() => expect(tray).toHaveTextContent('Deploy Silo'));
		expect(nav.goto).toHaveBeenCalledWith('/stacks/st-1', expect.anything());
		cleanup();

		// Reloaded: the URL no longer names it; the running list does.
		running = [job('job-new')];
		openStackPage('http://localhost/stacks/st-1');
		const again = await screen.findByRole('region', { name: 'Jobs started here' });
		await waitFor(() => expect(again).toHaveTextContent('Deploy Silo'));
	});
});

describe('JobTray.adopt', () => {
	const describeJob = (j: Job) => ({ kind: j.kind, ...stackJobCopy(j.kind, 'Silo') });

	it('adds running jobs once, oldest first, and never brings back a dismissed one', () => {
		const tray = new JobTray();
		const list = [job('0190-2', { kind: 'stack.rename' }), job('0190-1')];
		tray.adopt(list, describeJob);
		expect(tray.jobs.map((t) => t.title)).toEqual(['Rename Silo', 'Deploy Silo']);
		expect(tray.running('stack.rename')).toBe(true);
		tray.adopt(list, describeJob);
		expect(tray.jobs).toHaveLength(2);

		tray.markFinished('0190-1');
		tray.dismiss('0190-1');
		tray.adopt(list, describeJob);
		expect(tray.jobs.map((t) => t.id)).toEqual(['0190-2']);

		tray.reset();
		expect(tray.jobs).toEqual([]);
		tray.adopt(list, describeJob);
		expect(tray.jobs).toHaveLength(2);
	});

	it('keeps the copy of the action that started a job', () => {
		const tray = new JobTray();
		tray.add(
			{ id: '0190-1' },
			{ kind: 'stack.deploy', title: 'Pull & deploy Silo', success: 'x', failure: 'y' }
		);
		tray.adopt([job('0190-1')], describeJob);
		expect(tray.jobs.map((t) => t.title)).toEqual(['Pull & deploy Silo']);
	});
});

describe('JobTrayView', () => {
	afterEach(() => toast.clear());

	function showTray(tray: JobTray, list: Job[] = []) {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(QueryHarness, {
			props: {
				client,
				component: JobTrayView as unknown as Component<Record<string, unknown>>,
				props: { tray, running: list }
			}
		});
	}

	it('reports an ended deploy as a toast and leaves no panel behind', async () => {
		details = { '0190-1': job('0190-1', { state: 'succeeded', progress: { percent: 100 } }) };
		const tray = new JobTray();
		tray.add(
			{ id: '0190-1' },
			{
				kind: 'stack.deploy',
				title: 'Deploy Silo',
				success: 'Deployed Silo',
				failure: 'Silo was not deployed'
			}
		);
		showTray(tray);
		await waitFor(() => expect(toast.items.map((t) => t.title)).toEqual(['Deployed Silo']));
		expect(tray.jobs).toEqual([]);
		expect(screen.queryByRole('region', { name: 'Jobs started here' })).toBeNull();
	});

	it('reports a failed deploy as a toast with a link to the job', async () => {
		details = {
			'0190-1': job('0190-1', {
				state: 'failed',
				error: {
					class: 'compose_failed',
					message: 'pull access denied',
					recovery: 'Check the image name and the registry credentials.'
				}
			})
		};
		const tray = new JobTray();
		tray.add(
			{ id: '0190-1' },
			{
				kind: 'stack.deploy',
				title: 'Deploy Silo',
				success: 'Deployed Silo',
				failure: 'Silo was not deployed'
			}
		);
		showTray(tray);
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toEqual(['Silo was not deployed'])
		);
		expect(toast.items[0].action?.label).toBe('Open job');
		expect(tray.jobs).toEqual([]);
		expect(screen.queryByRole('button', { name: 'Dismiss Deploy Silo' })).toBeNull();
	});

	it('follows at most three running jobs over their own stream', async () => {
		const list = ['4', '3', '2', '1'].map((n) => job(`0190-${n}`));
		details = Object.fromEntries(list.map((j) => [j.id, j]));
		const tray = new JobTray();
		tray.adopt(list, (j) => ({ kind: j.kind, ...stackJobCopy(j.kind, `Silo ${j.id}`) }));
		showTray(tray, list);
		expect(screen.getAllByRole('progressbar')).toHaveLength(4);
		// The oldest is a compact row: a link to its job, no stream.
		expect(screen.getAllByRole('link').map((a) => a.getAttribute('href'))).toEqual([
			'/jobs/0190-1'
		]);
	});
});
