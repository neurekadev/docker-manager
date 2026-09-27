import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
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
		const confirm = within(d).getByRole('button', { name: 'Make public' });
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
		const d = await screen.findByRole('dialog', { name: 'Publish a version of Nextcloud' });
		expect(within(d).getByLabelText('Version', { exact: false })).toHaveValue('1.2.1');
		expect(within(d).queryByText('This template is public')).toBeNull();
		await user.click(within(d).getByRole('button', { name: 'Publish 1.2.1' }));
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
		const d = await screen.findByRole('dialog', { name: 'Publish a version of Nextcloud' });
		expect(within(d).getByText('This template is public')).toBeInTheDocument();
		const publish = within(d).getByRole('button', { name: 'Publish 1.2.1' });
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
		expect(screen.getByRole('link', { name: 'Open template Nextcloud' })).toHaveAttribute(
			'href',
			'/templates/tp-1'
		);
		expect(screen.getByText('v1.2.0')).toBeInTheDocument();
		expect(screen.getByText('Public')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: '#files' }));
		expect(onTag).toHaveBeenCalledWith('files');
	});
});
