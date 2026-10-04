// Notification channel dialog (#142): friendly fields per service (secrets
// in password fields) become one Shoutrrr URL; switching the service
// swaps the fields; editing keeps the stored address masked until "Show
// Address" reads it back, and saves without it when it is unchanged. What
// a channel sends is chosen on the page (SubscriptionMatrix.test.ts): a new
// channel sends everything. The built-in In App channel has no name or
// address to edit.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import { toast } from '$lib/ui';
import QueryHarness from '../../../test/QueryHarness.svelte';
import { choose } from '../../../test/select';
import ChannelDialog from './ChannelDialog.svelte';
import { allEvents, type NotificationChannel } from './model';

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

const channel: NotificationChannel = {
	id: 'c-1',
	name: 'Ops',
	service: 'discord',
	builtIn: false,
	enabled: true,
	events: [
		{ kind: 'updates', outcomes: ['available', 'failure', 'success'] },
		{ kind: 'job_failed', outcomes: ['failure'] }
	],
	allEnvironments: true,
	environmentIds: [],
	address: { fingerprint: 'fp_1', version: 1, updatedAt: '2026-09-30T09:00:00Z' },
	lastResult: 'ok',
	revision: 3,
	createdAt: '2026-09-30T09:00:00Z',
	updatedAt: '2026-09-30T09:00:00Z'
};

const STORED = 'discord://tok-123@123456789012345678';

const ONE_ENVIRONMENT = [{ id: 'e1', name: 'prod', status: 'active', online: true }];

/** Stubs the API; returns the requests other than the environments list. */
function stubApi(environments: object[] = ONE_ENVIRONMENT) {
	const calls: { method: string; path: string; body: unknown }[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request) => {
			const url = new URL(input.url);
			if (url.pathname === '/api/v1/environments')
				return json({ items: environments, nextCursor: null });
			const body =
				input.method === 'GET' ? undefined : await input.json().catch(() => undefined);
			calls.push({ method: input.method, path: url.pathname, body });
			if (url.pathname === '/api/v1/notification-channels/c-1/address')
				return json({ address: STORED });
			if (input.method === 'POST')
				return json({ ...channel, ...(body as object), id: 'c-2', revision: 1 }, 201);
			if (input.method === 'PATCH')
				return json({ ...channel, ...(body as object), revision: 4 });
			return json({ items: [], nextCursor: null });
		})
	);
	return calls;
}

function show(props: ComponentProps<typeof ChannelDialog>) {
	render(QueryHarness<ComponentProps<typeof ChannelDialog>>, {
		props: {
			client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
			component: ChannelDialog,
			props
		}
	});
}

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

afterEach(() => {
	vi.unstubAllGlobals();
	toast.clear();
});

