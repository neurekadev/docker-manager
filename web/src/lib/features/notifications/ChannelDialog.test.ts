// Notification channel dialog (#142): friendly fields per service (secrets
// in password fields) become one Shoutrrr URL; switching the service
// swaps the fields; editing keeps the stored address masked until "Show
// Address" reads it back, and saves without it when it is unchanged.
// "What to Send" is a grid: a checkbox per kind of event (mixed while some
// of its outcomes are ticked) and one per outcome.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
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
		expect(screen.getByRole('heading', { name: 'What to Send' })).toBeInTheDocument();
		for (const label of [
			'Disk Health',
			'RAID',
			'Temperature',
			'Disk Space',
			'Memory',
			'Environment Offline',
			'Backups',
			'Restores',
			'Prune',
			'Image Updates',
			'Other Jobs',
			'Disk Health: Warning',
			'Environment Offline: Back Online',
			'Image Updates: Applied',
			'Restores: Success',
			'Other Jobs: Resolved'
		])
			expect(screen.getByRole('checkbox', { name: label })).toBeChecked();
		// A row whose label does not say all it covers explains it in an (i).
		const others = screen
			.getByRole('checkbox', { name: 'Other Jobs' })
			.closest('.event-kind') as HTMLElement;
		expect(
			within(others).getByRole('img', { name: /an API token started/ })
		).toBeInTheDocument();
		const prune = screen
			.getByRole('checkbox', { name: 'Prune' })
			.closest('.event-kind') as HTMLElement;
		expect(within(prune).queryByRole('img')).toBeNull();
		// Each kind leads with its icon (decorative); its outcomes have none.
		expect(
			screen.getByRole('checkbox', { name: 'RAID' }).closest('label')?.querySelector('svg')
		).toHaveAttribute('aria-hidden', 'true');
		expect(
			screen
				.getByRole('checkbox', { name: 'RAID: Warning' })
				.closest('label')
				?.querySelector('svg')
		).toBeNull();
		expect(screen.getByRole('group', { name: 'Hosts' })).toBeInTheDocument();
		expect(screen.getByRole('group', { name: 'Jobs' })).toBeInTheDocument();
		expect(screen.queryByRole('checkbox', { name: /^Also send when resolved/ })).toBeNull();
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
			events: allEvents(),
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
		// The channel's events: every update outcome, only failed jobs' failures.
		expect(screen.getByRole('checkbox', { name: 'Image Updates' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Other Jobs' })).toBePartiallyChecked();
		expect(screen.getByRole('checkbox', { name: 'Other Jobs: Failure' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'Other Jobs: Resolved' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'RAID' })).not.toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'RAID' })).not.toBePartiallyChecked();
		expect(calls).toEqual([]);

		await user.click(screen.getByRole('button', { name: 'Show Address' }));
		const webhook = await screen.findByLabelText(/^Webhook URL/);
		expect(webhook).toHaveValue('https://discord.com/api/webhooks/123456789012345678/tok-123');
		// Asked for: shown in plain text, no second click on the eye.
		expect(webhook).toHaveAttribute('type', 'text');
		expect(calls).toEqual([
			{ method: 'GET', path: '/api/v1/notification-channels/c-1/address', body: undefined }
		]);

		await user.click(screen.getByRole('checkbox', { name: 'RAID' }));
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true));
		const patch = calls.find((c) => c.method === 'PATCH')!;
		expect(patch.path).toBe('/api/v1/notification-channels/c-1');
		expect(patch.body).toEqual({
			name: 'Ops',
			enabled: true,
			events: [
				{ kind: 'raid', outcomes: ['warning', 'critical', 'resolved'] },
				{ kind: 'updates', outcomes: ['available', 'failure', 'success'] },
				{ kind: 'job_failed', outcomes: ['failure'] }
			],
			allEnvironments: true,
			environmentIds: []
		});
		await waitFor(() => expect(toast.items.map((t) => t.title)).toContain('Saved Ops'));
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

	it('ticks every outcome of a kind with its checkbox, mixed while only some are', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true });
		const backups = await screen.findByRole('checkbox', { name: 'Backups' });
		const success = screen.getByRole('checkbox', { name: 'Backups: Success' });

		// One outcome off: the kind's checkbox is mixed.
		await user.click(success);
		expect(success).not.toBeChecked();
		expect(backups).toBePartiallyChecked();

		// The kind's checkbox ticks every outcome again, then none.
		await user.click(backups);
		expect(backups).toBeChecked();
		expect(success).toBeChecked();
		await user.click(backups);
		expect(backups).not.toBeChecked();
		for (const o of ['Failure', 'Warning', 'Success'])
			expect(screen.getByRole('checkbox', { name: `Backups: ${o}` })).not.toBeChecked();

		// Ticking one outcome of an unticked kind makes it mixed.
		await user.click(screen.getByRole('checkbox', { name: 'Prune: Failure' }));
		expect(screen.getByRole('checkbox', { name: 'Prune' })).toBePartiallyChecked();

		await user.type(screen.getByRole('textbox', { name: /^Name/ }), 'Ops');
		await choose(user, screen.getByRole('combobox', { name: /^Service/ }), 'Discord');
		await user.type(
			screen.getByLabelText(/^Webhook URL/),
			'https://discord.com/api/webhooks/123456789012345678/tok-123'
		);
		await user.click(screen.getByRole('checkbox', { name: 'Prune' }));
		await user.click(screen.getByRole('checkbox', { name: 'Prune: Success' }));
		await user.click(screen.getByRole('button', { name: 'Add Channel' }));
		await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true));
		const events = (calls.find((c) => c.method === 'POST')!.body as { events: unknown[] })
			.events;
		// Display order; backups left out; prune with its one outcome.
		expect(events).toEqual(
			allEvents()
				.filter((e) => e.kind !== 'backup')
				.map((e) => (e.kind === 'prune' ? { kind: 'prune', outcomes: ['failure'] } : e))
		);
	});

	it('refuses to save with nothing to send', async () => {
		const user = setup();
		const calls = stubApi();
		show({ open: true, channel });
		await user.click(await screen.findByRole('checkbox', { name: 'Image Updates' }));
		await user.click(screen.getByRole('checkbox', { name: 'Other Jobs' }));
		// Mixed: the click ticks every outcome; a second one clears them.
		await user.click(screen.getByRole('checkbox', { name: 'Other Jobs' }));
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		expect(await screen.findByText('Choose at least one event to send.')).toBeInTheDocument();
		expect(calls.filter((c) => c.method === 'PATCH')).toEqual([]);
	});
});
