import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Environment } from '$lib/api/client';
import { routes } from '$lib/routes';
import { templateKeys } from '$lib/features/templates/queries';
import DiffView from '$lib/ui/DiffView.svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import type { Stack, StackServiceStatus } from './queries';
import ServicesTable from './ServicesTable.svelte';
import StackHeader from './StackHeader.svelte';
import StackIcon from './StackIcon.svelte';
import { JobTray } from './tray.svelte';
import ValidationResult from './ValidationResult.svelte';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

interface Seen {
	method: string;
	path: string;
	search: string;
	body: unknown;
}
let seen: Seen[] = [];
// The stack's jobs (GET /jobs?target=stack:…) and the rename preview's extras.
let jobList: { id: string; kind: string; state: string }[] = [];
let previewExtra: Record<string, unknown> = {};

// The manager as seen by the components: image status with an update, the
// stack's jobs, rename previews, and 202 jobs for every other mutation.
beforeEach(() => {
	seen = [];
	jobList = [];
	previewExtra = {};
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const text = await req.text();
		seen.push({
			method: req.method,
			path: url.pathname,
			search: url.search,
			body: text ? JSON.parse(text) : undefined
		});
		const json = (status: number, body: unknown) =>
			new Response(JSON.stringify(body), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname.endsWith('/template-icons'))
			return json(200, {
				instanceId: 'inst-1',
				items: [
					{
						instanceId: 'inst-1',
						templateId: 'tpl-1',
						url: '/api/v1/templates/tpl-1/icon?v=abc'
					}
				]
			});
		if (url.pathname.endsWith('/image-status'))
			return json(200, {
				images: [
					{
						service: 'redis',
						image: 'redis:7',
						build: false,
						eligible: true,
						nonVersionTag: false,
						update: 'update_available'
					}
				]
			});
		if (req.method === 'GET' && url.pathname === '/api/v1/jobs')
			return json(200, {
				items: jobList.map((j) => ({ ...j, items: [], targets: [] }))
			});
		if (url.pathname.endsWith('/rename-previews')) {
			const to = (text ? JSON.parse(text) : {}).name ?? '';
			return json(200, {
				from: 'silo',
				to,
				fromDir: 'silo',
				toDir: to,
				running: ['web'],
				volumes: [],
				containers: [],
				blockers: [],
				warnings: [],
				...previewExtra
			});
		}
		if (url.pathname.endsWith('/renames'))
			return json(202, {
				id: 'job-r',
				state: 'queued',
				kind: 'stack.rename',
				items: [],
				targets: []
			});
		if (req.method !== 'GET')
			return json(202, {
				id: 'job-1',
				state: 'queued',
				kind: 'stack.stop',
				items: [],
				targets: []
			});
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

const ALL = [
	'stack.read',
	'stack.deploy',
	'stack.start',
	'stack.stop',
	'stack.restart',
	'stack.down',
	'stack.remove',
	'stack.migrate',
	'stack.manage',
	'stack.update',
	'update.check'
];

function stack(over: Partial<Stack> = {}): Stack {
	return {
		id: 'st-1',
		environmentId: 'env-1',
		name: 'silo',
		displayName: 'Silo',
		description: 'Personal cloud and media platform',
		status: 'deployed',
		view: 'full',
		actions: ALL,
		revision: 3,
		createdAt: '2026-09-04T10:00:00Z',
		environmentOnline: true,
		location: {
			root: 'stacks',
			dir: 'silo',
			hostPath: '/var/lib/docker/volumes/docker-manager_stacks/_data/silo'
		},
		services: [
			{ name: 'web', image: 'nginx', build: false, dependsOn: [] },
			{ name: 'redis', image: 'redis:7', build: false, dependsOn: [] }
		],
		engine: {
			state: 'running',
			services: [
				{ service: 'redis', containers: 1, running: 1 },
				{ service: 'web', containers: 2, running: 2 }
			]
		},
		...over
	} as Stack;
}

const env = {
	id: 'env-1',
	name: 'homelab',
	online: true,
	serviceAddress: '192.168.1.10'
} as Environment;

function header(s: Stack, tray = new JobTray()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			// The harness takes any component; StackHeader's Props is an interface.
			component: StackHeader as unknown as Component<Record<string, unknown>>,
			props: { stack: s, environment: env, tray, now: new Date('2026-09-25T10:00:00Z') }
		}
	});
	return tray;
}

/** A menu's items and separators ("---") in order. */
function menuEntries(menu: HTMLElement): (string | undefined)[] {
	return [...menu.querySelectorAll('[role="menuitem"], [role="separator"]')].map((e) =>
		e.getAttribute('role') === 'separator' ? '---' : e.textContent?.trim()
	);
}

