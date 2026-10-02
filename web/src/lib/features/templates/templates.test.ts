import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import CreateFromTemplateDialog from './CreateFromTemplateDialog.svelte';
import DefinitionSummary from './DefinitionSummary.svelte';
import PublishDialog from './PublishDialog.svelte';
import type { Template } from './queries';
import TemplateCard from './TemplateCard.svelte';
import VisibilityDialog from './VisibilityDialog.svelte';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

interface Seen {
	method: string;
	path: string;
	headers: Headers;
	body: Record<string, unknown> | undefined;
}
let seen: Seen[] = [];

beforeEach(() => {
	seen = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const text = await req.text();
		const body = text ? JSON.parse(text) : undefined;
		seen.push({ method: req.method, path: url.pathname, headers: req.headers, body });
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname.endsWith('/visibility'))
			return json(200, { ...template, visibility: body?.visibility, revision: 3 });
		if (url.pathname.endsWith('/versions'))
			return json(201, { number: 2, label: body?.label, definition: [] });
		return json(404, { code: 'not_found', message: 'no', details: [], retryable: false });
	});
});
afterEach(() => vi.unstubAllGlobals());

const template = {
	id: 'tp-1',
	name: 'Nextcloud',
	visibility: 'private',
	view: 'full',
	actions: ['template.read', 'template.publish'],
	versions: 1,
	tags: ['cloud'],
	latest: { label: '1.2.0' },
	revision: 2
} as unknown as Template;

function mount(component: unknown, props: Record<string, unknown>) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: { client, component: component as Component<Record<string, unknown>>, props }
	});
}

describe('VisibilityDialog', () => {
	it('makes a template public only after the acknowledgement', async () => {
		const user = setup();
		mount(VisibilityDialog, { open: true, template });
		const d = await screen.findByRole('alertdialog', { name: 'Make Nextcloud public?' });
		expect(within(d).getByText(/including \.env/)).toBeInTheDocument();
		const confirm = within(d).getByRole('button', { name: 'Make Public' });
		expect(confirm).toBeDisabled();
		await user.click(within(d).getByLabelText(/becomes public/));
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]).toMatchObject({
			method: 'PUT',
			path: '/api/v1/templates/tp-1/visibility',
			body: { visibility: 'public', acknowledgePublic: true }
		});
		expect(seen[0].headers.get('If-Match')).toBe('"2"');
	});
});

describe('PublishDialog', () => {
	it('suggests the next label and publishes a private template without acknowledgement', async () => {
		const user = setup();
		mount(PublishDialog, { open: true, template });
		const d = await screen.findByRole('dialog', { name: 'Publish a Version of Nextcloud' });
		expect(within(d).getByLabelText('Version', { exact: false })).toHaveValue('1.2.1');
		expect(within(d).queryByText('This template is public')).toBeNull();
		await user.click(within(d).getByRole('button', { name: 'Publish Version 1.2.1' }));
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0]).toMatchObject({
			method: 'POST',
			path: '/api/v1/templates/tp-1/versions',
			body: { label: '1.2.1', notes: '' }
		});
	});

	it('asks public templates for the acknowledgement', async () => {
		const user = setup();
		mount(PublishDialog, { open: true, template: { ...template, visibility: 'public' } });
		const d = await screen.findByRole('dialog', { name: 'Publish a Version of Nextcloud' });
		expect(within(d).getByText('This template is public')).toBeInTheDocument();
		const publish = within(d).getByRole('button', { name: 'Publish Version 1.2.1' });
		expect(publish).toBeDisabled();
		await user.click(within(d).getByLabelText(/becomes public/));
		await user.click(publish);
		await waitFor(() => expect(seen).toHaveLength(1));
		expect(seen[0].body).toMatchObject({ acknowledgePublic: true });
	});
});

describe('TemplateCard', () => {
	it('links the card and filters by tag without navigating', async () => {
		const user = setup();
		const onTag = vi.fn();
		render(TemplateCard, {
			props: {
				href: '/templates/tp-1',
				name: 'Nextcloud',
				tags: ['cloud', 'files'],
				latest: '1.2.0',
				visibility: 'public',
				onTag
			}
		});
		expect(screen.getByRole('link', { name: 'Open Template Nextcloud' })).toHaveAttribute(
			'href',
			'/templates/tp-1'
		);
		expect(screen.getByText('Version 1.2.0')).toBeInTheDocument();
		expect(screen.getByText('Public')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'files' }));
		expect(onTag).toHaveBeenCalledWith('files');
	});

	it('is one button in a picker, with static tags', async () => {
		const user = setup();
		const onselect = vi.fn();
		const onTag = vi.fn();
		render(TemplateCard, {
			props: { onselect, onTag, name: 'Nextcloud', tags: ['cloud'] }
		});
		expect(screen.queryByRole('link')).toBeNull();
		expect(screen.getAllByRole('button')).toHaveLength(1);
		expect(screen.getByText('Draft Only')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Use Template Nextcloud' }));
		expect(onselect).toHaveBeenCalledTimes(1);
		expect(onTag).not.toHaveBeenCalled();
	});
});

