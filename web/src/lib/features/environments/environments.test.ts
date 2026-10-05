import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Environment, EnvironmentSystem, MyPermissions } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';
import QueryHarness from '../../../test/QueryHarness.svelte';
import ArchiveEnvironmentDialog from './ArchiveEnvironmentDialog.svelte';
import EditEnvironmentDialog from './EditEnvironmentDialog.svelte';
import AgentsPanel from './AgentsPanel.svelte';
import SystemPanel from './SystemPanel.svelte';

const goto = vi.hoisted(() => vi.fn());
vi.mock('$app/navigation', () => ({ goto }));

type Handler = (req: Request) => Response | Promise<Response> | undefined;
const json = (body: unknown, status = 200) =>
	new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

let requests: Request[] = [];
function stubApi(handler: Handler) {
	requests = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: RequestInfo, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			requests.push(req.clone());
			return (await handler(req)) ?? json({ code: 'not_found', message: 'not found' }, 404);
		})
	);
}

const env: Environment = {
	id: 'e1',
	name: 'homelab',
	online: true,
	status: 'active',
	view: 'full',
	revision: 7,
	serviceAddress: '192.168.1.10',
	actions: ['environment.read', 'environment.manage', 'environment.remove']
};

function mount<P extends Record<string, unknown>>(
	component: Component<P>,
	props: P,
	seed?: (client: QueryClient) => void
) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	seed?.(client);
	return render(QueryHarness<P>, { props: { client, component, props } });
}

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

beforeEach(() => goto.mockReset());
afterEach(() => vi.unstubAllGlobals());

describe('EditEnvironmentDialog (#3)', () => {
	it('saves name and service address with If-Match of the opened revision', async () => {
		const user = setup();
		stubApi((req) =>
			req.method === 'PATCH' ? json({ ...env, name: 'homelab-1', revision: 8 }) : undefined
		);
		mount(EditEnvironmentDialog, { env, open: true });
		const dialog = await screen.findByRole('dialog', { name: 'Edit homelab' });
		const name = within(dialog).getByRole('textbox', { name: /^Name/ });
		expect(name).toHaveValue('homelab');
		await user.clear(name);
		await user.type(name, 'homelab-1');
		await user.click(within(dialog).getByRole('button', { name: 'Save Changes' }));
		await waitFor(() => expect(requests.some((r) => r.method === 'PATCH')).toBe(true));
		const patch = requests.find((r) => r.method === 'PATCH')!;
		expect(new URL(patch.url).pathname).toBe('/api/v1/environments/e1');
		expect(patch.headers.get('If-Match')).toBe('"7"');
		expect(await patch.json()).toEqual({ name: 'homelab-1', serviceAddress: '192.168.1.10' });
		expect(await screen.findByText('Saved homelab-1')).toBeInTheDocument();
	});

	it('reports a concurrent edit (412) and keeps the typed values', async () => {
		const user = setup();
		stubApi((req) =>
			req.method === 'PATCH'
				? json(
						{
							code: 'precondition_failed',
							message: 'the environment changed',
							retryable: false,
							details: []
						},
						412
					)
				: undefined
		);
		mount(EditEnvironmentDialog, { env, open: true });
		const dialog = await screen.findByRole('dialog', { name: 'Edit homelab' });
		const addr = within(dialog).getByRole('textbox', { name: /^Service Address/ });
		await user.clear(addr);
		await user.type(addr, 'nas.home.arpa');
		await user.click(within(dialog).getByRole('button', { name: 'Save Changes' }));
		expect(
			await within(dialog).findByText('Someone else changed this environment.')
		).toBeInTheDocument();
		expect(addr).toHaveValue('nas.home.arpa');
		expect(
			within(dialog).getByRole('button', { name: 'Load Current Values' })
		).toBeInTheDocument();
	});
});

