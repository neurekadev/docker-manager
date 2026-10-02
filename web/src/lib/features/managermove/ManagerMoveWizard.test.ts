// Settings → Move to a New Server (the old manager's wizard): the setup
// files after a step-up, shown once, with the one command to paste; new
// setup files when they are gone or expired; Next only once the new
// server's agent connected and its Docker Manager waits (its tooltip says
// why); the check with the reminders; Move Everything and its progress
// read from the move; a stopped run with "Try Again"; the moved panel
// after the handoff; resuming here waits for the restart.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import { stepUp } from '$lib/auth/stepup.svelte';
import type { EnvironmentMigrationPreview } from '$lib/features/environments/environment-migration';
import QueryHarness from '../../../test/QueryHarness.svelte';
import ManagerMoveWizard from './ManagerMoveWizard.svelte';
import type { ManagerMove } from './model';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const source = { environmentId: 'env-1', name: 'old-box', online: true, stackCount: 2 };

function move(p: Partial<ManagerMove> = {}): ManagerMove {
	return {
		id: 'mv-1',
		state: 'open',
		createdAt: '2026-09-29T10:00:00Z',
		expiresAt: '2026-10-06T10:00:00Z',
		thisServerAddress: '192.168.1.10:8080',
		newServerAddress: '192.168.1.20:8080',
		statusUrl: 'http://192.168.1.20:8080',
		jobsRunning: 0,
		oldManagerConfirmed: false,
		confirmAttempts: 0,
		redirects: [],
		...p
	};
}

const ready = {
	newServer: {
		environmentId: 'env-2',
		environmentName: 'new-box',
		enrollmentState: 'used' as const,
		online: true,
		managerCheckedIn: true
	},
	sourceEnvironment: source
};

const data = {
	destinationStacksFree: 10_000,
	destinationVolumesFree: 10_000,
	imageBytes: 0,
	projectBytes: 10,
	totalBytes: 110,
	volumeBytes: 100
};

const stackPreview = (stackId: string, name: string) => ({
	stackId,
	name,
	group: 0,
	dependsOn: [],
	preview: {
		access: { changes: [], complete: true, othersAffected: 0 },
		allowed: true,
		blockers: [],
		data,
		downtime: { basis: 'measured', estimatedSeconds: 30 },
		excluded: [],
		kind: 'stack' as const,
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

const preview = {
	sourceEnvironmentId: 'env-1',
	targetEnvironmentId: 'env-2',
	allowed: true,
	blockers: [],
	warnings: [],
	stacks: [stackPreview('st-1', 'proxy'), stackPreview('st-2', 'app')],
	groups: [['st-1'], ['st-2']],
	networks: [],
	skipped: [{ stackId: 'st-dm', name: 'docker-manager', reason: 'docker_manager' }],
	data,
	downtime: { basis: 'Each stack stops on its own.', estimatedSeconds: 30 }
} as unknown as EnvironmentMigrationPreview;

const job = {
	id: 'job-m',
	kind: 'manager.move',
	state: 'queued',
	origin: 'manual',
	executor: 'manager',
	targets: [{ type: 'manager', id: 'instance' }],
	attempt: 1,
	items: [],
	locks: [],
	locksHeld: false,
	cancelRequested: false,
	cancellable: true,
	retryable: false,
	createdAt: '2026-09-29T10:00:00Z',
	updatedAt: '2026-09-29T10:00:00Z'
} as unknown as Job;

let current: ManagerMove | null = null;
let created: unknown[] = [];
let runsStarted = 0;
let newFiles = 0;
let cancels: unknown[] = [];
let stepUpDone = false;

beforeEach(() => {
	current = null;
	created = [];
	runsStarted = 0;
	newFiles = 0;
	cancels = [];
	stepUpDone = false;
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const path = new URL(req.url).pathname;
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		const problem = (status: number, code: string) =>
			json(status, { code, message: code, details: [], requestId: 'r', retryable: false });
		if (req.method === 'GET' && path === '/api/v1/manager/move')
			return current ? json(200, current) : problem(404, 'not_found');
		if (path === '/api/v1/manager/move/defaults')
			return json(200, { thisServerAddress: '192.168.1.10', sourceEnvironment: source });
		if (path === '/api/v1/settings')
			return json(200, {
				instanceId: 'i-1',
				name: 'Home',
				revision: 1,
				updatedAt: '2026-09-29T10:00:00Z',
				deployment: { publicUrl: 'https://docker.example.com' }
			});
		if (path === '/api/v1/jobs') return json(200, { items: [], total: 0 });
		if (req.method === 'POST' && path === '/api/v1/manager/moves') {
			// The first try needs a recent sign-in (step-up).
			if (!stepUpDone) {
				stepUpDone = true;
				return problem(403, 'step_up_required');
			}
			created.push(await req.json());
			current = move();
			return json(201, {
				move: current,
				composeYaml: 'services:\n  docker-manager:\n    image: docker-manager\n',
				env: 'DOCKER_MANAGER_MOVE_CODE=dmm_x_y\n',
				statusUrl: 'http://192.168.1.20:8080'
			});
		}
		if (req.method === 'POST' && path === '/api/v1/manager/move/setup-files') {
			newFiles++;
			return json(200, {
				move: current,
				composeYaml: 'services:\n  docker-manager:\n    image: docker-manager\n',
				env: 'DOCKER_MANAGER_MOVE_CODE=dmm_x_new\n',
				statusUrl: 'http://192.168.1.20:8080',
				agentEnrolled: false
			});
		}
		if (req.method === 'POST' && path === '/api/v1/manager/move/cancellations') {
			cancels.push(await req.json());
			return json(200, move({ state: 'cancelled' }));
		}
		// Restarting: Docker Manager does not answer yet.
		if (path === '/api/v1/health') return problem(503, 'unavailable');
		if (req.method === 'POST' && path === '/api/v1/environments/env-1/migration-previews')
			return json(200, preview);
		if (req.method === 'POST' && path === '/api/v1/manager/move/runs') {
			runsStarted++;
			current = move({
				...ready,
				state: 'moving',
				progress: {
					jobId: 'job-m',
					jobState: 'running',
					stacksMoved: 1,
					stacksTotal: 2,
					currentStack: 'app'
				}
			});
			return json(202, job);
		}
		return problem(404, 'not_found');
	});
});
afterEach(() => {
	stepUp.settle(false);
	vi.unstubAllGlobals();
});

function wizard() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: ManagerMoveWizard as unknown as Component<Record<string, unknown>>,
			props: {}
		}
	});
}

