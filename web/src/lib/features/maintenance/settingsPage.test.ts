// The Maintenance page's running prunes (docs/internal/web.md, "Job progress
// after reload"): every environment's running prune job of maintenance has
// its own progress bar, found again in the running list after a reload,
// and Run Now shows one bar per job it started at once. Its preview shows
// each environment, also one that could not answer.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import MaintenancePage from '../../../routes/(app)/maintenance/+page.svelte';

const nav = vi.hoisted(() => ({
	page: {
		params: {} as Record<string, string>,
		url: new URL('http://localhost/maintenance')
	},
	goto: vi.fn()
}));
vi.mock('$app/state', () => ({ page: nav.page }));
vi.mock('$app/navigation', () => ({ goto: nav.goto, beforeNavigate: vi.fn() }));

function job(id: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'prune.run',
		state: 'running',
		origin: 'scheduled',
		executor: 'agent',
		environmentId: 'env-1',
		policyId: 'pol-1',
		targets: [],
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

const settings = {
	id: 'pol-1',
	enabled: true,
	schedule: {
		cron: '0 3 * * 0',
		timeZone: 'UTC',
		enabled: true,
		catchUp: 'skip',
		recentRuns: []
	},
	rules: [{ category: 'dangling_images', enabled: true, minAgeHours: 24 }],
	suggestedRules: [],
	categories: [],
	excludeEnvironments: [],
	actions: ['maintenance.run', 'maintenance.preview'],
	revision: 2,
	updatedAt: '2026-09-28T10:00:00Z'
};

const environments = [
	{ id: 'env-1', name: 'Silo', online: true, status: 'active' },
	{ id: 'env-2', name: 'Rack', online: true, status: 'active' }
];

let running: Job[] = [];
let details: Record<string, Job> = {};
let started: Job[] = [];

beforeEach(() => {
	running = [];
	details = {};
	started = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname === '/api/v1/maintenance-settings') return json(200, settings);
		if (url.pathname === '/api/v1/maintenance-settings/runs')
			return json(200, { jobs: started });
		if (url.pathname === '/api/v1/maintenance-settings/previews')
			return json(200, {
				items: [
					{
						environmentId: 'env-1',
						preview: {
							environmentId: 'env-1',
							at: '2026-09-28T10:00:00Z',
							remove: 0,
							bytes: 0,
							categories: [],
							notes: []
						}
					},
					{
						environmentId: 'env-2',
						errorClass: 'environment_offline',
						errorMessage: 'the environment is offline'
					}
				]
			});
		if (url.pathname === '/api/v1/environments')
			return json(200, { items: environments, total: environments.length });
		if (url.pathname === '/api/v1/jobs') {
			// The running list asks for the unfinished states; the recent
			// runs query does not.
			const items = url.searchParams.has('state') ? running : [];
			return json(200, { items, total: items.length });
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

function openPage() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: MaintenancePage as unknown as Component<Record<string, unknown>>,
			props: {}
		}
	});
}

describe('Maintenance page', () => {
	it("shows a bar for every environment's running prune of maintenance after a reload", async () => {
		running = [
			job('0190-3', { policyId: undefined }),
			job('0190-2', { environmentId: 'env-2' }),
			job('0190-1')
		];
		details = Object.fromEntries(running.map((j) => [j.id, j]));
		openPage();

		expect(
			await screen.findByRole('progressbar', { name: 'Prune Silo progress' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Prune Rack progress' })
		).toBeInTheDocument();
		// A one-off prune on Silo is not maintenance's.
		expect(screen.getAllByRole('progressbar')).toHaveLength(2);
		expect(screen.getByRole('region', { name: 'Running Prunes' })).toBeInTheDocument();
	});

	it('shows one bar per environment job Run Now started', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		started = [
			job('0190-5', { state: 'queued', origin: 'manual' }),
			job('0190-6', { state: 'queued', origin: 'manual', environmentId: 'env-2' })
		];
		details = Object.fromEntries(started.map((j) => [j.id, j]));
		openPage();

		await user.click(await screen.findByRole('button', { name: 'Run Now' }));
		await user.click(await screen.findByRole('button', { name: 'Run Maintenance' }));

		await waitFor(() => expect(screen.getAllByRole('progressbar')).toHaveLength(2));
		expect(
			screen.getByRole('progressbar', { name: 'Prune Silo progress' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Prune Rack progress' })
		).toBeInTheDocument();
	});

	it('previews every environment and says which one could not answer', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		openPage();

		await user.click(await screen.findByRole('button', { name: 'Preview' }));

		const rack = await screen.findByRole('region', { name: 'Rack' });
		expect(rack).toHaveTextContent('the environment is offline');
		expect(screen.getByRole('region', { name: 'Silo' })).not.toHaveTextContent(
			'the environment is offline'
		);
	});
});
