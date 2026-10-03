// The Updates page (#20, #240, docs/internal/web.md "Job progress after
// reload"): running checks and updates come back from the running list
// after a reload (Check Now stays busy while checks run), Check Now shows
// the jobs it started at once, and a caller who may not read the settings
// still sees what needs attention, without the settings' actions.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import UpdatesPage from '../../../routes/(app)/updates/+page.svelte';

const nav = vi.hoisted(() => ({
	page: {
		params: {} as Record<string, string>,
		url: new URL('http://localhost/updates')
	},
	goto: vi.fn()
}));
vi.mock('$app/state', () => ({ page: nav.page }));
vi.mock('$app/navigation', () => ({ goto: nav.goto, beforeNavigate: vi.fn() }));

function job(id: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'update.check',
		state: 'running',
		origin: 'scheduled',
		executor: 'manager',
		environmentId: 'env-1',
		policyId: 'set-1',
		targets: [{ type: 'container', id: 'web' }],
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
	id: 'set-1',
	excludeEnvironments: [],
	excludeStacks: [],
	excludeContainers: [],
	checkSchedule: { enabled: false, cron: '0 3 * * *', timeZone: 'UTC' },
	runSchedule: { enabled: false, cron: '0 4 * * *', timeZone: 'UTC' },
	waitTimeoutSeconds: 0,
	actions: ['update_policy.read', 'update_policy.manage', 'update.check', 'update.run'],
	revision: 1,
	updatedAt: '2026-09-01T10:00:00Z'
};

// A covered container's record with an update available.
const record = {
	id: 'rec-1',
	parentId: 'set-1',
	environmentId: 'env-1',
	name: 'Automatic updates for web',
	targetName: 'web',
	target: { type: 'container', id: 'web' },
	view: 'full',
	actions: ['update.check'],
	summary: {
		available: 1,
		upToDate: 0,
		quarantined: 0,
		ineligible: 0,
		failed: 0,
		unchecked: 0,
		lastCheckAt: '2026-09-28T09:00:00Z'
	}
};

let running: Job[] = [];
let details: Record<string, Job> = {};
let started: Job[] = [];
let canRead = true;

beforeEach(() => {
	running = [];
	details = {};
	started = [];
	canRead = true;
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		const problem = (status: number, code: string) =>
			json(status, { code, message: code, details: [], requestId: 'r', retryable: false });
		if (url.pathname.startsWith('/api/v1/update-settings') && !canRead)
			return problem(403, 'forbidden');
		if (url.pathname === '/api/v1/update-settings') return json(200, settings);
		if (url.pathname === '/api/v1/update-settings/targets') return json(200, { items: [] });
		if (url.pathname === '/api/v1/update-settings/checks') return json(200, { jobs: started });
		if (url.pathname === '/api/v1/update-policies') return json(200, { items: [record] });
		if (url.pathname === '/api/v1/jobs') {
			// The running list asks for the unfinished states; the recent
			// runs query does not.
			const items = url.searchParams.has('state') ? running : [];
			return json(200, { items, total: items.length });
		}
		const id = url.pathname.match(/^\/api\/v1\/jobs\/([^/]+)$/)?.[1];
		if (id && details[id]) return json(200, details[id]);
		return problem(404, 'not_found');
	});
});
afterEach(() => vi.unstubAllGlobals());

function openPage() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: UpdatesPage as unknown as Component<Record<string, unknown>>,
			props: {}
		}
	});
}

describe('Updates page', () => {
	it('shows the running checks and updates after a reload', async () => {
		running = [
			job('0190-2', { kind: 'update.run', targets: [{ type: 'container', id: 'api' }] }),
			job('0190-1')
		];
		details = Object.fromEntries(running.map((j) => [j.id, j]));
		openPage();

		expect(
			await screen.findByRole('progressbar', { name: 'Check for Updates web progress' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Apply Updates api progress' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('region', { name: 'Running Checks and Updates' })
		).toBeInTheDocument();
		// Checks are running: Check Now waits for them.
		expect(await screen.findByRole('button', { name: /Check Now/ })).toBeDisabled();
	});

	it('shows the checks Check Now started at once', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		started = [
			job('0190-5', { state: 'queued', origin: 'manual' }),
			job('0190-6', {
				state: 'queued',
				origin: 'manual',
				targets: [{ type: 'container', id: 'api' }]
			})
		];
		details = Object.fromEntries(started.map((j) => [j.id, j]));
		openPage();

		await user.click(await screen.findByRole('button', { name: /Check Now/ }));

		await waitFor(() => expect(screen.getAllByRole('progressbar')).toHaveLength(2));
		expect(
			screen.getByRole('progressbar', { name: 'Check for Updates web progress' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Check for Updates api progress' })
		).toBeInTheDocument();
	});

	it('shows what needs attention without the settings to a caller who may not read them', async () => {
		canRead = false;
		openPage();

		const table = await screen.findByRole('table', {
			name: 'Stacks and containers that need attention'
		});
		expect(table).toHaveTextContent('web');
		expect(screen.queryByRole('button', { name: /Check Now/ })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull();
		expect(screen.queryByText('What It Covers')).toBeNull();
	});
});
