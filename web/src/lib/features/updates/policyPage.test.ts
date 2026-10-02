// The update policy page's running checks and updates (#20,
// docs/internal/web.md "Job progress after reload"): the policy's running
// jobs come back from the running list after a reload (Check now stays
// busy while its checks run), and Check now shows the jobs it started at
// once.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import PolicyPage from '../../../routes/(app)/updates/[policyId]/+page.svelte';

const nav = vi.hoisted(() => ({
	page: {
		params: { policyId: 'pol-1' } as Record<string, string>,
		url: new URL('http://localhost/updates/pol-1')
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
		policyId: 'pol-1',
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

const policy = {
	id: 'pol-1',
	name: 'Nightly updates',
	scope: 'all',
	enabled: true,
	revision: 1,
	excludeStacks: [],
	excludeContainers: [],
	checkSchedule: { enabled: false, cron: '0 3 * * *', timeZone: 'UTC' },
	runSchedule: { enabled: false, cron: '0 4 * * *', timeZone: 'UTC' },
	waitTimeoutSeconds: 0,
	createdAt: '2026-09-01T10:00:00Z',
	updatedAt: '2026-09-01T10:00:00Z'
};

let running: Job[] = [];
let details: Record<string, Job> = {};
let started: Job[] = [];
// Target records (GET /update-policies/{id}) by ID.
let records: Record<string, unknown> = {};

beforeEach(() => {
	running = [];
	details = {};
	started = [];
	records = {};
	nav.page.params.policyId = 'pol-1';
	nav.goto.mockClear();
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname === '/api/v1/environment-update-policies/pol-1') return json(200, policy);
		if (url.pathname === '/api/v1/environment-update-policies/pol-1/targets')
			return json(200, { items: [] });
		if (url.pathname === '/api/v1/environment-update-policies/pol-1/checks')
			return json(202, { jobs: started });
		if (url.pathname === '/api/v1/jobs') {
			// The running list asks for the unfinished states; the recent
			// runs query does not.
			const items = url.searchParams.has('state') ? running : [];
			return json(200, { items, total: items.length });
		}
		const id = url.pathname.match(/^\/api\/v1\/jobs\/([^/]+)$/)?.[1];
		if (id && details[id]) return json(200, details[id]);
		const rec = url.pathname.match(/^\/api\/v1\/update-policies\/([^/]+)$/)?.[1];
		if (rec && records[rec]) return json(200, records[rec]);
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

// A target's record as GET /update-policies/{id} returns it.
function record(id: string, parentId?: string) {
	return {
		id,
		parentId,
		environmentId: 'env-1',
		name: 'Automatic updates for web',
		target: { type: 'container', id: 'web' },
		view: 'full',
		actions: []
	};
}

describe('update policy page: links to a target record (#218)', () => {
	it('opens the environment policy that manages the record', async () => {
		nav.page.params.policyId = 'rec-1';
		records['rec-1'] = record('rec-1', 'pol-1');
		openPage();
		await waitFor(() =>
			expect(nav.goto).toHaveBeenCalledWith('/updates/pol-1', { replaceState: true })
		);
		expect(screen.queryByText('This update policy does not exist.')).toBeNull();
	});

	it('opens Updates for a record without an environment policy', async () => {
		nav.page.params.policyId = 'rec-2';
		records['rec-2'] = record('rec-2');
		openPage();
		await waitFor(() =>
			expect(nav.goto).toHaveBeenCalledWith('/updates', { replaceState: true })
		);
	});

	it('says the policy does not exist when nothing has the ID', async () => {
		nav.page.params.policyId = 'gone';
		openPage();
		expect(await screen.findByText('This update policy does not exist.')).toBeInTheDocument();
		expect(nav.goto).not.toHaveBeenCalled();
	});
});

describe('update policy page: running checks and updates', () => {
	it("shows the policy's running checks and updates after a reload", async () => {
		running = [
			job('0190-3', { policyId: 'pol-2', targets: [{ type: 'container', id: 'db' }] }),
			job('0190-2', { kind: 'update.run', targets: [{ type: 'container', id: 'api' }] }),
			job('0190-1')
		];
		details = Object.fromEntries(running.map((j) => [j.id, j]));
		openPage();

		expect(
			await screen.findByRole('progressbar', { name: 'Check for updates web progress' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Apply updates api progress' })
		).toBeInTheDocument();
		// Another policy's check is not this page's.
		expect(screen.getAllByRole('progressbar')).toHaveLength(2);
		expect(
			screen.getByRole('region', { name: 'Running checks and updates of Nightly updates' })
		).toBeInTheDocument();
		// Its checks are running: Check now waits for them.
		expect(screen.getByRole('button', { name: /Check now/ })).toBeDisabled();
	});

	it('shows the checks Check now started at once', async () => {
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

		await user.click(await screen.findByRole('button', { name: /Check now/ }));

		await waitFor(() => expect(screen.getAllByRole('progressbar')).toHaveLength(2));
		expect(
			screen.getByRole('progressbar', { name: 'Check for updates web progress' })
		).toBeInTheDocument();
		expect(
			screen.getByRole('progressbar', { name: 'Check for updates api progress' })
		).toBeInTheDocument();
	});
});