describe('ChannelDialog (#142)', () => {
	it('adds a Discord channel from its webhook URL, subscribed to everything by default', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true });
		expect(await screen.findByRole('heading', { name: 'Destination' })).toBeInTheDocument();
		// What it sends is chosen on the page's bells, not here.
		expect(screen.queryByRole('heading', { name: 'What to Send' })).toBeNull();
		expect(screen.queryByRole('checkbox', { name: 'Backups' })).toBeNull();
		expect(screen.getByRole('switch', { name: 'Enabled' })).toHaveAttribute(
			'aria-checked',
			'true'
		);
		// One environment: no environment picker.
		expect(screen.queryByRole('combobox', { name: /^Environments/ })).toBeNull();

		await user.type(screen.getByRole('textbox', { name: /^Name/ }), 'Ops');
		await choose(user, screen.getByRole('combobox', { name: /^Service/ }), 'Discord');
		const webhook = screen.getByLabelText(/^Webhook URL/);
		expect(webhook).toHaveAttribute('type', 'password');
		await user.type(webhook, 'https://discord.com/api/webhooks/123456789012345678/tok-123');
		await user.click(screen.getByRole('button', { name: 'Add Channel' }));

		await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
		const post = calls.find((c) => c.method === 'POST')!;
		expect(post.path).toBe('/api/v1/notification-channels');
		expect(post.body).toEqual({
			name: 'Ops',
			address: STORED,
			enabled: true,
			allEnvironments: true,
			environmentIds: []
		});
		await waitFor(() => expect(toast.items.map((t) => t.title)).toContain('Added Ops'));
		expect(toast.items.find((t) => t.title === 'Added Ops')?.action?.label).toBe('Send Test');
	});

	it('says what is missing and sends nothing', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true });
		await user.click(await screen.findByRole('button', { name: 'Add Channel' }));
		expect(await screen.findByText('Enter a name.')).toBeInTheDocument();
		expect(screen.getByText('Choose a service.')).toBeInTheDocument();
		await user.type(screen.getByRole('textbox', { name: /^Name/ }), 'Mail');
		await choose(user, screen.getByRole('combobox', { name: /^Service/ }), 'Email (SMTP)');
		await user.click(screen.getByRole('button', { name: 'Add Channel' }));
		expect(await screen.findByText('Enter the SMTP server.')).toBeInTheDocument();
		expect(calls).toEqual([]);
	});

	it('swaps the fields when the service changes', async () => {
		const user = setup();
		stubApi();
		show({ open: true });
		await choose(
			user,
			await screen.findByRole('combobox', { name: /^Service/ }),
			'Email (SMTP)'
		);
		expect(screen.getByRole('textbox', { name: /^SMTP Server/ })).toBeInTheDocument();
		expect(screen.getByRole('textbox', { name: /^Port/ })).toHaveValue('587');
		expect(screen.getByLabelText(/^Password/)).toHaveAttribute('type', 'password');
		expect(screen.getByRole('combobox', { name: /^Encryption/ })).toHaveTextContent('Auto');
		await choose(user, screen.getByRole('combobox', { name: /^Service/ }), 'Telegram');
		expect(screen.queryByRole('textbox', { name: /^SMTP Server/ })).toBeNull();
		expect(screen.getByLabelText(/^Bot Token/)).toHaveAttribute('type', 'password');
		expect(screen.getByRole('textbox', { name: /^Chats/ })).toBeInTheDocument();
	});

	it('keeps the stored address masked until Show Address, and saves without it when unchanged', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true, channel });
		expect(await screen.findByText('The address is stored encrypted')).toBeInTheDocument();
		expect(screen.queryByLabelText(/^Webhook URL/)).toBeNull();
		expect(calls).toEqual([]);

		await user.click(screen.getByRole('button', { name: 'Show Address' }));
		const webhook = await screen.findByLabelText(/^Webhook URL/);
		expect(webhook).toHaveValue('https://discord.com/api/webhooks/123456789012345678/tok-123');
		// Asked for: shown in plain text, no second click on the eye.
		expect(webhook).toHaveAttribute('type', 'text');
		expect(calls).toEqual([
			{ method: 'GET', path: '/api/v1/notification-channels/c-1/address', body: undefined }
		]);

		await user.click(screen.getByRole('switch', { name: 'Enabled' }));
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
		const patch = calls.find((c) => c.method === 'PATCH')!;
		expect(patch.path).toBe('/api/v1/notification-channels/c-1');
		// Its events are left as they are.
		expect(patch.body).toEqual({
			name: 'Ops',
			enabled: false,
			allEnvironments: true,
			environmentIds: []
		});
		await waitFor(() => expect(toast.items.map((t) => t.title)).toContain('Saved Ops'));
	});

	it('edits the In App channel without a name or an address', async () => {
		const user = setup();
		const calls = stubApi();
		const inApp: NotificationChannel = {
			...channel,
			id: '00000000-0000-0000-0000-000000000000',
			name: 'In App',
			service: 'app',
			builtIn: true,
			events: allEvents(),
			lastResult: undefined,
			address: { fingerprint: '', version: 0, updatedAt: '2026-10-03T09:00:00Z' }
		};
		show({ open: true, channel: inApp });
		expect(await screen.findByText('Notices in Docker Manager')).toBeInTheDocument();
		expect(screen.queryByLabelText(/^Name/)).toBeNull();
		expect(screen.queryByRole('combobox', { name: /Service/ })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Show Address' })).toBeNull();
		await user.click(screen.getByRole('switch', { name: 'Enabled' }));
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
		const patch = calls.find((c) => c.method === 'PATCH')!;
		expect(patch.path).toBe(
			'/api/v1/notification-channels/00000000-0000-0000-0000-000000000000'
		);
		const body = patch.body as Record<string, unknown>;
		expect(body).toEqual({ enabled: false, allEnvironments: true, environmentIds: [] });
	});

	it('sends a changed address after Show Address', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true, channel });
		await user.click(await screen.findByRole('button', { name: 'Show Address' }));
		const webhook = await screen.findByLabelText(/^Webhook URL/);
		await user.clear(webhook);
		await user.type(webhook, 'https://discord.com/api/webhooks/42/new-token');
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
		expect(
			(calls.find((c) => c.method === 'PATCH')!.body as { address?: string }).address
		).toBe('discord://new-token@42');
	});

	it('keeps a filter on an archived environment instead of widening it to all', async () => {
		const user = setup();
		const calls = stubApi([
			{ id: 'e1', name: 'prod', status: 'active', online: true },
			{
				id: 'e2',
				name: 'old-lab',
				status: 'archived',
				online: false,
				archivedAt: '2026-09-30T08:00:00Z'
			},
			{ id: 'e3', name: 'staging', status: 'active', online: true }
		]);
		show({
			open: true,
			channel: { ...channel, allEnvironments: false, environmentIds: ['e2'] }
		});
		const archived = await screen.findByRole('checkbox', { name: 'old-lab (archived)' });
		expect(archived).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'prod' })).not.toBeChecked();
		expect(screen.getByRole('combobox', { name: /^Environments/ })).toHaveTextContent(
			'Some Environments'
		);

		// Unticking the last one blocks the save; it never means every environment.
		await user.click(archived);
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		expect(await screen.findByText('Choose at least one environment.')).toBeInTheDocument();
		expect(calls.filter((c) => c.method === 'PATCH')).toEqual([]);

		// Kept as it is, the archived environment is sent back unchanged.
		await user.click(archived);
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
		expect(calls.find((c) => c.method === 'PATCH')!.body).toMatchObject({
			allEnvironments: false,
			environmentIds: ['e2']
		});
	});
});