describe('ArchiveEnvironmentDialog (#34)', () => {
	const preview = {
		environmentId: 'e1',
		environmentName: 'homelab',
		status: 'active',
		revision: 9,
		action: 'archive',
		description: 'internal text',
		hostUntouched: true,
		backupSnapshots: 0,
		reattach: '',
		migration: { stacks: 2, description: 'POST /api/v1/…' },
		dependents: [
			{
				kind: 'stack',
				onArchive: 'kept',
				count: 2,
				items: [
					{ id: 's1', name: 'silo' },
					{ id: 's2', name: 'media' }
				]
			},
			{ kind: 'permission_rule', onArchive: 'removed', count: 0, items: [] }
		]
	};

	it('previews every dependent, offers migration and archives with If-Match after the typed name', async () => {
		const user = setup();
		stubApi((req) => {
			const path = new URL(req.url).pathname;
			if (req.method === 'POST' && path === '/api/v1/environments/e1/removal-previews')
				return json(preview);
			if (req.method === 'DELETE' && path === '/api/v1/environments/e1')
				return new Response(null, { status: 204 });
		});
		mount(ArchiveEnvironmentDialog, { env, open: true });
		const dialog = await screen.findByRole('alertdialog', { name: 'Archive homelab' });
		expect(dialog).toHaveTextContent('2 stacks: kept, and back after a re-attach.');
		expect(dialog).toHaveTextContent('Nothing on the host changes');
		expect(dialog).not.toHaveTextContent('POST /api'); // API wording never reaches the user
		expect(within(dialog).getByText('silo')).toBeInTheDocument();
		expect(
			within(dialog).getByRole('button', { name: 'Migrate 2 Stacks First' })
		).toBeInTheDocument();
		const confirm = within(dialog).getByRole('button', { name: 'Archive Environment' });
		expect(confirm).toBeDisabled();
		await user.type(
			within(dialog).getByRole('textbox', { name: 'Type homelab to confirm' }),
			'homelab'
		);
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		await waitFor(() => expect(requests.some((r) => r.method === 'DELETE')).toBe(true));
		expect(requests.find((r) => r.method === 'DELETE')!.headers.get('If-Match')).toBe('"9"');
		expect(await screen.findByText('Archived homelab')).toBeInTheDocument();
		expect(goto).toHaveBeenCalledWith('/environments');
	});

	it('migrates the stacks first in the environment migration wizard', async () => {
		const user = setup();
		stubApi((req) =>
			req.method === 'POST' &&
			new URL(req.url).pathname === '/api/v1/environments/e1/removal-previews'
				? json(preview)
				: undefined
		);
		mount(ArchiveEnvironmentDialog, { env, open: true });
		const dialog = await screen.findByRole('alertdialog', { name: 'Archive homelab' });
		await user.click(within(dialog).getByRole('button', { name: 'Migrate 2 Stacks First' }));
		expect(goto).toHaveBeenCalledWith('/environments/e1/migrate');
	});

	it('shows why the preview failed with a retry', async () => {
		stubApi(() =>
			json(
				{
					code: 'unavailable',
					message: 'the manager is busy',
					retryable: true,
					details: []
				},
				503
			)
		);
		mount(ArchiveEnvironmentDialog, { env, open: true });
		const dialog = await screen.findByRole('dialog', { name: 'Archive homelab' });
		expect(
			await within(dialog).findByText('The removal preview could not be loaded.')
		).toBeInTheDocument();
		expect(within(dialog).getByRole('button', { name: /Retry/ })).toBeInTheDocument();
	});
});

