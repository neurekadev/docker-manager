// Move complete (the new manager): what is left after the move, "Mark as
// done" once the old server's confirmation failed (step-up), the agents'
// fixes, removing the old copies (from the environment migration's record)
// and archiving the old environment.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import MoveCompleteCard from './MoveCompleteCard.svelte';
import type { ManagerMove } from './model';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

function arrived(p: Partial<ManagerMove> = {}): ManagerMove {
	return {
		id: 'mv-1',
		state: 'arrived',
		createdAt: '2026-09-29T10:00:00Z',
		expiresAt: '2026-10-06T10:00:00Z',
		sourceUrl: 'http://192.168.1.10:8080',
		arrivedAt: '2026-09-29T12:00:00Z',
		jobsRunning: 0,
		oldManagerConfirmed: false,
		confirmAttempts: 4,
		redirects: [],
		...p
	};
}

const old = {
	environmentId: 'env-old',
	name: 'old-box',
	online: true,
	archived: false,
	stackCount: 0,
	stoppedCopies: 2,
	migrationId: 'job-e'
};

let acknowledged = 0;

beforeEach(() => {
	acknowledged = 0;
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const path = new URL(req.url).pathname;
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (path === '/api/v1/environments/env-old/migrations/job-e')
			return json(200, {
				id: 'job-e',
				sourceEnvironmentId: 'env-old',
				targetEnvironmentId: 'env-new',
				state: 'succeeded',
				groups: [['st-1', 'st-2']],
				stacks: [
					{ stackId: 'st-1', name: 'proxy', state: 'moved', migrationId: 'm-1' },
					{ stackId: 'st-2', name: 'app', state: 'moved', migrationId: 'm-2' }
				],
				networks: [],
				createdAt: '2026-09-29T10:00:00Z',
				updatedAt: '2026-09-29T11:00:00Z'
			});
		if (req.method === 'POST' && path === '/api/v1/manager/move/acknowledgements') {
			acknowledged++;
			return json(
				200,
				arrived({
					confirmError: 'state_refused',
					confirmAcknowledgedAt: '2026-09-29T13:00:00Z'
				})
			);
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

function card(move: ManagerMove) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: MoveCompleteCard as unknown as Component<Record<string, unknown>>,
			props: { move }
		}
	});
}

describe('MoveCompleteCard', () => {
	it('lists what is left: the confirmation, an agent’s fix, old copies, archiving', async () => {
		card(
			arrived({
				oldManagerConfirmed: true,
				oldEnvironment: old,
				redirects: [
					{
						environmentId: 'env-old',
						environmentName: 'old-box',
						role: 'old_server',
						url: 'http://192.168.1.20:8080',
						sent: false,
						errorCode: 'timeout',
						connected: false,
						needsFix: true
					}
				]
			})
		);

		expect(screen.getByRole('heading', { name: 'Move complete' })).toBeInTheDocument();
		expect(
			screen.getByText(/Docker Manager moved here from http:\/\/192\.168\.1\.10:8080\./)
		).toBeInTheDocument();
		expect(screen.getByText(/The old server confirmed the move/)).toBeInTheDocument();
		expect(screen.getByText(/old-box did not get the new address/)).toBeInTheDocument();
		expect(
			screen.getByText('DOCKER_AGENT_MANAGER_URL=http://192.168.1.20:8080')
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Copy setting' })).toBeInTheDocument();
		// The old copies come from the environment migration that moved the apps.
		expect(
			await screen.findByRole('button', { name: 'Remove old copies from old-box' })
		).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Open old-box to archive it' })).toHaveAttribute(
			'href',
			'/environments/env-old'
		);
		expect(screen.queryByRole('button', { name: 'Mark as done' })).toBeNull();
	});

	it('offers Mark as done once the confirmation failed, with a warning', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		card(arrived({ confirmError: 'state_refused' }));

		expect(screen.getByText(/Waiting for the old server to confirm/)).toBeInTheDocument();
		expect(
			screen.getByText(
				'The old server refused the confirmation. Make sure Docker Manager no longer runs there, then mark this as done.'
			)
		).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Mark as done' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Mark the move as done?' });
		expect(dialog).toHaveTextContent('Two copies must never manage the same servers.');
		const confirm = screen
			.getAllByRole('button', { name: 'Mark as done' })
			.find((b) => dialog.contains(b));
		await user.click(confirm!);
		await waitFor(() => expect(acknowledged).toBe(1));
	});
});
