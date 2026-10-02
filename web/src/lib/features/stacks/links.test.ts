import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Environment } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import EditDetailsDialog from './EditDetailsDialog.svelte';
import type { Stack } from './queries';
import StackHeader from './StackHeader.svelte';
import { JobTray } from './tray.svelte';

// A stack's links: shown in its header (full view only) and edited in
// "Edit Details" with the server's rules checked inline.

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

let patches: unknown[] = [];
let answer: (body: Record<string, unknown>) => Response;

const json = (status: number, body: unknown) =>
	new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

beforeEach(() => {
	patches = [];
	answer = (body) => json(200, { ...stack(), ...body, revision: 4 });
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		if (req.method === 'PATCH') {
			const body = JSON.parse(await req.text());
			patches.push(body);
			return answer(body);
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

function stack(over: Partial<Stack> = {}): Stack {
	return {
		id: 'st-1',
		environmentId: 'env-1',
		name: 'silo',
		displayName: 'Silo',
		status: 'deployed',
		view: 'full',
		actions: ['stack.read', 'stack.manage'],
		revision: 3,
		environmentOnline: true,
		services: [],
		engine: { state: 'running', services: [] },
		links: [
			{ label: 'Documentation', url: 'https://docs.example.com/silo' },
			{ url: 'https://git.example.com/silo' }
		],
		...over
	} as Stack;
}

function show(component: Component<Record<string, unknown>>, props: Record<string, unknown>) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, { props: { client, component, props } });
}

const header = (s: Stack) =>
	show(StackHeader as unknown as Component<Record<string, unknown>>, {
		stack: s,
		environment: { id: 'env-1', name: 'homelab', online: true } as Environment,
		tray: new JobTray(),
		now: new Date('2026-09-25T10:00:00Z')
	});

describe('stack links in the header', () => {
	it('lists the links below the meta row, each opening in a new tab', () => {
		header(stack());
		const list = screen.getByRole('list', { name: 'Links of Silo' });
		const links = within(list).getAllByRole('link');
		expect(links.map((a) => a.textContent?.replace('(opens in a new tab)', '').trim())).toEqual(
			['Documentation', 'git.example.com']
		);
		expect(links[0]).toHaveAttribute('rel', 'noopener noreferrer');
	});

	it('shows nothing without links', () => {
		header(stack({ links: undefined }));
		expect(screen.queryByRole('list', { name: 'Links of Silo' })).not.toBeInTheDocument();
	});

	it('hides links from a minimal view', () => {
		header(stack({ view: 'minimal', actions: ['stack.deploy'] }));
		expect(screen.queryByRole('link', { name: /Documentation/ })).not.toBeInTheDocument();
	});
});

describe('Edit Details: links', () => {
	const dialog = () =>
		show(EditDetailsDialog as unknown as Component<Record<string, unknown>>, {
			stack: stack(),
			onclose: () => {}
		});

	it('saves the edited links in order with the details', async () => {
		const user = setup();
		dialog();
		const d = await screen.findByRole('dialog', { name: 'Edit Details of Silo' });
		expect(within(d).getByLabelText('URL of Link 1')).toHaveValue(
			'https://docs.example.com/silo'
		);
		await user.click(within(d).getByRole('button', { name: 'Remove Link 2' }));
		await user.click(within(d).getByRole('button', { name: 'Add Link' }));
		await user.type(within(d).getByLabelText('Label of Link 2'), 'Website');
		await user.type(within(d).getByLabelText('URL of Link 2'), 'https://silo.example.com');
		await user.click(within(d).getByRole('button', { name: 'Save Details' }));
		await waitFor(() => expect(patches).toHaveLength(1));
		expect((patches[0] as { links: unknown }).links).toEqual([
			{ label: 'Documentation', url: 'https://docs.example.com/silo' },
			{ label: 'Website', url: 'https://silo.example.com' }
		]);
	});

	it('edits the service descriptions and offers no icon choice', async () => {
		const user = setup();
		show(EditDetailsDialog as unknown as Component<Record<string, unknown>>, {
			// A leftover icon from an older server is neither shown nor sent.
			stack: stack({
				icon: 'database',
				services: [
					{
						name: 'web',
						image: 'nginx',
						build: false,
						dependsOn: [],
						description: 'Front',
						icon: 'globe'
					}
				]
			}),
			onclose: () => {}
		});
		const d = await screen.findByRole('dialog', { name: 'Edit Details of Silo' });
		expect(within(d).queryByText(/icon/i)).not.toBeInTheDocument();
		const svc = within(d).getByLabelText('Description of web');
		await user.clear(svc);
		await user.type(svc, 'Web frontend');
		await user.click(within(d).getByRole('button', { name: 'Save Details' }));
		await waitFor(() => expect(patches).toHaveLength(1));
		const body = patches[0] as Record<string, unknown>;
		expect(body).not.toHaveProperty('icon');
		expect(body.services).toEqual({ web: { description: 'Web frontend' } });
	});

	it('does not save invalid links and says why', async () => {
		const user = setup();
		dialog();
		const d = await screen.findByRole('dialog', { name: 'Edit Details of Silo' });
		await user.click(within(d).getByRole('button', { name: 'Add Link' }));
		await user.type(within(d).getByLabelText('URL of Link 3'), 'file:///etc/passwd');
		await user.click(within(d).getByRole('button', { name: 'Save Details' }));
		expect(within(d).getByLabelText('URL of Link 3')).toHaveAccessibleDescription(
			'Use a web address that starts with http:// or https://.'
		);
		expect(patches).toHaveLength(0);
	});

	it('shows the server’s refusal on the link it names', async () => {
		const user = setup();
		answer = () =>
			json(422, {
				code: 'validation_failed',
				message: 'is listed twice',
				details: [{ field: 'body.links[1].url', message: 'is listed twice' }],
				requestId: 'r',
				retryable: false
			});
		dialog();
		const d = await screen.findByRole('dialog', { name: 'Edit Details of Silo' });
		await user.click(within(d).getByRole('button', { name: 'Save Details' }));
		await waitFor(() =>
			expect(within(d).getByLabelText('URL of Link 2')).toHaveAccessibleDescription(
				'Is listed twice.'
			)
		);
		expect(patches).toHaveLength(1);
	});
});
