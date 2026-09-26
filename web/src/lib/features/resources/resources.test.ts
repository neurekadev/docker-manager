// Components of the resource pages (#6, #32): refusals are shown with the
// server's reason, removals list their consequences or blockers, and
// Docker Manager's own objects are marked.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import type { Container } from '$lib/api/queries';
import EnvironmentGaps from './EnvironmentGaps.svelte';
import ProtectionBadge from './ProtectionBadge.svelte';
import RemovalDialog from './RemovalDialog.svelte';
import ActionHostHarness from './test/ActionHostHarness.svelte';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

const agentProtection = {
	role: 'agent' as const,
	reason: 'the Docker Agent connected to this environment: stopping or removing it cuts Docker Manager off from this host',
	self: true,
	restartAllowed: false
};

const agent: Container = {
	id: 'c1',
	name: 'docker-agent',
	environmentId: 'e1',
	state: 'running',
	view: 'full',
	actions: ['container.stop', 'container.remove', 'container.details.read'],
	protection: agentProtection
};

function json(status: number, body: unknown) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

afterEach(() => vi.unstubAllGlobals());

describe('container actions on Docker Manager itself (#32)', () => {
	it('shows the refusal of stopping the connected agent with the reason', async () => {
		const fetch = vi.fn(async () =>
			json(409, {
				code: 'protected',
				message: `refused to stop a protected Docker Manager resource: ${agentProtection.reason}`,
				requestId: 'r1',
				retryable: false,
				details: []
			})
		);
		vi.stubGlobal('fetch', fetch);
		const user = setup();
		render(ActionHostHarness, { props: { container: agent, verb: 'stop' } });
		await user.click(screen.getByRole('button', { name: 'Request stop' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Stop docker-agent?' });
		await user.click(within(dialog).getByRole('button', { name: 'Stop container' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent(
			"Docker Manager's own agent can't be stopped from Docker Manager."
		);
		expect(alert).toHaveTextContent('cuts Docker Manager off from this host');
		const req = fetch.mock.calls[0] as unknown as [Request];
		expect(new URL(req[0].url).pathname).toBe(
			'/api/v1/environments/e1/containers/docker-agent/stop'
		);
	});

	it("lists why the agent can't be removed instead of offering the removal", async () => {
		vi.stubGlobal(
			'fetch',
			vi.fn(async () =>
				json(200, {
					...agent,
					details: {
						removal: {
							allowed: false,
							blockers: [
								{
									code: 'protected',
									message: `Docker Manager's own resource: ${agentProtection.reason}`
								}
							],
							consequences: [
								'The container and its writable layer are deleted; its logs are lost.'
							]
						}
					}
				})
			)
		);
		const user = setup();
		render(ActionHostHarness, { props: { container: agent, verb: 'remove' } });
		await user.click(screen.getByRole('button', { name: 'Request remove' }));
		const dialog = await screen.findByRole('alertdialog', {
			name: "docker-agent can't be removed"
		});
		expect(dialog).toHaveTextContent('cuts Docker Manager off from this host');
		expect(dialog).toHaveTextContent('use Docker on the host');
		expect(within(dialog).queryByRole('button', { name: /Remove/ })).toBeNull();
	});
});

describe('RemovalDialog (#6 deletion consequences)', () => {
	it('lists consequences and needs the name typed before removing', async () => {
		const user = setup();
		const onconfirm = vi.fn();
		render(RemovalDialog, {
			props: {
				open: true,
				kind: 'volume',
				name: 'app_data',
				removal: {
					allowed: true,
					blockers: [],
					consequences: [
						'All data in the volume is deleted permanently (restore it from a backup, #10).'
					]
				},
				confirmLabel: 'Remove volume and its data',
				onconfirm
			}
		});
		const dialog = screen.getByRole('alertdialog', { name: 'Remove app_data?' });
		expect(dialog).toHaveTextContent(
			'All data in the volume is deleted permanently (restore it from a backup).'
		);
		const button = within(dialog).getByRole('button', { name: 'Remove volume and its data' });
		expect(button).toBeDisabled();
		await user.type(within(dialog).getByLabelText('Type app_data to confirm'), 'app_data');
		await user.click(button);
		await waitFor(() => expect(onconfirm).toHaveBeenCalledOnce());
	});

	it('shows blockers such as a predefined network', () => {
		render(RemovalDialog, {
			props: {
				open: true,
				kind: 'network',
				name: 'bridge',
				removal: {
					allowed: false,
					blockers: [
						{
							code: 'network_builtin',
							message: 'Predefined networks cannot be removed.'
						}
					],
					consequences: []
				},
				confirmLabel: 'Remove network',
				onconfirm: vi.fn()
			}
		});
		const dialog = screen.getByRole('alertdialog', { name: "bridge can't be removed" });
		expect(dialog).toHaveTextContent('Predefined networks cannot be removed.');
	});
});

describe('marks and notices', () => {
	it('marks Docker Manager system resources with the reason for assistive technology', () => {
		render(ProtectionBadge, { props: { protection: agentProtection } });
		expect(screen.getByText(/Docker Manager system/)).toHaveTextContent(
			'Docker Manager system: the Docker Agent connected to this environment'
		);
	});

	it('names offline and failing environments instead of silently shortening the list', () => {
		render(EnvironmentGaps, {
			props: {
				what: 'containers',
				single: false,
				unavailable: [
					{ environment: { id: 'e3', name: 'edge', online: false }, offline: true },
					{
						environment: { id: 'e2', name: 'nas', online: true },
						offline: false,
						error: new Error('agent timeout')
					}
				]
			}
		});
		expect(screen.getByText('edge is offline')).toBeInTheDocument();
		expect(screen.getByText('nas could not be read')).toBeInTheDocument();
		expect(
			screen.getByText(
				/Agent timeout\. The containers of the other environments are listed\./
			)
		).toBeInTheDocument();
	});
});