describe('StackHeader', () => {
	it('shows the mockup header: name, status, description, meta with the host path, and every permitted action', async () => {
		const user = setup();
		header(stack());
		expect(screen.getByRole('heading', { level: 1, name: 'Silo' })).toBeInTheDocument();
		expect(screen.getByText('Running')).toBeInTheDocument();
		expect(screen.getByText('Personal cloud and media platform')).toBeInTheDocument();
		expect(screen.getByText('2 services')).toBeInTheDocument();
		expect(screen.getByText('3 containers')).toBeInTheDocument();
		expect(screen.getByText('Created 3 weeks ago')).toBeInTheDocument();
		const location = screen.getByText('homelab · silo');
		expect(location.closest('li')).toHaveAttribute(
			'title',
			'/var/lib/docker/volumes/docker-manager_stacks/_data/silo'
		);
		expect(screen.getByRole('button', { name: 'Copy Host Path' })).toBeInTheDocument();

		expect(screen.getByRole('button', { name: 'Deploy' })).toBeEnabled();
		expect(screen.getByRole('button', { name: /^More Deploy Options/ })).toBeInTheDocument();
		// Start, Restart and Stop are one split button: Stop while it runs.
		expect(screen.getByRole('button', { name: 'Stop' })).toHaveClass('danger-soft');
		expect(screen.queryByRole('button', { name: 'Restart' })).not.toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'More Start and Stop Options' }));
		const lifecycle = await screen.findByRole('menu');
		const entries = within(lifecycle).getAllByRole('menuitem');
		expect(entries.map((i) => i.textContent?.trim())).toEqual(['Start', 'Restart', 'Stop']);
		// Everything runs: nothing to start.
		expect(entries[0]).toHaveAttribute('aria-disabled', 'true');
		await user.keyboard('{Escape}');
		// Pull is a deploy option, not a button of its own, and so is the
		// former Update: the deploy menu says when newer images exist.
		expect(screen.queryByRole('button', { name: 'Pull' })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /^Update/ })).not.toBeInTheDocument();
		expect(
			await screen.findByRole('button', {
				name: 'More Deploy Options (newer images are available)'
			})
		).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'More Stack Actions' }));
		const menu = await screen.findByRole('menu', { name: 'More Stack Actions' });
		expect(
			within(menu)
				.getAllByRole('menuitem')
				.map((i) => i.textContent?.trim())
		).toEqual(['Migrate', 'Edit Details', 'Delete']);
	});

	it('offers neither Take Down nor Rename in the overflow menu', async () => {
		const user = setup();
		header(stack({ actions: [...ALL, 'stack.rename'] }));
		await user.click(screen.getByRole('button', { name: 'More Stack Actions' }));
		const menu = await screen.findByRole('menu', { name: 'More Stack Actions' });
		const labels = within(menu)
			.getAllByRole('menuitem')
			.map((i) => i.textContent?.trim());
		expect(labels).not.toContain('Take Down');
		expect(labels).not.toContain('Rename');
		// Rename is the pencil right of the name.
		expect(screen.getByRole('button', { name: 'Rename Silo' })).toBeEnabled();
	});

	it('stops a partially running stack by default and starts the rest from its menu', async () => {
		const user = setup();
		const tray = header(
			stack({
				engine: {
					state: 'partial',
					services: [
						{ service: 'redis', containers: 1, running: 0 },
						{ service: 'web', containers: 2, running: 2 }
					]
				}
			})
		);
		expect(screen.getByRole('button', { name: 'Stop' })).toBeEnabled();
		await user.click(screen.getByRole('button', { name: 'More Start and Stop Options' }));
		const start = await screen.findByRole('menuitem', { name: 'Start' });
		expect(start).not.toHaveAttribute('aria-disabled', 'true');
		await user.click(start);
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Start Silo'));
		expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/operations',
			body: { action: 'start' }
		});
	});

	it("keeps Stop and Restart of Docker Manager's own stack visible but off, with the reason", async () => {
		const user = setup();
		header(
			stack({
				protection: {
					role: 'docker_manager_project',
					reason: "Docker Manager's own Compose project",
					self: true,
					restartAllowed: false
				}
			})
		);
		const reason =
			'Docker Manager cannot stop, restart, migrate, rename or delete its own stack. Deploy works.';
		const stop = screen.getByRole('button', { name: 'Stop' });
		expect(stop).toBeDisabled();
		expect(stop).toHaveAttribute('title', reason);
		expect(screen.getByText(reason)).toHaveClass('sr-only');
		expect(screen.getByRole('button', { name: 'Deploy' })).toBeEnabled();
		await user.click(screen.getByRole('button', { name: 'More Start and Stop Options' }));
		expect(await screen.findByRole('menuitem', { name: 'Restart' })).toHaveAttribute(
			'aria-disabled',
			'true'
		);
		expect(screen.getByRole('menuitem', { name: 'Stop' })).not.toHaveAccessibleDescription();
	});

	it('hides what the caller may not do (the server still decides)', () => {
		header(
			stack({
				view: 'minimal',
				actions: ['stack.deploy'],
				services: undefined,
				engine: undefined,
				revision: undefined
			})
		);
		expect(screen.getByRole('button', { name: 'Deploy' })).toBeInTheDocument();
		for (const name of [
			'Restart',
			'Stop',
			'Start',
			'More Start and Stop Options',
			'More Stack Actions',
			'Rename Silo'
		])
			expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /Update/ })).not.toBeInTheDocument();
		// No host path for a minimal view.
		expect(screen.queryByRole('button', { name: 'Copy Host Path' })).not.toBeInTheDocument();
	});

	it('offers Start for a stopped stack and disables actions while the environment is offline', () => {
		header(
			stack({
				engine: { state: 'stopped', services: [] },
				readOnly: true,
				environmentOnline: false
			})
		);
		const start = screen.getByRole('button', { name: 'Start' });
		expect(start).toBeDisabled();
		expect(start).toHaveClass('ok-soft');
		expect(screen.getByRole('button', { name: 'More Start and Stop Options' })).toBeDisabled();
		expect(screen.queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Deploy' })).toBeDisabled();
		expect(screen.getByText('Read-Only')).toHaveAttribute(
			'title',
			'Read-only while homelab is offline'
		);
	});

	it('stops only after the confirmation that lists what happens, and tracks the job', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(screen.getByRole('button', { name: 'Stop' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Stop Silo?' });
		expect(
			within(dialog).getByText('Stops 3 containers, the services that need others first.')
		).toBeInTheDocument();
		expect(seen.filter((s) => s.method === 'POST')).toEqual([]);
		await user.click(within(dialog).getByRole('button', { name: 'Stop' }));
		await waitFor(() => expect(tray.jobs).toHaveLength(1));
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/operations',
			body: { action: 'stop' }
		});
		expect(tray.jobs[0]).toMatchObject({
			id: 'job-1',
			title: 'Stop Silo',
			success: 'Stopped Silo',
			failure: 'Silo was not stopped'
		});
	});

	it('deletes only after typing the project name, keeping volumes and files', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(screen.getByRole('button', { name: 'More Stack Actions' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Delete' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Delete Silo?' });
		expect(
			within(dialog).getByText('Keeps its volumes and the project directory on the host.')
		).toBeInTheDocument();
		const confirm = within(dialog).getByRole('button', { name: 'Delete Stack' });
		expect(confirm).toBeDisabled();
		await user.type(within(dialog).getByRole('textbox'), 'silo');
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Delete Silo'));
		const del = seen.find((s) => s.method === 'DELETE');
		expect(del?.path).toBe('/api/v1/stacks/st-1');
		// Volumes are kept unless the box is ticked.
		expect(del?.search).toBe('');
	});

	it('removes the stack’s own volumes only when the box is ticked', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(screen.getByRole('button', { name: 'More Stack Actions' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Delete' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Delete Silo?' });
		const box = within(dialog).getByRole('checkbox', {
			name: /Also Remove the Stack’s Volumes/
		});
		expect(box).not.toBeChecked();
		await user.click(box);
		expect(
			within(dialog).getByText(/Removes the volumes the stack owns and all data in them/)
		).toBeInTheDocument();
		await user.type(within(dialog).getByRole('textbox'), 'silo');
		await user.click(within(dialog).getByRole('button', { name: 'Delete Stack and Volumes' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Delete Silo'));
		expect(seen.find((s) => s.method === 'DELETE')?.search).toBe('?removeVolumes=true');
	});

	it('deploys from the split button without a confirmation (the files on disk are the definition)', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(screen.getByRole('button', { name: 'Deploy' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Deploy Silo'));
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/deployments',
			body: {}
		});
		// A deploy that changed nothing says so (its last deploy time stays).
		expect(tray.jobs[0].successFor).toBeTypeOf('function');
	});

	it('restarts at once from the lifecycle menu, without a confirmation', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(screen.getByRole('button', { name: 'More Start and Stop Options' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Restart' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Restart Silo'));
		expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/operations',
			body: { action: 'restart' }
		});
	});

	it('pulls every image and deploys from the menu', async () => {
		const user = setup();
		const tray = header(stack());
		// No separate Update button any more: Pull & Deploy replaces it.
		expect(screen.queryByRole('button', { name: /^Update/ })).not.toBeInTheDocument();
		await user.click(
			await screen.findByRole('button', {
				name: 'More Deploy Options (newer images are available)'
			})
		);
		const item = await screen.findByRole('menuitem', { name: 'Pull & Deploy' });
		expect(item).not.toHaveAccessibleDescription();
		await user.click(item);
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Pull and Deploy Silo'));
		expect(tray.jobs[0]).toMatchObject({ failure: 'Silo was not pulled and deployed' });
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/deployments',
			body: { pull: 'always' }
		});
		expect(seen.some((s) => s.path.endsWith('/pulls'))).toBe(false);
	});

	it('keeps the builds out of the deploy menu and shows Build only for a build section', async () => {
		const user = setup();
		header(stack());
		expect(screen.queryByRole('button', { name: 'Build' })).not.toBeInTheDocument();
		await user.click(await screen.findByRole('button', { name: /^More Deploy Options/ }));
		expect(menuEntries(await screen.findByRole('menu'))).toEqual([
			'Deploy',
			expect.stringMatching(/^Pull & Deploy/),
			'---',
			'Cleanup Orphans & Deploy'
		]);
	});

	it('builds without deploying from the Build button', async () => {
		const user = setup();
		const tray = header(
			stack({
				actions: [...ALL, 'stack.build'],
				services: [{ name: 'web', image: 'silo-web', build: true, dependsOn: [] }]
			})
		);
		await user.click(screen.getByRole('button', { name: 'Build' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Build Images of Silo'));
		expect(tray.jobs[0]).toMatchObject({
			kind: 'stack.build',
			success: 'Built the images of Silo',
			failure: 'The images of Silo were not built'
		});
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/builds',
			body: {}
		});
	});

	it('pulls newer base images and builds with Pull & Build', async () => {
		const user = setup();
		const tray = header(
			stack({
				actions: [...ALL, 'stack.build'],
				services: [{ name: 'web', image: 'silo-web', build: true, dependsOn: [] }]
			})
		);
		await user.click(screen.getByRole('button', { name: 'More Build Options' }));
		const menu = await screen.findByRole('menu');
		expect(menuEntries(menu)).toEqual([
			'Build',
			'Pull & Build',
			'---',
			'Build & Deploy',
			'Pull, Build & Deploy'
		]);
		await user.click(within(menu).getByRole('menuitem', { name: 'Pull & Build' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Pull and Build Images of Silo'));
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/builds',
			body: { pull: true }
		});
	});

	it('offers the build deploys without stack.build and pulls newer base images with Pull, Build & Deploy', async () => {
		const user = setup();
		const tray = header(
			stack({ services: [{ name: 'web', image: 'silo-web', build: true, dependsOn: [] }] })
		);
		// Without stack.build the main part builds and deploys.
		expect(screen.getByRole('button', { name: 'Build & Deploy' })).toBeEnabled();
		await user.click(screen.getByRole('button', { name: 'More Build Options' }));
		const menu = await screen.findByRole('menu');
		expect(menuEntries(menu)).toEqual(['Build & Deploy', 'Pull, Build & Deploy']);
		await user.click(within(menu).getByRole('menuitem', { name: 'Pull, Build & Deploy' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Pull, Build and Deploy Silo'));
		expect(tray.jobs[0]).toMatchObject({ failure: 'Silo was not pulled, built and deployed' });
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/deployments',
			body: { pull: 'always', build: true }
		});
	});

	it('hides the actions while the migration wizard is open', () => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(QueryHarness, {
			props: {
				client,
				component: StackHeader as unknown as Component<Record<string, unknown>>,
				props: { stack: stack(), environment: env, tray: new JobTray(), showActions: false }
			}
		});
		expect(screen.getByRole('heading', { level: 1, name: 'Silo' })).toBeInTheDocument();
		for (const name of ['Deploy', 'Stop', 'More Stack Actions', 'Rename Silo'])
			expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
	});

	it('removes orphaned containers only from the deploy menu, after a confirmation', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(await screen.findByRole('button', { name: /^More Deploy Options/ }));
		const menu = await screen.findByRole('menu');
		expect(menuEntries(menu)).toEqual([
			'Deploy',
			expect.stringMatching(/^Pull & Deploy/),
			'---',
			'Cleanup Orphans & Deploy'
		]);
		await user.click(within(menu).getByRole('menuitem', { name: 'Cleanup Orphans & Deploy' }));
		const dialog = await screen.findByRole('alertdialog', {
			name: 'Deploy Silo and remove orphaned containers?'
		});
		expect(seen.filter((s) => s.method === 'POST')).toEqual([]);
		await user.click(within(dialog).getByRole('button', { name: 'Deploy and Remove Orphans' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Deploy Silo and Remove Orphans'));
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/deployments',
			body: { removeOrphans: true }
		});
	});
});

