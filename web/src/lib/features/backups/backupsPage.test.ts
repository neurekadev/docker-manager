// The Backups page (#10, #246): the running backups and retentions come
// back from GET /backup-activity as one steady line each in "Running
// Now", the recent runs show the set being written with its progress,
// Back Up Now waits, and the settings name the Primary and Secondary
// repositories. Without a Primary, backups are paused.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import BackupsPage from '../../../routes/(app)/backups/+page.svelte';

const nav = vi.hoisted(() => ({
	page: {
		params: {} as Record<string, string>,
		url: new URL('http://localhost/backups')
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
		policyId: 'bs-1',
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

let settings: Record<string, unknown>;

function baseSettings(): Record<string, unknown> {
	return {
		id: 'bs-1',
		enabled: true,
		primaryRepositoryId: 'repo-1',
		secondaryRepositoryId: 'repo-2',
		schedule: { cron: '0 2 * * *', timeZone: 'UTC', nextRun: '2026-09-29T02:00:00Z' },
		excludeEnvironments: [],
		excludeStacks: [],
		excludeVolumes: [],
		anonymousVolumes: false,
		buildxVolumes: false,
		externalBinds: false,
		includeMetrics: false,
		shutdown: false,
		retention: { daily: 7 },
		revision: 3,
		updatedAt: '2026-09-28T09:00:00Z',
		actions: ['backup_policy.manage', 'backup.run', 'backup.retention'],
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
						repositoryId: 'repo-1',
						state: 'pending',
						environmentId: 'env-1'
					},
					{
						item: 'stack:st-1',
						kind: 'stack',
						scope: 'env:env-1',
						repositoryId: 'repo-2',
						state: 'pending',
						environmentId: 'env-1'
					}
				]
			}
		]
	};
}

const repositories = [
	{ id: 'repo-1', name: 'B2', state: 'ready', role: 'primary', view: 'full', actions: [] },
	{ id: 'repo-2', name: 'NAS', state: 'ready', role: 'secondary', view: 'full', actions: [] }
];

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
		policyId: 'bs-1',
		environmentId: 'env-1',
		itemCount: 1,
		stacks: 1,
		volumes: 0,
		cancellable: false,
		percent: 40
	},
	{
		jobId: '0190-2',
		kind: 'backup.retention',
		state: 'running',
		setId: '',
		policyId: 'bs-1',
		environmentId: 'env-2',
		itemCount: 1,
		stacks: 0,
		volumes: 0,
		cancellable: true,
		percent: 40,
		message: 'freeing the space of the removed backups'
	}
];

let running: Job[] = [];

beforeEach(() => {
	running = [];
	settings = baseSettings();
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname === '/api/v1/backup-settings') return json(200, settings);
		if (url.pathname === '/api/v1/backup-repositories')
			return json(200, { items: repositories, total: repositories.length });
		if (url.pathname === '/api/v1/backup-activity') return json(200, { jobs: activity });
		if (url.pathname === '/api/v1/environments')
			return json(200, { items: environments, total: environments.length });
		if (url.pathname === '/api/v1/backups') return json(200, { items: [], total: 0 });
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
			component: BackupsPage as unknown as Component<Record<string, unknown>>,
			props: {}
		}
	});
}

describe('Backups page', () => {
	it('restores the running backup and retention lines and the running set after a reload', async () => {
		running = [
			job('0190-2', { kind: 'backup.retention', environmentId: 'env-2' }),
			job('0190-1')
		];
		openPage();

		expect(
			await screen.findByRole('progressbar', { name: 'Backup Progress of Backups, Silo' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Retention Progress of Backups, Rack' })
		).toBeInTheDocument();
		expect(screen.getByText('Freeing the space of the removed backups')).toBeInTheDocument();
		// The retention can be cancelled from its line.
		expect(screen.getByRole('button', { name: 'Cancel Backups, Rack' })).toBeInTheDocument();

		// The recent runs show the set being written.
		expect(
			await screen.findByRole('progressbar', { name: /^Progress of the Set Started/ })
		).toHaveAttribute('aria-valuenow', '40');
		expect(screen.getByRole('button', { name: 'Backing Up…' })).toBeDisabled();
	});

	it('names where backups go', async () => {
		openPage();
		expect(
			await screen.findByText(/Backs up every environment to B2, then to NAS/)
		).toBeInTheDocument();
		expect(screen.getByText('Primary Repository')).toBeInTheDocument();
		expect(screen.getByText('Secondary Repository')).toBeInTheDocument();
	});

	it('says backups are paused without a Primary repository', async () => {
		settings = { ...baseSettings(), primaryRepositoryId: '', secondaryRepositoryId: '' };
		openPage();
		expect(await screen.findByText('Backups Are Paused')).toBeInTheDocument();
		expect(screen.getByText('Choose the Primary Repository')).toBeInTheDocument();
	});
});