describe('DefinitionSummary', () => {
	it('lists services, images, ports and .env names without values', async () => {
		render(DefinitionSummary, {
			props: {
				files: [
					{
						path: '.env',
						content: ['DB_PASSWORD=hunter2', 'ADMIN_EMAIL=', ''].join('\n')
					},
					{
						path: 'compose.override.yaml',
						content: ['services:', '  web:', '    ports: ["8443:443"]', ''].join('\n')
					},
					{
						path: 'compose.yaml',
						content: [
							'services:',
							'  web:',
							'    image: nginx:1.27',
							'    ports: ["8080:80"]',
							'  worker:',
							'    build: .',
							''
						].join('\n')
					}
				]
			}
		});
		const table = await screen.findByRole('table', { name: 'Services' });
		expect(within(table).getByText('nginx:1.27')).toBeInTheDocument();
		expect(within(table).getByText('8080:80, 8443:443')).toBeInTheDocument();
		expect(within(table).getByText("Built from the template's files")).toBeInTheDocument();
		const keys = screen.getByRole('list', { name: 'Settings in .env' });
		expect(within(keys).getByText('DB_PASSWORD')).toBeInTheDocument();
		expect(within(keys).getByText('ADMIN_EMAIL (no value)')).toBeInTheDocument();
		expect(document.body.textContent).not.toContain('hunter2');
	});
});

/** The API of the create-from-template flow; POSTed creations land in `posted`. */
function stubCreateFlow(posted: Seen[]) {
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const text = await req.text();
		const body = text ? JSON.parse(text) : undefined;
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		switch (url.pathname) {
			case '/api/v1/me/permissions':
				return json(200, {
					owner: true,
					entries: [],
					environments: [],
					catalogVersion: 1
				});
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
			case '/api/v1/template-catalog':
				return json(200, {
					items: [
						{
							instanceId: 'self',
							registryName: 'Home',
							own: true,
							templateId: 'tp-1',
							name: 'Next Cloud',
							tags: [],
							actions: ['template.use'],
							versions: [
								{ number: 3, label: '1.2.0', publishedAt: '', contentSize: 1 }
							]
						}
					]
				});
			case '/api/v1/templates/tp-1/versions/3/definition':
				return json(200, {
					version: { number: 3, label: '1.2.0', definition: [] },
					files: [
						{ path: 'compose.yaml', content: 'services: {}\n' },
						{ path: '.env', content: 'A=1\n' }
					]
				});
			case '/api/v1/stacks/template-creations':
				posted.push({
					method: req.method,
					path: url.pathname,
					headers: req.headers,
					body
				});
				return json(201, {
					stack: {
						id: 'st-9',
						environmentId: 'env-1',
						name: 'next-cloud',
						status: 'undeployed',
						view: 'full',
						actions: []
					},
					validation: {
						valid: true,
						errors: [],
						warnings: [],
						services: [],
						binds: []
					}
				});
		}
		return json(404, { code: 'not_found', message: 'no', details: [], retryable: false });
	});
}

describe('CreateFromTemplateDialog', () => {
	it('creates a stack from the preselected template with a suggested name', async () => {
		const { goto } = await import('$app/navigation');
		const user = setup();
		const posted: Seen[] = [];
		stubCreateFlow(posted);
		mount(CreateFromTemplateDialog, { open: true, environmentId: 'env-1', templateId: 'tp-1' });
		const d = await screen.findByRole('dialog', { name: 'Create Stack From Next Cloud' });
		await waitFor(() => expect(within(d).getByLabelText(/^Name/)).toHaveValue('next-cloud'));
		await user.click(within(d).getByRole('button', { name: 'Create Stack' }));
		await waitFor(() => expect(posted).toHaveLength(1));
		expect(posted[0].body).toMatchObject({
			environmentId: 'env-1',
			name: 'next-cloud',
			templateId: 'tp-1',
			version: 3
		});
		// The unchanged .env is not rewritten; the new stack opens.
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/stacks/st-9'));
	});

	it('chooses a template by clicking its card', async () => {
		const user = setup();
		stubCreateFlow([]);
		mount(CreateFromTemplateDialog, { open: true, environmentId: 'env-1' });
		const d = await screen.findByRole('dialog', { name: 'Create Stack From Template' });
		await user.click(await within(d).findByRole('button', { name: 'Use Template Next Cloud' }));
		await screen.findByRole('dialog', { name: 'Create Stack From Next Cloud' });
		await waitFor(() => expect(screen.getByLabelText(/^Name/)).toHaveValue('next-cloud'));
		expect(screen.queryByRole('link', { name: /Open Template/ })).toBeNull();
	});
});