describe('StackHeader inline rename', () => {
	const renamable = (over: Partial<Stack> = {}) =>
		stack({ actions: [...ALL, 'stack.rename'], ...over });

	it('turns the name into a field with the project name; Escape leaves it unchanged', async () => {
		const user = setup();
		header(renamable());
		await user.click(screen.getByRole('button', { name: 'Rename Silo' }));
		// The heading shows the display name; the field edits the project name.
		const field = screen.getByRole('textbox', { name: 'Stack Name' });
		expect(field).toHaveValue('silo');
		expect(field).toHaveFocus();
		expect(screen.getByText(/the display name Silo stays/)).toBeInTheDocument();
		await user.clear(field);
		await user.type(field, 'store{Escape}');
		expect(screen.queryByRole('textbox', { name: 'Stack Name' })).not.toBeInTheDocument();
		expect(screen.getByRole('heading', { level: 1, name: 'Silo' })).toBeVisible();
		expect(seen.filter((s) => s.method === 'POST')).toEqual([]);
		// The cancel button does the same.
		await user.click(screen.getByRole('button', { name: 'Rename Silo' }));
		await user.click(screen.getByRole('button', { name: 'Cancel Rename' }));
		expect(screen.queryByRole('textbox', { name: 'Stack Name' })).not.toBeInTheDocument();
	});

	it('checks the name inline before anything is sent', async () => {
		const user = setup();
		header(renamable());
		await user.click(screen.getByRole('button', { name: 'Rename Silo' }));
		const field = screen.getByRole('textbox', { name: 'Stack Name' });
		// Unchanged.
		await user.type(field, '{Enter}');
		expect(await screen.findByText('The stack already has this name.')).toBeInTheDocument();
		await user.clear(field);
		await user.type(field, 'My Store{Enter}');
		expect(await screen.findByText(/Use lower-case letters/)).toBeInTheDocument();
		expect(field).toHaveAttribute('aria-invalid', 'true');
		expect(seen.filter((s) => s.method === 'POST')).toEqual([]);
	});

	it('renames at once on Enter, without a confirmation, and tracks the rename job', async () => {
		const user = setup();
		const tray = header(renamable());
		await user.click(screen.getByRole('button', { name: 'Rename Silo' }));
		const field = screen.getByRole('textbox', { name: 'Stack Name' });
		await user.clear(field);
		await user.type(field, 'store{Enter}');
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Rename silo to store'));
		expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument();
		const post = seen.find((s) => s.path === '/api/v1/stacks/st-1/renames');
		expect(post?.body).toEqual({ name: 'store' });
		expect(tray.jobs[0]).toMatchObject({
			id: 'job-r',
			kind: 'stack.rename',
			success: 'Renamed silo to store',
			failure: 'silo was not renamed'
		});
		expect(screen.queryByRole('textbox', { name: 'Stack Name' })).not.toBeInTheDocument();
	});

	it('submits with the check button too', async () => {
		const user = setup();
		const tray = header(renamable());
		await user.click(screen.getByRole('button', { name: 'Rename Silo' }));
		const field = screen.getByRole('textbox', { name: 'Stack Name' });
		await user.clear(field);
		await user.type(field, 'store');
		await user.click(screen.getByRole('button', { name: 'Rename Stack' }));
		await waitFor(() => expect(tray.jobs[0]?.kind).toBe('stack.rename'));
	});

	it('says why the server would refuse, and does not rename', async () => {
		const user = setup();
		previewExtra = { declaredName: 'shop' };
		header(renamable());
		await user.click(screen.getByRole('button', { name: 'Rename Silo' }));
		const field = screen.getByRole('textbox', { name: 'Stack Name' });
		await user.clear(field);
		await user.type(field, 'store{Enter}');
		expect(await screen.findByText(/Its Compose file sets name: shop/)).toBeInTheDocument();
		previewExtra = {
			blockers: [{ code: 'name_taken', message: 'Another stack is named store.' }]
		};
		await user.type(field, '{Enter}');
		expect(await screen.findByText('Another stack is named store.')).toBeInTheDocument();
		expect(seen.some((s) => s.path.endsWith('/renames'))).toBe(false);
	});

	it('shows the pencil only with the capability and turns it off with the reason', () => {
		header(stack());
		expect(screen.queryByRole('button', { name: 'Rename Silo' })).not.toBeInTheDocument();
	});

	it('turns the pencil off while the environment is offline', () => {
		header(renamable({ readOnly: true, environmentOnline: false }));
		const pencil = screen.getByRole('button', { name: 'Rename Silo' });
		expect(pencil).toBeDisabled();
		expect(pencil).toHaveAttribute('title', 'Read-only while homelab is offline');
	});

	it("turns the pencil off for Docker Manager's own stack", () => {
		header(
			renamable({
				protection: {
					role: 'docker_manager_project',
					reason: "Docker Manager's own Compose project",
					self: true,
					restartAllowed: false
				}
			})
		);
		const pencil = screen.getByRole('button', { name: 'Rename Silo' });
		expect(pencil).toBeDisabled();
		expect(pencil).toHaveAttribute('title', expect.stringMatching(/cannot .*rename/));
	});

	it('turns every action off while a rename runs, and on again when it ends', async () => {
		const user = setup();
		const tray = new JobTray();
		tray.add(
			{ id: 'job-r' },
			{
				kind: 'stack.rename',
				title: 'Rename silo to store',
				success: 'Renamed silo to store',
				failure: 'silo was not renamed'
			}
		);
		header(renamable(), tray);
		const reason = 'Renaming Silo…';
		const deploy = screen.getByRole('button', { name: 'Deploy' });
		const stop = screen.getByRole('button', { name: 'Stop' });
		for (const b of [deploy, stop]) {
			expect(b).toBeDisabled();
			expect(b).toHaveAttribute('title', reason);
		}
		expect(screen.getByRole('button', { name: /^More Deploy Options/ })).toBeDisabled();
		expect(screen.getByRole('button', { name: 'More Start and Stop Options' })).toBeDisabled();
		const pencil = screen.getByRole('button', { name: 'Rename Silo' });
		expect(pencil).toBeDisabled();
		expect(pencil).toHaveAttribute('title', reason);
		await user.click(screen.getByRole('button', { name: 'More Stack Actions' }));
		const menu = await screen.findByRole('menu', { name: 'More Stack Actions' });
		expect(within(menu).getByText(reason)).toBeInTheDocument();
		for (const item of within(menu).getAllByRole('menuitem'))
			expect(item).toHaveAttribute('aria-disabled', 'true');
		await user.keyboard('{Escape}');

		tray.markFinished('job-r');
		await waitFor(() => expect(screen.getByRole('button', { name: 'Deploy' })).toBeEnabled());
		expect(screen.getByRole('button', { name: 'Stop' })).toBeEnabled();
		expect(screen.getByRole('button', { name: 'Rename Silo' })).toBeEnabled();
	});

	it('knows about a running rename after a reload, from the stack’s jobs', async () => {
		jobList = [{ id: 'job-r', kind: 'stack.rename', state: 'running' }];
		header(renamable());
		await waitFor(() => expect(screen.getByRole('button', { name: 'Deploy' })).toBeDisabled());
		expect(screen.getByRole('button', { name: 'Stop' })).toHaveAttribute(
			'title',
			'Renaming Silo…'
		);
	});
});