describe('ManagerMoveWizard', () => {
	it('creates the setup files after a step-up and shows them once', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		wizard();

		expect(
			await screen.findByRole('heading', { level: 2, name: 'New Server' })
		).toBeInTheDocument();
		const here = screen.getByRole('textbox', { name: /This Server's Address/ });
		await waitFor(() => expect(here).toHaveValue('192.168.1.10'));
		await user.type(
			screen.getByRole('textbox', { name: /New Server's Address/ }),
			'192.168.1.20'
		);
		await user.click(screen.getByRole('button', { name: 'Create Setup Files' }));

		// The manager asks for a recent sign-in first; the files come after it.
		await waitFor(() => expect(stepUp.open).toBe(true));
		stepUp.settle(true);

		expect(
			await screen.findByText(
				'These files are shown once. They contain a one-time pairing code.'
			)
		).toBeInTheDocument();
		expect(created).toEqual([
			{ thisServerAddress: '192.168.1.10', newServerAddress: '192.168.1.20' }
		]);
		expect(screen.getByRole('button', { name: 'Copy compose.yaml' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Copy .env' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Download compose.yaml' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Download .env' })).toBeInTheDocument();
		expect(screen.getByText('Then run this in that folder:')).toBeInTheDocument();
		expect(screen.getByText('docker compose up -d')).toBeInTheDocument();
		// The same in one paste: the folder, both files and the start.
		expect(
			screen.getByRole('heading', { name: 'Or Paste This on the New Server' })
		).toBeInTheDocument();
		const script = screen.getByLabelText('One Command: command').textContent ?? '';
		expect(script).toContain('mkdir -p docker-manager');
		expect(script).toContain(
			"cat > .env <<'DOCKER_MANAGER_EOF'\nDOCKER_MANAGER_MOVE_CODE=dmm_x_y\n"
		);
		expect(script).toContain('chmod 600 .env');
		expect(script.trimEnd().endsWith('docker compose up -d\n)')).toBe(true);
		// The files' heading takes the focus.
		await waitFor(() =>
			expect(screen.getByRole('heading', { name: 'Set Up the New Server' })).toHaveFocus()
		);

		// The checklist waits for the new server; Next stays off until it is
		// ready, and says why.
		expect(screen.getByText(/New server's agent connected/)).toBeInTheDocument();
		expect(screen.getByText(/New Docker Manager is waiting/)).toBeInTheDocument();
		const next = screen.getByRole('button', { name: 'Next' });
		expect(next).toBeDisabled();
		expect(next).toHaveAttribute(
			'title',
			"Wait until the new server's agent is connected and its Docker Manager is waiting."
		);
		// The files are on the page: nothing to create again.
		expect(screen.queryByRole('button', { name: 'Create New Setup Files' })).toBeNull();
		expect(screen.getByRole('button', { name: 'Cancel the Move' })).toBeInTheDocument();
	});

	it('goes on once the new server is ready, checks, and moves everything', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		current = move(ready);
		wizard();

		const next = await screen.findByRole('button', { name: 'Next' });
		await waitFor(() => expect(next).toBeEnabled());
		await user.click(next);

		expect(await screen.findByRole('heading', { level: 2, name: 'Check' })).toBeInTheDocument();
		expect(await screen.findByText('Ready to migrate 2 stacks to new-box')).toBeInTheDocument();
		expect(screen.getByRole('heading', { name: 'Before You Start' })).toBeInTheDocument();
		expect(
			screen.getByText(
				'Your usual address stops working while your reverse proxy moves, until you point DNS at the new server.'
			)
		).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'http://192.168.1.20:8080' })).toHaveAttribute(
			'href',
			'http://192.168.1.20:8080'
		);

		await user.click(screen.getByRole('button', { name: 'Next' }));
		expect(await screen.findByRole('heading', { level: 2, name: 'Move' })).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Move Everything' }));

		expect(await screen.findByRole('progressbar', { name: 'Apps Moved' })).toBeInTheDocument();
		expect(runsStarted).toBe(1);
		expect(screen.getByText('Moving your apps: 1 of 2 stacks')).toBeInTheDocument();
		expect(screen.getByText('Now moving app.')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Moving…' })).toBeDisabled();
	});

	it('creates new setup files when the page no longer has them', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		current = move({
			newServer: { online: false, managerCheckedIn: false },
			sourceEnvironment: source
		});
		wizard();

		expect(
			await screen.findByText('The setup files were shown when you created the move.')
		).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Create New Setup Files' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Create new setup files?' });
		expect(dialog).toHaveTextContent(
			'The setup files you have now stop working. Docker Manager on the new server must use the new ones.'
		);
		expect(dialog).toHaveTextContent(
			"The new server's agent gets a new enrollment token, valid for 24 hours."
		);
		await user.click(within(dialog).getByRole('button', { name: 'Create New Setup Files' }));

		expect(
			await screen.findByText(
				'These files are shown once. They contain a one-time pairing code.'
			)
		).toBeInTheDocument();
		expect(newFiles).toBe(1);
		expect(screen.getByLabelText('.env: .env')).toHaveTextContent(
			'DOCKER_MANAGER_MOVE_CODE=dmm_x_new'
		);
		expect(
			screen.queryByText('The setup files were shown when you created the move.')
		).toBeNull();
	});

	it('offers new setup files once the enrollment token expired', async () => {
		current = move({
			newServer: { online: false, managerCheckedIn: false, enrollmentState: 'expired' },
			sourceEnvironment: source
		});
		wizard();

		expect(await screen.findByText('The setup files expired.')).toBeInTheDocument();
		expect(
			screen.getByText('The setup files expired. Create new setup files.')
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Create New Setup Files' })).toBeInTheDocument();
	});

	it('says only Docker Manager moves when there are no apps', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		current = move({ ...ready, sourceEnvironment: undefined });
		wizard();

		const next = await screen.findByRole('button', { name: 'Next' });
		await waitFor(() => expect(next).toBeEnabled());
		await user.click(next);
		expect(
			await screen.findByText(
				'Only Docker Manager moves: there are no apps on this server to move.'
			)
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Next' })).toBeEnabled();
	});

	it('opens on the Move step with the reason after a run stopped', async () => {
		current = move({
			...ready,
			progress: {
				jobId: 'job-m',
				jobState: 'failed',
				errorCode: 'manager_move_apps_not_moved',
				recovery: 'app did not start on new-box. Press Move everything again.',
				stacksMoved: 1,
				stacksTotal: 2
			}
		});
		wizard();

		expect(await screen.findByRole('heading', { level: 2, name: 'Move' })).toBeInTheDocument();
		expect(screen.getByText('Not every app moved')).toBeInTheDocument();
		expect(
			screen.getByText('app did not start on new-box. Press Move everything again.')
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Try Again' })).toBeEnabled();
		expect(screen.getByRole('button', { name: 'Cancel the Move' })).toBeInTheDocument();
	});

	it('says where to point DNS once handed over, and offers to resume here', async () => {
		current = move({ ...ready, state: 'handed_off' });
		wizard();

		expect(await screen.findByText('Docker Manager moved.')).toBeInTheDocument();
		expect(
			await screen.findByText(
				/Point DNS for docker\.example\.com at the new server \(192\.168\.1\.20\)\./
			)
		).toBeInTheDocument();
		expect(
			screen.getByText(/Handed over\. Waiting for the new Docker Manager to confirm\./)
		).toBeInTheDocument();
		expect(
			await screen.findByRole('button', { name: 'Resume on This Server' })
		).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Cancel the Move' })).toBeNull();
	});

	it('waits for Docker Manager to restart after resuming here', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		current = move({ ...ready, state: 'handed_off' });
		wizard();

		await user.click(await screen.findByRole('button', { name: 'Resume on This Server' }));
		const dialog = await screen.findByRole('alertdialog', {
			name: 'Resume Docker Manager on this server?'
		});
		await user.type(within(dialog).getByLabelText('Type Home to confirm'), 'Home');
		await user.click(within(dialog).getByRole('button', { name: 'Resume on This Server' }));

		expect(await screen.findByText(/Restarting Docker Manager…/)).toBeInTheDocument();
		expect(
			screen.getByText('This page reloads by itself as soon as Docker Manager answers again.')
		).toBeInTheDocument();
		expect(cancels).toEqual([{ resumeHere: true, instanceName: 'Home' }]);
	});
});
