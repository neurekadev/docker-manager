import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import ImportStackDialog from './ImportStackDialog.svelte';
import type { DiscoveredStack } from './queries';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

let posted: { path: string; body: unknown }[] = [];
// The caller's running jobs (GET /jobs?state=…) and extra discovered projects.
let running: Job[] = [];
let more: DiscoveredStack[] = [];

// An import by copy of `media` running since before the page loaded: its
// stack exists already, so discovery lists the project as managed.
const mediaImport = {
	id: 'job-7',
	kind: 'stack.import',
	state: 'running',
	origin: 'manual',
	executor: 'agent',
	environmentId: 'env-1',
	targets: [{ type: 'stack', id: 'st-5' }],
	attempt: 1,
	progress: { percent: 30, step: 'copy_files' },
	items: [],
	locks: [],
	locksHeld: true,
	cancelRequested: false,
	cancellable: true,
	retryable: false,
	createdAt: '2026-09-28T10:00:00Z',
	updatedAt: '2026-09-28T10:00:00Z'
} as Job;
const media: DiscoveredStack = {
	name: 'media',
	adoptable: false,
	copyable: false,
	stackId: 'st-5',
	services: [{ name: 'jellyfin', image: 'jellyfin', containers: 1, running: 0 }]
};

// A project that runs, and one without containers copied from an import
// mount, with its volumes.
const projects: DiscoveredStack[] = [
	{
		name: 'shop',
		adoptable: true,
		copyable: false,
		services: [{ name: 'web', image: 'nginx', containers: 1, running: 1 }],
		volumes: ['shop_a', 'shop_b', 'shop_c', 'shop_d', 'shop_e']
	},
	{
		name: 'wiki',
		adoptable: false,
		copyable: true,
		containerless: true,
		sourceDir: '/srv/wiki',
		services: [{ name: 'app', image: 'wiki:2', containers: 0, running: 0 }],
		volumes: ['wiki_data']
	}
];

beforeEach(() => {
	posted = [];
	running = [];
	more = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const text = await req.text();
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (req.method === 'POST') {
			posted.push({ path: url.pathname, body: text ? JSON.parse(text) : undefined });
			return json(202, {
				id: 'job-1',
				state: 'queued',
				kind: 'stack.import',
				items: [],
				targets: [{ type: 'stack', id: 'st-9' }]
			});
		}
		switch (url.pathname) {
			case '/api/v1/me/permissions':
				return json(200, { owner: true, entries: [], environments: [], catalogVersion: 1 });
			case '/api/v1/environments':
				return json(200, {
					items: [
						{
							id: 'env-1',
							name: 'nas',
							online: true,
							status: 'active',
							view: 'full',
							actions: []
						}
					]
				});
			case '/api/v1/environments/env-1/stacks/discovered':
				return json(200, { projects: [...projects, ...more] });
			case '/api/v1/jobs':
				return json(200, { items: running, total: running.length });
			case '/api/v1/jobs/job-7':
				return json(200, mediaImport);
		}
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

function dialog() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: ImportStackDialog as unknown as Component<Record<string, unknown>>,
			props: { open: true, environmentId: 'env-1' }
		}
	});
}

describe('ImportStackDialog', () => {
	it('lists projects without containers and every project’s volumes', async () => {
		dialog();
		const list = await screen.findByRole('list', { name: 'Compose projects on nas' });
		const items = within(list).getAllByRole('listitem');
		const wiki = items.find((li) => li.textContent?.includes('wiki'));
		const shop = items.find((li) => li.textContent?.includes('shop'));
		expect(wiki).toBeDefined();
		expect(shop).toBeDefined();
		expect(within(wiki!).getByText('No containers')).toBeInTheDocument();
		expect(within(wiki!).queryByText(/of 0 running/)).toBeNull();
		expect(
			within(wiki!).getByText(
				'Copied into Docker Manager. Nothing starts: deploy it afterwards.'
			)
		).toBeInTheDocument();
		expect(within(wiki!).getByText('Volumes: wiki_data')).toBeInTheDocument();
		expect(within(shop!).getByText('1 of 1 running')).toBeInTheDocument();
		expect(
			within(shop!).getByText('Volumes: shop_a, shop_b, shop_c +2 more')
		).toBeInTheDocument();
	});

	it('imports a project without containers by copy without asking to stop anything', async () => {
		const user = setup();
		dialog();
		const list = await screen.findByRole('list', { name: 'Compose projects on nas' });
		const wiki = within(list)
			.getAllByRole('listitem')
			.find((li) => li.textContent?.includes('wiki'))!;
		await user.click(within(wiki).getByRole('button', { name: 'Import' }));
		await waitFor(() =>
			expect(posted).toEqual([
				{
					path: '/api/v1/environments/env-1/stacks/import-copies',
					body: { projectName: 'wiki' }
				}
			])
		);
		expect(screen.queryByRole('alertdialog')).toBeNull();
	});

	it('shows an import still running when the dialog opens again', async () => {
		running = [mediaImport];
		more = [media];
		dialog();
		const bar = await screen.findByRole('progressbar', { name: 'Import media progress' });
		const row = bar.closest('li')!;
		expect(within(row).getByText('media')).toBeInTheDocument();
		// Not "Managed" while the import runs, and nothing to start.
		expect(within(row).queryByText('Managed')).toBeNull();
		expect(within(row).queryByRole('button', { name: 'Import' })).toBeNull();
		expect(within(row).queryByRole('link', { name: 'Open stack' })).toBeNull();
	});

	it('hides a managed project without a running import', async () => {
		more = [media];
		dialog();
		await screen.findByRole('list', { name: 'Compose projects on nas' });
		expect(screen.queryByText('media')).toBeNull();
	});
});