describe('ServicesTable', () => {
	const services: StackServiceStatus[] = [
		{
			name: 'web',
			description: 'Web frontend',
			image: 'nginx:1.27',
			build: false,
			dependsOn: [],
			drift: [],
			status: 'running',
			containers: [
				{
					name: 'silo-web-1',
					state: 'running',
					view: 'full',
					restartPolicy: 'unless-stopped',
					ports: [{ privatePort: 80, publicPort: 8080, protocol: 'tcp' }]
				}
			]
		},
		{
			name: 'worker',
			image: 'worker:1',
			build: false,
			dependsOn: [],
			drift: [],
			status: 'exited',
			containers: [{ name: 'silo-worker-1', state: 'exited', view: 'full' }]
		}
	] as StackServiceStatus[];

	it('links ports and the open action only with a service address, and terminals to the service', () => {
		const { unmount } = render(ServicesTable, {
			props: { stack: stack(), services, usage: null, serviceAddress: '192.168.1.10' }
		});
		expect(screen.getByRole('link', { name: '8080:80' })).toHaveAttribute(
			'href',
			'http://192.168.1.10:8080'
		);
		expect(screen.getByRole('link', { name: 'Open web' })).toHaveAttribute('target', '_blank');
		// Terminals need container.exec on the stack.
		expect(
			screen.queryByRole('link', { name: 'Open a Terminal in web' })
		).not.toBeInTheDocument();
		expect(screen.getByText('Web frontend')).toBeInTheDocument();
		unmount();

		render(ServicesTable, {
			props: { stack: stack({ actions: [...ALL, 'container.exec'] }), services, usage: null }
		});
		expect(screen.queryByRole('link', { name: '8080:80' })).not.toBeInTheDocument();
		expect(screen.getByText('8080:80')).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Open web' })).not.toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Open a Terminal in web' })).toHaveAttribute(
			'href',
			'/stacks/st-1/terminal?container=silo-web-1'
		);
		// No terminal into a stopped service.
		expect(
			screen.queryByRole('link', { name: 'Open a Terminal in worker' })
		).not.toBeInTheDocument();
	});

	it('shows every service with the same service tile, whatever its image or a leftover icon', () => {
		const { container } = render(ServicesTable, {
			props: {
				stack: stack(),
				services: [
					{ ...services[0], image: 'postgres:16', icon: 'database' },
					services[1]
				] as StackServiceStatus[],
				usage: null
			}
		});
		const tiles = [...container.querySelectorAll('.svc > [data-color]')];
		expect(tiles).toHaveLength(2);
		for (const tile of tiles) {
			expect(tile).toHaveAttribute('data-color', 'blue');
			expect(tile.querySelector('svg')).toHaveClass('lucide-workflow');
		}
	});

	it('offers each service its own start, stop and restart', async () => {
		const user = setup();
		const onoperate = vi.fn();
		render(ServicesTable, { props: { stack: stack(), services, usage: null, onoperate } });
		await user.click(screen.getByRole('button', { name: 'More Actions for web' }));
		expect((await screen.findAllByRole('menuitem')).map((i) => i.textContent?.trim())).toEqual([
			'Restart web',
			'Stop web'
		]);
		await user.click(screen.getByRole('menuitem', { name: 'Stop web' }));
		expect(onoperate).toHaveBeenCalledWith('web', 'stop');
		await user.click(screen.getByRole('button', { name: 'More Actions for worker' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Start worker' }));
		expect(onoperate).toHaveBeenCalledWith('worker', 'start');
	});

	it('orders the columns like the containers list and links the networks', () => {
		const day = 86400;
		const web = services[0];
		const live = [
			{
				...web,
				containers: [
					{
						...web.containers[0],
						startedAt: new Date(Date.now() - (2 * day + 3600) * 1000).toISOString(),
						networks: [
							{ name: 'silo_default', ipAddress: '172.18.0.2' },
							{ name: 'edge', ipAddress: '10.0.0.5' }
						]
					}
				]
			},
			services[1]
		] as StackServiceStatus[];
		render(ServicesTable, {
			props: {
				stack: stack(),
				services: live,
				usage: {
					cpu: [],
					cpuNow: 12.5,
					memoryNow: 64 << 20,
					containers: { 'silo-web-1': { cpu: 12.5, memory: 64 << 20 } }
				}
			}
		});
		const headers = screen.getAllByRole('columnheader').map((h) => h.textContent ?? '');
		const at = (name: string) => headers.findIndex((h) => h.includes(name));
		expect(
			[
				'Status',
				'Containers',
				'CPU',
				'Memory',
				'Uptime',
				'Image',
				'Volumes',
				'Networks',
				'Ports'
			].map(at)
		).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9]);
		expect(at('Restart Policy')).toBe(-1);
		expect(at('Image Update')).toBe(-1);
		expect(screen.getByText('2d 1h 0m')).toBeInTheDocument();
		expect(screen.getByText('12.5%')).toBeInTheDocument();
		expect(screen.getByText('64 MB')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'silo_default' })).toHaveAttribute(
			'href',
			routes.network('env-1', 'silo_default')
		);
		expect(screen.getByText('172.18.0.2')).toBeInTheDocument();
		// One network per row (rows keep one height); the rest in the tooltip.
		expect(screen.queryByRole('link', { name: 'edge' })).not.toBeInTheDocument();
		expect(screen.getByText('+1 more')).toHaveAttribute('title', 'edge: 10.0.0.5');
	});

	it('links images and volumes to their pages, anonymous volumes marked', () => {
		const anon = 'ab12'.repeat(16);
		const [web, worker] = services;
		const live = [
			{
				...web,
				applied: {
					service: 'web',
					image: 'nginx:1.27',
					imageId: 'sha256:old',
					build: false
				},
				containers: [
					{
						...web.containers[0],
						imageId: 'sha256:run',
						volumes: [
							{ name: 'silo_data', destination: '/data' },
							{ name: 'silo_conf', destination: '/etc/app', readOnly: true }
						]
					}
				]
			},
			{
				...worker,
				containers: [
					{
						...worker.containers[0],
						volumes: [
							{ name: 'silo_data', destination: '/data' },
							{ name: anon, destination: '/cache', anonymous: true },
							{ name: 'silo_spool', destination: '/spool' }
						]
					}
				]
			}
		] as StackServiceStatus[];
		render(ServicesTable, { props: { stack: stack(), services: live, usage: null } });
		// The image the container runs, not the one the last deploy applied.
		expect(screen.getByRole('link', { name: 'nginx:1.27' })).toHaveAttribute(
			'href',
			routes.image('env-1', 'sha256:run')
		);
		// Without a known image ID the reference is plain text.
		expect(screen.queryByRole('link', { name: 'worker:1' })).not.toBeInTheDocument();
		expect(screen.getByText('worker:1')).toBeInTheDocument();
		const rows = screen.getAllByRole('row');
		const webRow = rows.find((r) => within(r).queryByText('Web frontend'))!;
		expect(within(webRow).getByRole('link', { name: 'silo_data' })).toHaveAttribute(
			'href',
			routes.volume('env-1', 'silo_data')
		);
		expect(within(webRow).getByRole('link', { name: 'silo_conf' })).toHaveAttribute(
			'href',
			routes.volume('env-1', 'silo_conf')
		);
		// Past two volumes the second line says how many more (named first,
		// then anonymous ones).
		const workerRow = rows.find((r) => within(r).queryByText('worker:1'))!;
		expect(within(workerRow).getByRole('link', { name: 'silo_data' })).toBeInTheDocument();
		expect(
			within(workerRow).queryByRole('link', { name: 'silo_spool' })
		).not.toBeInTheDocument();
		expect(within(workerRow).getByText('+2 more')).toHaveAttribute(
			'title',
			`silo_spool at /spool\nAnonymous volume ${anon} at /cache`
		);
	});

	it('shows an anonymous volume by its mount path and links it', () => {
		const anon = 'cd34'.repeat(16);
		const [web] = services;
		const live = [
			{
				...web,
				containers: [
					{
						...web.containers[0],
						volumes: [{ name: anon, destination: '/cache', anonymous: true }]
					}
				]
			}
		] as StackServiceStatus[];
		render(ServicesTable, { props: { stack: stack(), services: live, usage: null } });
		expect(screen.getByRole('link', { name: `Anonymous Volume ${anon}` })).toHaveAttribute(
			'href',
			routes.volume('env-1', anon)
		);
		expect(screen.getByText('/cache')).toBeInTheDocument();
	});

	it('links a single-container service to its container and its logs to the service', async () => {
		const user = setup();
		render(ServicesTable, {
			props: {
				stack: stack({
					actions: [...ALL, 'container.details.read', 'container.logs.read']
				}),
				services,
				usage: null
			}
		});
		expect(screen.getByRole('link', { name: 'web' })).toHaveAttribute(
			'href',
			routes.container('env-1', 'silo-web-1')
		);
		await user.click(screen.getByRole('button', { name: 'More Actions for web' }));
		expect(await screen.findByRole('menuitem', { name: 'Logs of web' })).toHaveAttribute(
			'href',
			routes.stackLogs('st-1', 'web')
		);
	});

	it('invites a deploy when the stack has no services yet', async () => {
		const user = setup();
		const ondeploy = vi.fn();
		render(ServicesTable, { props: { stack: stack(), services: [], usage: null, ondeploy } });
		expect(
			screen.getByRole('heading', { name: 'No Services Running Yet' })
		).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Deploy' }));
		expect(ondeploy).toHaveBeenCalled();
	});

	it("disables restart and stop of Docker Manager's own stack (not start)", async () => {
		const user = setup();
		render(ServicesTable, {
			props: {
				stack: stack({
					protection: {
						role: 'docker_manager_project',
						reason: "Docker Manager's own Compose project",
						self: true,
						restartAllowed: false
					}
				}),
				services,
				usage: null,
				onoperate: vi.fn()
			}
		});
		await user.click(screen.getByRole('button', { name: 'More Actions for web' }));
		expect(await screen.findByRole('menuitem', { name: 'Restart web' })).toHaveAttribute(
			'aria-disabled',
			'true'
		);
		expect(screen.getByRole('menuitem', { name: 'Stop web' })).toHaveAttribute(
			'aria-disabled',
			'true'
		);
		await user.keyboard('{Escape}');
		await user.click(screen.getByRole('button', { name: 'More Actions for worker' }));
		expect(await screen.findByRole('menuitem', { name: 'Start worker' })).not.toHaveAttribute(
			'aria-disabled',
			'true'
		);
	});
});

