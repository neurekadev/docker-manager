// The environment migration wizard (#35): the stacks to move (Docker
// Manager's own stack off), the check with the move order and what is not
// moved, a running migration of the environment reopening the wizard at
// the move step after a reload (docs/internal/web.md, "Job progress after
// reload"), and the empty state with a single environment.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Environment, Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import EnvironmentMigrationWizard from './EnvironmentMigrationWizard.svelte';
import type {
	EnvironmentMigration,
	EnvironmentMigrationPreview,
	StackMove
} from './environment-migration';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const source: Environment = {
	id: 'env-1',
	name: 'homelab',
	online: true,
	status: 'active',
	view: 'full',
	actions: []
};

const env = (id: string, name: string, online = true) => ({
	id,
	name,
	online,
	status: 'active',
	view: 'full',
	actions: []
});

const stack = (id: string, name: string) => ({
	id,
	name,
	environmentId: 'env-1',
	status: 'deployed',
	view: 'full',
	actions: ['stack.migrate']
});

const data = {
	destinationStacksFree: 10_000,
	destinationVolumesFree: 10_000,
	imageBytes: 0,
	projectBytes: 10,
	totalBytes: 110,
	volumeBytes: 100
};

const move = (stackId: string, name: string, dependsOn: string[] = []): StackMove => ({
	stackId,
	name,
	group: 0,
	dependsOn,
	preview: {
		access: { changes: [], complete: true, othersAffected: 0 },
		allowed: true,
		blockers: [],
		data,
		downtime: { basis: 'measured', estimatedSeconds: 30 },
		excluded: [],
		kind: 'stack',
		leftovers: [],
		services: [],
		sourceEnvironmentId: 'env-1',
		targetEnvironmentId: 'env-2',
		transport: {
			bandwidthLimitBytesPerSecond: 0,
			destinationPlainHttp: false,
			sourcePlainHttp: false
		},
		volumes: [],
		warnings: []
	}
});

const preview: EnvironmentMigrationPreview = {
	sourceEnvironmentId: 'env-1',
	targetEnvironmentId: 'env-2',
	allowed: true,
	blockers: [],
	warnings: [],
	stacks: [move('st-1', 'proxy'), move('st-2', 'app', ['st-1'])],
	groups: [['st-1', 'st-2']],
	networks: [],
	skipped: [{ stackId: 'st-dm', name: 'docker-manager', reason: 'docker_manager' }],
	data,
	downtime: { basis: 'The stacks of a group stop together.', estimatedSeconds: 60 }
};

const record: EnvironmentMigration = {
	id: 'job-e',
	sourceEnvironmentId: 'env-1',
	targetEnvironmentId: 'env-2',
	state: 'running',
	groups: [['st-1', 'st-2']],
	stacks: [
		{ stackId: 'st-1', name: 'proxy', state: 'moved', migrationId: 'm-1' },
		{ stackId: 'st-2', name: 'app', state: 'moving', migrationId: 'm-2' }
	],
	networks: [],
	createdAt: '2026-09-29T10:00:00Z',
	updatedAt: '2026-09-29T10:00:00Z'
};

function migration(p: Partial<Job> = {}): Job {
	return {
		id: 'job-e',
		kind: 'environment.migrate',
		state: 'running',
		origin: 'manual',
		executor: 'manager',
		environmentId: 'env-1',
		targets: [
			{ type: 'stack', id: 'st-1' },
			{ type: 'stack', id: 'st-2' }
		],
		attempt: 1,
		progress: { percent: 40, step: 'migrate' },
		items: [],
		locks: [],
		locksHeld: true,
		cancelRequested: false,
		cancellable: true,
		retryable: false,
		createdAt: '2026-09-29T10:00:00Z',
		updatedAt: '2026-09-29T10:00:00Z',
		...p
	} as Job;
}

let environments: unknown[] = [];
let running: Job[] = [];
let previews: unknown[] = [];

beforeEach(() => {
	environments = [env('env-1', 'homelab'), env('env-2', 'nas'), env('env-3', 'lab', false)];
	running = [];
	previews = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		const path = url.pathname;
		if (path === '/api/v1/environments') return json(200, { items: environments });
		if (path === '/api/v1/stacks')
			return json(200, {
				items: [
					stack('st-1', 'proxy'),
					stack('st-2', 'app'),
					stack('st-dm', 'docker-manager')
				]
			});
		if (path === '/api/v1/environments/env-1/containers')
			return json(200, {
				items: [
					{
						id: 'c1',
						name: 'docker-manager',
						protection: {
							role: 'manager',
							reason: 'r',
							restartAllowed: false,
							self: true
						},
						stack: { project: 'docker-manager', managed: true, stackId: 'st-dm' }
					}
				]
			});
		if (req.method === 'POST' && path === '/api/v1/environments/env-1/migration-previews') {
			previews.push(await req.json());
			return json(200, preview);
		}
		if (path === '/api/v1/jobs') return json(200, { items: running, total: running.length });
		if (path === '/api/v1/jobs/job-e') return json(200, migration());
		if (path === '/api/v1/environments/env-1/migrations/job-e') return json(200, record);
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

function wizard() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: EnvironmentMigrationWizard as unknown as Component<Record<string, unknown>>,
			props: { environment: source }
		}
	});
}

describe('EnvironmentMigrationWizard', () => {
	it('chooses the stacks, Docker Manager’s own off, and shows the check’s order', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		wizard();

		expect(
			await screen.findByRole('heading', { level: 2, name: 'Destination' })
		).toBeInTheDocument();
		const own = await screen.findByRole('checkbox', { name: /^docker-manager/ });
		await waitFor(() => expect(own).toBeDisabled());
		expect(own).not.toBeChecked();
		expect(screen.getByText('Moves with Docker Manager')).toBeInTheDocument();
		expect(screen.getByRole('checkbox', { name: 'app' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'proxy' })).toBeChecked();

		// The only online destination is chosen; the offline one says why.
		expect(
			screen.getByText('Offline environments cannot be chosen: the check needs them online.')
		).toBeInTheDocument();
		const next = screen.getByRole('button', { name: 'Check migration' });
		await waitFor(() => expect(next).toBeEnabled());
		await user.click(next);

		expect(await screen.findByText('Ready to migrate 2 stacks to nas')).toBeInTheDocument();
		expect(previews).toEqual([{ targetEnvironmentId: 'env-2' }]);
		expect(
			screen.getByText(
				'These share a network or volume: they stop together and move in this order.'
			)
		).toBeInTheDocument();
		expect(screen.getByText(/after proxy/)).toBeInTheDocument();
		expect(
			screen.getByText(/Docker Manager's own stack\. It moves when you move Docker Manager\./)
		).toBeInTheDocument();
		expect(screen.getByText('Longest downtime')).toBeInTheDocument();
		expect(screen.getByText('The stacks of a group stop together.')).toBeInTheDocument();
	});

	it('opens at the move step on the environment’s running migration', async () => {
		running = [migration()];
		wizard();

		expect(
			await screen.findByRole('progressbar', { name: 'Migrate homelab to nas progress' })
		).toBeInTheDocument();
		expect(screen.getByRole('heading', { level: 2, name: 'Move' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Back' })).toBeNull();
	});

	it('says a second environment is needed when there is only one', async () => {
		environments = [env('env-1', 'homelab')];
		wizard();

		expect(
			await screen.findByRole('heading', {
				level: 3,
				name: 'Moving an environment needs a second environment.'
			})
		).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Add environment' })).toBeInTheDocument();
	});
});
