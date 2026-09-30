// Notification channel dialog (#142): friendly fields per service (secrets
// in password fields) become one Shoutrrr URL; switching the service
// swaps the fields; editing keeps the stored address masked until "Show
// address" reads it back, and saves without it when it is unchanged.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import { toast } from '$lib/ui';
import QueryHarness from '../../../test/QueryHarness.svelte';
import { choose } from '../../../test/select';
import ChannelDialog from './ChannelDialog.svelte';
import type { NotificationChannel } from './model';

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
	enabled: true,
	eventKinds: ['job_failed', 'updates_available'],
	sendResolved: false,
	environmentIds: [],
	address: { fingerprint: 'fp_1', version: 1, updatedAt: '2026-09-30T09:00:00Z' },
	lastResult: 'ok',
	revision: 3,
	createdAt: '2026-09-30T09:00:00Z',
	updatedAt: '2026-09-30T09:00:00Z'
};

const STORED = 'discord://tok-123@123456789012345678';

/** Stubs the API; returns the requests other than the environments list. */
function stubApi() {
	const calls: { method: string; path: string; body: unknown }[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request) => {
			const url = new URL(input.url);
			if (url.pathname === '/api/v1/environments')
				return json({
					items: [{ id: 'e1', name: 'prod', status: 'active', online: true }],
					nextCursor: null
				});
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
		expect(screen.getByRole('heading', { name: 'What to send' })).toBeInTheDocument();
		for (const label of [
			'Disk health problems',
			'RAID problems',
			'Environment offline',
			'Failed jobs',
			'Updates available',
			'Also send when resolved'
		])
			expect(screen.getByRole('checkbox', { name: label })).toBeChecked();
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
		await user.click(screen.getByRole('button', { name: 'Add channel' }));

		await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
		const post = calls.find((c) => c.method === 'POST')!;
		expect(post.path).toBe('/api/v1/notification-channels');
		expect(post.body).toEqual({
			name: 'Ops',
			address: STORED,
			enabled: true,
			eventKinds: [
				'disk_health',
				'raid',
				'environment_offline',
				'job_failed',
				'updates_available'
			],
			sendResolved: true,
			environmentIds: []
		});
		await waitFor(() => expect(toast.items.map((t) => t.title)).toContain('Added Ops'));
		expect(toast.items.find((t) => t.title === 'Added Ops')?.action?.label).toBe('Send test');
	});

	it('says what is missing and sends nothing', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true });
		await user.click(await screen.findByRole('button', { name: 'Add channel' }));
		expect(await screen.findByText('Enter a name.')).toBeInTheDocument();
		expect(screen.getByText('Choose a service.')).toBeInTheDocument();
		await user.type(screen.getByRole('textbox', { name: /^Name/ }), 'Mail');
		await choose(user, screen.getByRole('combobox', { name: /^Service/ }), 'Email (SMTP)');
		await user.click(screen.getByRole('button', { name: 'Add channel' }));
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
		expect(screen.getByRole('textbox', { name: /^SMTP server/ })).toBeInTheDocument();
		expect(screen.getByRole('textbox', { name: /^Port/ })).toHaveValue('587');
		expect(screen.getByLabelText(/^Password/)).toHaveAttribute('type', 'password');
		expect(screen.getByRole('combobox', { name: /^Encryption/ })).toHaveTextContent('Auto');
		await choose(user, screen.getByRole('combobox', { name: /^Service/ }), 'Telegram');
		expect(screen.queryByRole('textbox', { name: /^SMTP server/ })).toBeNull();
		expect(screen.getByLabelText(/^Bot token/)).toHaveAttribute('type', 'password');
		expect(screen.getByRole('textbox', { name: /^Chats/ })).toBeInTheDocument();
	});

	it('keeps the stored address masked until Show address, and saves without it when unchanged', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true, channel });
		expect(await screen.findByText('The address is stored encrypted')).toBeInTheDocument();
		expect(screen.queryByLabelText(/^Webhook URL/)).toBeNull();
		expect(screen.getByRole('checkbox', { name: 'Failed jobs' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'RAID problems' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Also send when resolved' })).not.toBeChecked();
		expect(calls).toEqual([]);

		await user.click(screen.getByRole('button', { name: 'Show address' }));
		const webhook = await screen.findByLabelText(/^Webhook URL/);
		expect(webhook).toHaveValue('https://discord.com/api/webhooks/123456789012345678/tok-123');
		expect(calls).toEqual([
			{ method: 'GET', path: '/api/v1/notification-channels/c-1/address', body: undefined }
		]);

		await user.click(screen.getByRole('checkbox', { name: 'RAID problems' }));
		await user.click(screen.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
		const patch = calls.find((c) => c.method === 'PATCH')!;
		expect(patch.path).toBe('/api/v1/notification-channels/c-1');
		expect(patch.body).toEqual({
			name: 'Ops',
			enabled: true,
			eventKinds: ['raid', 'job_failed', 'updates_available'],
			sendResolved: false,
			environmentIds: []
		});
		await waitFor(() => expect(toast.items.map((t) => t.title)).toContain('Saved Ops'));
	});

	it('sends a changed address after Show address', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true, channel });
		await user.click(await screen.findByRole('button', { name: 'Show address' }));
		const webhook = await screen.findByLabelText(/^Webhook URL/);
		await user.clear(webhook);
		await user.type(webhook, 'https://discord.com/api/webhooks/42/new-token');
		await user.click(screen.getByRole('button', { name: 'Save changes' }));
		await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
		expect(
			(calls.find((c) => c.method === 'PATCH')!.body as { address?: string }).address
		).toBe('discord://new-token@42');
	});
});
