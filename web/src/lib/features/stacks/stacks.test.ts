import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Environment } from '$lib/api/client';
import DiffView from '$lib/ui/DiffView.svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import type { Stack, StackServiceStatus } from './queries';
import ServicesTable from './ServicesTable.svelte';
import StackHeader from './StackHeader.svelte';
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

// The manager as seen by the components: image status with an update, and
// 202 jobs for every mutation.
beforeEach(() => {
	seen = [];
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
		expect(screen.getByRole('button', { name: 'Copy host path' })).toBeInTheDocument();

		expect(screen.getByRole('button', { name: 'Deploy' })).toBeEnabled();
		expect(screen.getByRole('button', { name: 'More deploy options' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Restart' })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument();
		expect(
			await screen.findByRole('button', { name: /Update.*update available/ })
		).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'More stack actions' }));
		const menu = await screen.findByRole('menu');
		expect(
			within(menu)
				.getAllByRole('menuitem')
				.map((i) => i.textContent?.trim())
		).toEqual(['Take down', 'Migrate', 'Edit details', 'Delete']);
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
		for (const name of ['Restart', 'Stop', 'Start', 'More stack actions'])
			expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /Update/ })).not.toBeInTheDocument();
		// No host path for a minimal view.
		expect(screen.queryByRole('button', { name: 'Copy host path' })).not.toBeInTheDocument();
	});

	it('offers Start for a stopped stack and disables actions while the environment is offline', () => {
		header(
			stack({
				engine: { state: 'stopped', services: [] },
				readOnly: true,
				environmentOnline: false
			})
		);
		expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled();
		expect(screen.queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Deploy' })).toBeDisabled();
		expect(screen.getByText('Read-only while homelab is offline')).toBeInTheDocument();
	});

	it('stops only after the confirmation that lists what happens, and tracks the job', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(screen.getByRole('button', { name: 'Stop' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Stop Silo?' });
		expect(
			within(dialog).getByText('Stops 3 containers in reverse dependency order.')
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
		await user.click(screen.getByRole('button', { name: 'More stack actions' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Delete' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Delete Silo?' });
		expect(
			within(dialog).getByText('Keeps its volumes and the project directory on the host.')
		).toBeInTheDocument();
		const confirm = within(dialog).getByRole('button', { name: 'Delete stack' });
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
		await user.click(screen.getByRole('button', { name: 'More stack actions' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Delete' }));
		const dialog = await screen.findByRole('alertdialog', { name: 'Delete Silo?' });
		const box = within(dialog).getByRole('checkbox', {
			name: /Also remove the stack’s volumes/
		});
		expect(box).not.toBeChecked();
		await user.click(box);
		expect(
			within(dialog).getByText(/Removes the volumes the stack owns and all data in them/)
		).toBeInTheDocument();
		await user.type(within(dialog).getByRole('textbox'), 'silo');
		await user.click(within(dialog).getByRole('button', { name: 'Delete stack and volumes' }));
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

	it('pulls the images without deploying them', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(screen.getByRole('button', { name: 'Pull' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Pull Silo'));
		const post = seen.find((s) => s.method === 'POST');
		expect(post?.path).toBe('/api/v1/stacks/st-1/pulls');
		expect(seen.some((s) => s.path.endsWith('/deployments'))).toBe(false);
	});

	it('hides Pull without stack.update', () => {
		header(stack({ actions: ALL.filter((a) => a !== 'stack.update') }));
		expect(screen.queryByRole('button', { name: 'Pull' })).not.toBeInTheDocument();
	});

	it('removes orphaned containers only from the deploy menu, after a confirmation', async () => {
		const user = setup();
		const tray = header(stack());
		await user.click(screen.getByRole('button', { name: 'More deploy options' }));
		const menu = await screen.findByRole('menu');
		expect(
			within(menu)
				.getAllByRole('menuitem')
				.map((i) => i.textContent?.trim())
		).toEqual(['Deploy', 'Deploy and remove orphaned containers…']);
		await user.click(
			within(menu).getByRole('menuitem', { name: 'Deploy and remove orphaned containers…' })
		);
		const dialog = await screen.findByRole('alertdialog', {
			name: 'Deploy Silo and remove orphaned containers?'
		});
		expect(seen.filter((s) => s.method === 'POST')).toEqual([]);
		await user.click(within(dialog).getByRole('button', { name: 'Deploy and remove orphans' }));
		await waitFor(() => expect(tray.jobs[0]?.title).toBe('Deploy Silo and remove orphans'));
		expect(seen.find((s) => s.method === 'POST')).toMatchObject({
			path: '/api/v1/stacks/st-1/deployments',
			body: { removeOrphans: true }
		});
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
			screen.queryByRole('link', { name: 'Open a terminal in web' })
		).not.toBeInTheDocument();
		expect(screen.getByText('Web frontend')).toBeInTheDocument();
		// jsdom matches no media query: below 1280 px the restart policy
		// column gives way to the others.
		expect(
			screen.queryByRole('columnheader', { name: 'Restart policy' })
		).not.toBeInTheDocument();
		unmount();

		render(ServicesTable, {
			props: { stack: stack({ actions: [...ALL, 'container.exec'] }), services, usage: null }
		});
		expect(screen.queryByRole('link', { name: '8080:80' })).not.toBeInTheDocument();
		expect(screen.getByText('8080:80')).toBeInTheDocument();
		expect(screen.queryByRole('link', { name: 'Open web' })).not.toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Open a terminal in web' })).toHaveAttribute(
			'href',
			'/stacks/st-1/terminal?container=silo-web-1'
		);
		// No terminal into a stopped service.
		expect(
			screen.queryByRole('link', { name: 'Open a terminal in worker' })
		).not.toBeInTheDocument();
	});

	it('offers each service its own start, stop and restart', async () => {
		const user = setup();
		const onoperate = vi.fn();
		render(ServicesTable, { props: { stack: stack(), services, usage: null, onoperate } });
		await user.click(screen.getByRole('button', { name: 'More actions for web' }));
		expect((await screen.findAllByRole('menuitem')).map((i) => i.textContent?.trim())).toEqual([
			'Restart web',
			'Stop web'
		]);
		await user.click(screen.getByRole('menuitem', { name: 'Stop web' }));
		expect(onoperate).toHaveBeenCalledWith('web', 'stop');
		await user.click(screen.getByRole('button', { name: 'More actions for worker' }));
		await user.click(await screen.findByRole('menuitem', { name: 'Start worker' }));
		expect(onoperate).toHaveBeenCalledWith('worker', 'start');
	});

	it('shows uptime, CPU and memory right after the status, and the addresses', () => {
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
		expect([at('Status'), at('Uptime'), at('CPU'), at('Memory')]).toEqual([1, 2, 3, 4]);
		expect(at('IP addresses')).toBeLessThan(at('Image'));
		expect(screen.getByText('2d 1h 0m')).toBeInTheDocument();
		expect(screen.getByText('12.5%')).toBeInTheDocument();
		expect(screen.getByText('64 MB')).toBeInTheDocument();
		expect(screen.getByText('172.18.0.2')).toBeInTheDocument();
		expect(screen.getByText('+1')).toBeInTheDocument();
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
		await user.click(screen.getByRole('button', { name: 'More actions for web' }));
		expect(await screen.findByRole('menuitem', { name: 'Restart web' })).toHaveAttribute(
			'aria-disabled',
			'true'
		);
		expect(screen.getByRole('menuitem', { name: 'Stop web' })).toHaveAttribute(
			'aria-disabled',
			'true'
		);
		await user.keyboard('{Escape}');
		await user.click(screen.getByRole('button', { name: 'More actions for worker' }));
		expect(await screen.findByRole('menuitem', { name: 'Start worker' })).not.toHaveAttribute(
			'aria-disabled',
			'true'
		);
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
				afterLabel: 'On disk'
			}
		});
		const region = screen.getByRole('region', { name: 'Changes in compose.yaml' });
		expect(
			within(region).getByText('1 lines added, 1 lines removed (Revision 2 to On disk)')
		).toBeInTheDocument();
		expect(within(region).getByText('Removed:')).toBeInTheDocument();
		expect(within(region).getByText('Added:')).toBeInTheDocument();
		await user.click(within(region).getByRole('button', { name: 'Show 2 unchanged lines' }));
		expect(within(region).getByText('line 1')).toBeInTheDocument();
		expect(
			within(region).getByRole('button', { name: 'Hide unchanged lines' })
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
});
