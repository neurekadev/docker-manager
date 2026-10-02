// The migration wizard after a reload (docs/internal/web.md, "Job progress
// after reload"): a migration of the stack that is still running opens
// the wizard at the move step on its progress, not at the destination.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import MigrationWizard from './MigrationWizard.svelte';
import type { Stack } from './queries';
import { JobTray } from './tray.svelte';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const stack = {
	id: 'st-1',
	environmentId: 'env-1',
	name: 'silo',
	displayName: 'Silo',
	status: 'deployed',
	view: 'full',
	actions: ['stack.migrate'],
	revision: 3,
	createdAt: '2026-09-04T10:00:00Z',
	environmentOnline: true,
	services: [{ name: 'web', image: 'nginx', build: false, dependsOn: [] }]
} as unknown as Stack;

function migration(p: Partial<Job> = {}): Job {
	return {
		id: 'job-m',
		kind: 'stack.migrate',
		state: 'running',
		origin: 'manual',
		executor: 'manager',
		environmentId: 'env-1',
		targets: [
			{ type: 'stack', id: 'st-1' },
			{ type: 'volume', id: 'silo_data' },
			{ type: 'volume', id: 'silo_data', environmentId: 'env-3' }
		],
		attempt: 1,
		progress: { percent: 40, step: 'transfer' },
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

const env = (id: string, name: string) => ({
	id,
	name,
	online: true,
	status: 'active',
	view: 'full',
	actions: []
});

let running: Job[] = [];
let fetched: string[] = [];

beforeEach(() => {
	running = [];
	fetched = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		fetched.push(url.pathname + url.search);
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname === '/api/v1/environments')
			return json(200, {
				items: [env('env-1', 'nas'), env('env-2', 'lab'), env('env-3', 'cloud')]
			});
		if (url.pathname === '/api/v1/jobs')
			return json(200, { items: running, total: running.length });
		if (url.pathname === '/api/v1/jobs/job-m') return json(200, migration());
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

function wizard(tray = new JobTray()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: MigrationWizard as unknown as Component<Record<string, unknown>>,
			props: { stack, tray }
		}
	});
	return tray;
}

describe('MigrationWizard', () => {
	it('opens at the move step on the stack’s running migration', async () => {
		running = [migration()];
		const tray = wizard();

		expect(
			await screen.findByRole('progressbar', { name: 'Migrate Silo to cloud progress' })
		).toBeInTheDocument();
		expect(screen.getByRole('heading', { level: 2, name: 'Move' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Back' })).toBeNull();
		const list = fetched.find((u) => u.startsWith('/api/v1/jobs?')) ?? '';
		expect(decodeURIComponent(list)).toContain('state=queued,blocked,dispatched,running');
		// The wizard shows it: the stack's job tray does not as well.
		expect(tray.jobs).toEqual([]);
	});

	it('starts at the destination when no migration of the stack runs', async () => {
		running = [
			migration({ id: 'job-o', targets: [{ type: 'stack', id: 'st-2' }] }),
			migration({ id: 'job-d', kind: 'stack.deploy' })
		];
		wizard();

		expect(
			await screen.findByRole('heading', { level: 2, name: 'Destination' })
		).toBeInTheDocument();
		await waitFor(() => expect(fetched.some((u) => u.startsWith('/api/v1/jobs?'))).toBe(true));
		expect(screen.queryByRole('progressbar')).toBeNull();
		expect(screen.getByRole('group', { name: 'Destination Environment' })).toBeInTheDocument();
	});
});