describe('StackIcon', () => {
	const icon = (s: Partial<Stack>) => {
		const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const r = render(QueryHarness, {
			props: {
				client,
				component: StackIcon as unknown as Component<Record<string, unknown>>,
				props: { stack: s, size: 'xs' }
			}
		});
		return { ...r, client };
	};
	const stackTile = (container: HTMLElement) => {
		const tile = container.querySelector('[data-color]');
		expect(tile).toHaveAttribute('data-color', 'blue');
		expect(tile?.querySelector('svg')).toHaveClass('lucide-layers');
		expect(container.querySelector('img')).toBeNull();
	};

	it('shows the stack tile, never an icon of the stack’s own', () => {
		const { container } = icon({ icon: 'database' });
		stackTile(container);
		expect(seen.some((r) => r.path.endsWith('/template-icons'))).toBe(false);
	});

	it('shows the image of the template the stack was created from', async () => {
		const { container } = icon({
			template: {
				instanceId: 'inst-1',
				templateId: 'tpl-1',
				name: 'Nextcloud',
				version: 1,
				versionLabel: '1.0.0'
			}
		});
		await waitFor(() =>
			expect(container.querySelector('img')).toHaveAttribute(
				'src',
				'/api/v1/templates/tpl-1/icon?v=abc'
			)
		);
	});

	it('keeps the stack tile when the template has no image', async () => {
		const { container, client } = icon({
			template: {
				instanceId: 'inst-1',
				templateId: 'tpl-2',
				name: 'Wiki',
				version: 1,
				versionLabel: '1.0.0'
			}
		});
		await waitFor(() => expect(client.getQueryData(templateKeys.icons())).toBeDefined());
		stackTile(container);
	});
});

