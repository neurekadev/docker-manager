// The environment migration wizard (#35): the stacks to move (Docker
// Manager's own stack off), the check with the move order and what is not
// moved, a running migration of the environment reopening the wizard at
// the move step after a reload (docs/internal/web.md, "Job progress after
// reload"), the latest ended migration's result restored while it left
// something to do (old copies to remove, stacks to migrate), the removal of
// the old copies as tracked jobs, Next's reason while it is off, and the
// empty state with a single environment.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
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

const stack = (id: string, name: string, environmentId = 'env-1') => ({
	id,
	name,
	environmentId,
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

function removal(state: Job['state']): Job {
	return migration({
		id: 'rm-1',
		kind: 'stack.remove_source',
		state,
		targets: [{ type: 'stack', id: 'st-1' }],
		progress: { percent: state === 'succeeded' ? 100 : 0 }
	});
}

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

// The latest migration, ended: proxy moved (its old copy still on
// homelab), app did not and is still there.
const endedRecord = (removed = false): EnvironmentMigration => ({
	...record,
	state: 'failed',
	stacks: [
		{
			stackId: 'st-1',
			name: 'proxy',
			state: 'moved',
			migrationId: 'm-1',
			sourceRemoved: removed || undefined
		},
		{ stackId: 'st-2', name: 'app', state: 'failed', migrationId: 'm-2' }
	],
	finishedAt: '2026-09-29T10:05:00Z'
});

let environments: unknown[] = [];
let running: Job[] = [];
let previews: unknown[] = [];
let stacks: unknown[] = [];
let recent: EnvironmentMigration[] = [];
let jobState: Job['state'] = 'running';
let removals: string[] = [];

beforeEach(() => {
	environments = [env('env-1', 'homelab'), env('env-2', 'nas'), env('env-3', 'lab', false)];
	running = [];
	previews = [];
	stacks = [stack('st-1', 'proxy'), stack('st-2', 'app'), stack('st-dm', 'docker-manager')];
	recent = [];
	jobState = 'running';
	removals = [];
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
		if (path === '/api/v1/stacks') return json(200, { items: stacks });
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
		if (path === '/api/v1/jobs/job-e') return json(200, migration({ state: jobState }));
		if (path === '/api/v1/environments/env-1/migrations') return json(200, { items: recent });
		if (path === '/api/v1/environments/env-1/migrations/job-e')
			return json(200, recent[0] ?? record);
		if (
			req.method === 'POST' &&
			path === '/api/v1/stacks/st-1/migrations/m-1/source-removals'
		) {
			removals.push('st-1/m-1');
			return json(202, removal('queued'));
		}
		if (path === '/api/v1/jobs/rm-1') {
			// The removal ended: the record says so from now on.
			recent = [endedRecord(true)];
			return json(200, removal('succeeded'));
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

	it('opens on the result of the latest ended migration while it left something to do', async () => {
		recent = [endedRecord()];
		jobState = 'failed';
		stacks = [
			stack('st-1', 'proxy', 'env-2'),
			stack('st-2', 'app'),
			stack('st-dm', 'docker-manager')
		];
		wizard();

		expect(await screen.findByRole('heading', { level: 2, name: 'Move' })).toBeInTheDocument();
		expect(screen.getByText(/This migration ended/)).toBeInTheDocument();
		const rows = within(screen.getByRole('region', { name: 'Stacks' }));
		const proxy = rows.getByRole('link', { name: 'proxy' }).closest('li') as HTMLElement;
		expect(within(proxy).getByText('Moved')).toBeInTheDocument();
		expect(within(proxy).getByText('Old copy kept')).toBeInTheDocument();
		const app = rows.getByRole('link', { name: 'app' }).closest('li') as HTMLElement;
		expect(within(app).getByText('Did not move')).toBeInTheDocument();

		expect(screen.getByText('1 stack runs on nas now.')).toBeInTheDocument();
		expect(
			screen.getByText(
				'Its old copy on homelab is stopped and kept. Remove it once you are sure.'
			)
		).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Remove old copies from homelab' })
		).toBeInTheDocument();
		expect(screen.getByText(/1 stack is still on homelab\./)).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Migrate the rest' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Start a new migration' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Back' })).toBeNull();
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Back to homelab' })).toBeEnabled()
		);
		// It ended before the page opened: no toast about it.
		expect(screen.queryByText('app did not move to nas')).toBeNull();
	});

	it('starts a new migration from a restored result, focus on the first step', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		recent = [endedRecord()];
		jobState = 'failed';
		stacks = [stack('st-1', 'proxy', 'env-2'), stack('st-2', 'app')];
		wizard();

		await user.click(await screen.findByRole('button', { name: 'Start a new migration' }));

		const heading = await screen.findByRole('heading', { level: 2, name: 'Destination' });
		await waitFor(() => expect(heading).toHaveFocus());
		expect(screen.getByRole('checkbox', { name: 'app' })).toBeChecked();
		expect(screen.queryByRole('checkbox', { name: 'proxy' })).toBeNull();
	});

	it('removes the old copies as tracked jobs and shows the result from the record', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		recent = [endedRecord()];
		jobState = 'failed';
		stacks = [stack('st-1', 'proxy', 'env-2'), stack('st-2', 'app')];
		wizard();

		await user.click(
			await screen.findByRole('button', { name: 'Remove old copies from homelab' })
		);
		const dialog = await screen.findByRole('alertdialog', {
			name: 'Remove the old copies from homelab?'
		});
		await user.type(
			within(dialog).getByRole('textbox', { name: 'Type homelab to confirm' }),
			'homelab'
		);
		await user.click(within(dialog).getByRole('button', { name: 'Remove old copies' }));

		expect(await screen.findByText('Removed 1 old copy from homelab')).toBeInTheDocument();
		expect(removals).toEqual(['st-1/m-1']);
		expect(
			within(screen.getByRole('region', { name: 'Removing old copies' })).getAllByText(
				/Remove the old copy of proxy/
			).length
		).toBeGreaterThan(0);
		// The record now says the copy is gone.
		expect(await screen.findByText('Old copy removed')).toBeInTheDocument();
		expect(screen.getByText('Its old copy was removed from homelab.')).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Remove old copies from homelab' })).toBeNull();
	});

	it('starts a new migration when the latest one left nothing to do', async () => {
		const moved = endedRecord(true);
		recent = [{ ...moved, state: 'completed', stacks: [moved.stacks[0]] }];
		stacks = [stack('st-1', 'proxy', 'env-2'), stack('st-2', 'app')];
		wizard();

		expect(
			await screen.findByRole('heading', { level: 2, name: 'Destination' })
		).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Start a new migration' })).toBeNull();
	});

	it('says why Next is off', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		stacks = [stack('st-2', 'app')];
		wizard();

		const next = await screen.findByRole('button', { name: 'Check migration' });
		await waitFor(() => expect(next).toBeEnabled());
		await user.click(screen.getByRole('checkbox', { name: 'app' }));
		expect(next).toBeDisabled();
		expect(next).toHaveAttribute('title', 'Choose at least one stack to migrate.');
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
