// The backup policy page's running jobs (docs/internal/web.md, "Job
// progress after reload"): the policy's running backup and retention jobs
// come back from the running list with a bar each, the recent runs show
// the set being written with its progress, and Back up now waits.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import PolicyPage from '../../../routes/(app)/backups/policies/[policyId]/+page.svelte';

const nav = vi.hoisted(() => ({
	page: {
		params: { policyId: 'pol-1' } as Record<string, string>,
		url: new URL('http://localhost/backups/policies/pol-1')
	},
	goto: vi.fn()
}));
vi.mock('$app/state', () => ({ page: nav.page }));
vi.mock('$app/navigation', () => ({ goto: nav.goto, beforeNavigate: vi.fn() }));

function job(id: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'backup.run',
		state: 'running',
		origin: 'scheduled',
		executor: 'agent',
		environmentId: 'env-1',
		policyId: 'pol-1',
		targets: [{ type: 'repository', id: 'repo-1' }],
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

const policy = {
	id: 'pol-1',
	name: 'Nightly',
	scope: 'all',
	enabled: true,
	view: 'full',
	revision: 1,
	actions: ['backup.run', 'backup.retention'],
	anonymousVolumes: false,
	buildxVolumes: false,
	excludeStacks: [],
	excludeVolumes: [],
	includeManagerState: false,
	includeMetrics: false,
	shutdown: false,
	stacks: [],
	volumes: [],
	repositoryId: 'repo-1',
	recentSets: [
		{
			id: 'set-1',
			state: 'pending',
			origin: 'scheduled',
			startedAt: '2026-09-28T10:00:00Z',
			members: [
				{
					item: 'stack:st-1',
					kind: 'stack',
					scope: 'env:env-1',
					state: 'pending',
					environmentId: 'env-1'
				}
			]
		}
	]
};

const environments = [
	{ id: 'env-1', name: 'Silo', online: true, status: 'active' },
	{ id: 'env-2', name: 'Rack', online: true, status: 'active' }
];

const activity = [
	{
		jobId: '0190-1',
		kind: 'backup.run',
		state: 'running',
		setId: 'set-1',
		policyId: 'pol-1',
		environmentId: 'env-1',
		itemCount: 1,
		percent: 40
	},
	{
		jobId: '0190-9',
		kind: 'backup.run',
		state: 'running',
		setId: 'set-9',
		policyId: 'pol-9',
		environmentId: 'env-1',
		itemCount: 1,
		percent: 10
	}
];

let running: Job[] = [];

beforeEach(() => {
	running = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname === '/api/v1/backup-policies/pol-1') return json(200, policy);
		if (url.pathname === '/api/v1/backup-activity') return json(200, { jobs: activity });
		if (url.pathname === '/api/v1/environments')
			return json(200, { items: environments, total: environments.length });
		if (url.pathname === '/api/v1/jobs')
			return json(200, { items: running, total: running.length });
		const id = url.pathname.match(/^\/api\/v1\/jobs\/([^/]+)$/)?.[1];
		const detail = running.find((j) => j.id === id);
		if (detail) return json(200, detail);
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
			component: PolicyPage as unknown as Component<Record<string, unknown>>,
			props: {}
		}
	});
}

describe('backup policy page: running jobs', () => {
	it('restores the running backup and retention bars and the running set after a reload', async () => {
		running = [
			job('0190-3', { policyId: 'pol-9' }),
			job('0190-2', { kind: 'backup.retention', environmentId: 'env-2' }),
			job('0190-1')
		];
		openPage();

		expect(
			await screen.findByRole('progressbar', { name: 'Back up: Silo progress' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Apply backup retention: Rack progress' })
		).toBeInTheDocument();
		expect(screen.getByRole('region', { name: 'Running jobs of Nightly' })).toBeInTheDocument();
		// Another policy's backup has no bar here.
		expect(screen.queryAllByRole('progressbar', { name: /progress$/ })).toHaveLength(2);

		// The recent runs show the set being written, from this policy's activity.
		expect(
			await screen.findByRole('progressbar', { name: /^Progress of the set started/ })
		).toHaveAttribute('aria-valuenow', '40');
		expect(screen.getByRole('button', { name: 'Backing up…' })).toBeDisabled();
	});
});