describe('DiffView', () => {
	it('marks additions and removals in text, not only colour, and expands unchanged lines', async () => {
		const user = setup();
		const before = Array.from({ length: 12 }, (_, i) => `line ${i + 1}`).join('\n');
		const after = before.replace('line 6', 'line six');
		render(DiffView, {
			props: {
				title: 'compose.yaml',
				before,
				after,
				beforeLabel: 'Revision 2',
				afterLabel: 'On Disk'
			}
		});
		const region = screen.getByRole('region', { name: 'Changes in compose.yaml' });
		expect(
			within(region).getByText('1 lines added, 1 lines removed (Revision 2 to On Disk)')
		).toBeInTheDocument();
		expect(within(region).getByText('Removed:')).toBeInTheDocument();
		expect(within(region).getByText('Added:')).toBeInTheDocument();
		await user.click(within(region).getByRole('button', { name: 'Show 2 Unchanged Lines' }));
		expect(within(region).getByText('line 1')).toBeInTheDocument();
		expect(
			within(region).getByRole('button', { name: 'Hide Unchanged Lines' })
		).toBeInTheDocument();
	});
});

describe('ValidationResult', () => {
	it('lists errors and warnings with their codes and flags external binds', () => {
		render(ValidationResult, {
			props: {
				validation: {
					valid: true,
					errors: [],
					warnings: [
						{
							code: 'obsolete_version',
							message: 'the top-level version key is obsolete'
						}
					],
					services: [{ name: 'web', image: 'nginx', build: false, dependsOn: [] }],
					binds: [
						{
							service: 'web',
							source: '/srv/media',
							target: '/media',
							external: true,
							readOnly: true
						}
					]
				}
			}
		});
		expect(screen.getByText(/Valid: 1\s+service/)).toBeInTheDocument();
		expect(
			within(screen.getByRole('list', { name: 'Warnings' })).getByText('obsolete_version')
		).toBeInTheDocument();
		expect(screen.getByText('/srv/media')).toBeInTheDocument();
	});

	it('lists each bind source outside the project once, not as a warning per bind', () => {
		const bind = (service: string, source: string, target: string) => ({
			service,
			source,
			target,
			external: true,
			readOnly: false
		});
		const outside = (service: string, source: string) => ({
			code: 'bind_outside_project',
			service,
			message: `service ${service} binds ${source} from outside the project directory`
		});
		render(ValidationResult, {
			props: {
				validation: {
					valid: true,
					errors: [],
					warnings: [
						outside('radarr', '/mnt'),
						outside('sonarr', '/mnt'),
						outside('silo', '/proc/stat')
					],
					services: [
						{ name: 'radarr', image: 'radarr', build: false, dependsOn: [] },
						{ name: 'sonarr', image: 'sonarr', build: false, dependsOn: [] },
						{ name: 'silo', image: 'silo', build: false, dependsOn: [] }
					],
					binds: [
						bind('radarr', '/mnt', '/mnt'),
						bind('sonarr', '/mnt', '/mnt'),
						bind('silo', '/mnt', '/data'),
						bind('silo', '/proc/stat', '/host/stat')
					]
				}
			}
		});
		expect(screen.queryByRole('list', { name: 'Warnings' })).toBeNull();
		expect(screen.queryByText(/warnings?$/)).toBeNull();
		expect(screen.getAllByText('/mnt')).toHaveLength(1);
		expect(screen.getAllByText('/proc/stat')).toHaveLength(1);
	});

	it('keeps outside-the-project warnings no bind line covers (a definition file)', () => {
		render(ValidationResult, {
			props: {
				validation: {
					valid: true,
					errors: [],
					warnings: [
						{
							code: 'bind_outside_project',
							message:
								'definition file /srv/shared.env is outside the project directory and is not part of stack revisions or backups'
						}
					],
					services: [{ name: 'web', image: 'nginx', build: false, dependsOn: [] }],
					binds: []
				}
			}
		});
		const list = screen.getByRole('list', { name: 'Warnings' });
		expect(within(list).getAllByRole('listitem')).toHaveLength(1);
		expect(list).toHaveTextContent('definition file /srv/shared.env');
	});
});