describe('AgentsPanel (#3)', () => {
	const agent = {
		id: 'a1',
		environmentId: 'e1',
		hostname: 'homelab',
		status: 'active',
		connected: true,
		view: 'full',
		revision: 4,
		version: '1.2.0',
		compatibility: 'outdated',
		actions: ['agent.read', 'agent.manage', 'agent.remove']
	};

	it('flags outdated agents, rotates credentials and removes an agent with If-Match', async () => {
		const user = setup();
		stubApi((req) => {
			const path = new URL(req.url).pathname;
			if (req.method === 'GET' && path === '/api/v1/environments/e1/agents')
				return json({
					items: [
						agent,
						{
							...agent,
							id: 'a0',
							status: 'revoked',
							actions: [],
							revokedAt: '2026-09-01T00:00:00Z'
						}
					]
				});
			if (req.method === 'POST' && path === '/api/v1/agents/a1/credential-rotations')
				return json(
					{ agentId: 'a1', state: 'pending', requestedAt: '2026-09-25T12:00:00Z' },
					201
				);
			if (req.method === 'DELETE' && path === '/api/v1/agents/a1')
				return new Response(null, { status: 204 });
		});
		mount(AgentsPanel, { env });
		const table = await screen.findByRole('table', { name: 'Agents of homelab' });
		expect(within(table).getByText('Upgrade Recommended')).toBeInTheDocument();
		expect(table).toHaveTextContent('Connected now'); // not its once-a-minute last-seen time
		expect(table).not.toHaveTextContent('a1'); // the agent ID is only the name's tooltip
		// Only the active agent has actions (not the revoked one).
		expect(within(table).getAllByRole('button', { name: 'Actions for homelab' })).toHaveLength(
			1
		);

		await user.click(within(table).getByRole('button', { name: 'Actions for homelab' }));
		expect((await screen.findAllByRole('menuitem')).map((i) => i.textContent?.trim())).toEqual([
			'Rotate Credential',
			'Remove Agent'
		]);
		await user.click(screen.getByRole('menuitem', { name: 'Rotate Credential' }));
		const rotate = await screen.findByRole('alertdialog', { name: 'Rotate Agent Credential' });
		await user.click(within(rotate).getByRole('button', { name: 'Rotate Credential' }));
		await waitFor(() =>
			expect(requests.some((r) => r.url.endsWith('/credential-rotations'))).toBe(true)
		);
		expect(
			requests
				.find((r) => r.url.endsWith('/credential-rotations'))!
				.headers.get('Idempotency-Key')
		).toBeTruthy();
		expect(await screen.findByText('Rotated the credential of homelab')).toBeInTheDocument();

		await user.click(within(table).getByRole('button', { name: 'Actions for homelab' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Remove Agent' }));
		const remove = await screen.findByRole('alertdialog', { name: 'Remove Agent' });
		expect(remove).toHaveTextContent('stays offline and detached');
		await user.type(
			within(remove).getByRole('textbox', { name: 'Type homelab to confirm' }),
			'homelab'
		);
		await user.click(within(remove).getByRole('button', { name: 'Remove Agent' }));
		await waitFor(() => expect(requests.some((r) => r.method === 'DELETE')).toBe(true));
		expect(requests.find((r) => r.method === 'DELETE')!.headers.get('If-Match')).toBe('"4"');
	});

	it('offers Re-Attach when only removed agents are left, with Add Environments', async () => {
		stubApi((req) => {
			// Re-attaching creates an install command: agent.enroll (#282).
			if (new URL(req.url).pathname === '/api/v1/me/permissions')
				return json({
					owner: false,
					entries: [
						{ capability: 'agent.enroll', scope: { kind: 'instance' }, allowed: true }
					]
				});
			if (new URL(req.url).pathname === '/api/v1/environments/e1/agents')
				return json({
					items: [
						{
							...agent,
							status: 'revoked',
							actions: [],
							revokedAt: '2026-09-01T00:00:00Z'
						}
					]
				});
		});
		mount(AgentsPanel, { env });
		await screen.findByRole('table', { name: 'Agents of homelab' });
		expect(screen.getByText('No agent is attached.')).toBeInTheDocument();
		expect(await screen.findByRole('link', { name: 'Re-Attach' })).toHaveAttribute(
			'href',
			expect.stringContaining('reattach=e1')
		);
	});

	it('offers Re-Attach in the detached notice exactly with Add Environments', async () => {
		// The caller's permissions are known before the panel renders (and a
		// refetch answers the same), so the gate is decided at once.
		const perms = (enroll: boolean): MyPermissions =>
			({
				catalogVersion: 1,
				owner: false,
				userId: 'u1',
				groupIds: [],
				environments: [],
				entries: [
					{ capability: 'environment.read', scope: { kind: 'instance' }, allowed: true },
					...(enroll
						? [
								{
									capability: 'agent.enroll',
									scope: { kind: 'instance' },
									allowed: true
								}
							]
						: [])
				]
			}) as MyPermissions;
		for (const enroll of [true, false]) {
			stubApi((req) => {
				const path = new URL(req.url).pathname;
				if (path === '/api/v1/me/permissions') return json(perms(enroll));
				if (path === '/api/v1/environments/e1/agents')
					return json({
						items: [
							{
								...agent,
								status: 'revoked',
								actions: [],
								revokedAt: '2026-09-01T00:00:00Z'
							}
						]
					});
			});
			const { unmount } = mount(AgentsPanel, { env }, (client) =>
				client.setQueryData(liveKeys.myPermissions, perms(enroll))
			);
			await screen.findByRole('table', { name: 'Agents of homelab' });
			expect(screen.getByText('No agent is attached.')).toBeInTheDocument();
			const link = screen.queryByRole('link', { name: 'Re-Attach' });
			if (enroll)
				expect(link).toHaveAttribute('href', expect.stringContaining('reattach=e1'));
			else expect(link).not.toBeInTheDocument();
			unmount();
		}
	});
});

describe('SystemPanel (#3)', () => {
	const now = new Date('2026-09-25T12:00:00Z');
	const system = (connected: boolean): EnvironmentSystem => ({
		environmentId: 'e1',
		online: connected,
		agent: {
			id: 'a1',
			version: '1.4.0',
			versionStatus: 'current',
			compatibility: 'current',
			os: 'linux',
			arch: 'amd64',
			protocols: ['docker-manager.agent/v1'],
			connected
		},
		features: [],
		commands: [],
		requests: [],
		streams: [],
		roots: [],
		diagnostics: []
	});
	const seen = {
		...env,
		createdAt: '2026-09-01T00:00:00Z',
		connectionChangedAt: '2026-09-25T09:00:00Z',
		lastSeenAt: '2026-09-25T11:58:00Z'
	};

	it('shows when a disconnected agent was last seen', () => {
		mount(SystemPanel, { env: { ...seen, online: false }, system: system(false), now });
		expect(screen.getByText('Not Connected')).toBeInTheDocument();
		expect(screen.getByText(/^last seen/)).toBeInTheDocument();
		expect(screen.getByText('2 minutes ago')).toHaveAttribute(
			'datetime',
			'2026-09-25T11:58:00Z'
		);
		expect(screen.queryByText(/^since/)).not.toBeInTheDocument();
	});

	it('shows since when a connected agent is connected', () => {
		mount(SystemPanel, { env: seen, system: system(true), now });
		expect(screen.getByText('Connected')).toBeInTheDocument();
		expect(screen.getByText(/^since/)).toBeInTheDocument();
		expect(screen.queryByText(/^last seen/)).not.toBeInTheDocument();
	});
});
