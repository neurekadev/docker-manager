// "What to Send" (#142): a bell per event and channel, the In App channel
// first; a kind's bell switches all its outcomes (mixed while only some are
// on); changes wait for Save Changes, which patches only the changed
// channels' events; a channel with nothing left to send can't be saved.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import { toast } from '$lib/ui';
import QueryHarness from '../../../test/QueryHarness.svelte';
import SubscriptionMatrix from './SubscriptionMatrix.svelte';
import { allEvents, type NotificationChannel } from './model';

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

const base: NotificationChannel = {
	id: 'c-1',
	name: 'Ops',
	service: 'discord',
	builtIn: false,
	enabled: true,
	events: [
		{ kind: 'backup', outcomes: ['failure', 'warning', 'success'] },
		{ kind: 'job_failed', outcomes: ['failure'] }
	],
	allEnvironments: true,
	environmentIds: [],
	address: { fingerprint: 'fp_1', version: 1, updatedAt: '2026-09-30T09:00:00Z' },
	revision: 3,
	createdAt: '2026-09-30T09:00:00Z',
	updatedAt: '2026-09-30T09:00:00Z'
};

const channels: NotificationChannel[] = [
	base,
	{ ...base, id: 'c-2', name: 'Mail', service: 'smtp', enabled: false, events: allEvents() },
	{
		...base,
		id: '00000000-0000-0000-0000-000000000000',
		name: 'In App',
		service: 'app',
		builtIn: true,
		events: allEvents()
	}
];

function stubApi() {
	const calls: { method: string; path: string; body: unknown; ifMatch: string | null }[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request) => {
			const url = new URL(input.url);
			const body = input.method === 'GET' ? undefined : await input.json();
			calls.push({
				method: input.method,
				path: url.pathname,
				body,
				ifMatch: input.headers.get('If-Match')
			});
			return json({ ...base, ...(body as object), revision: 4 });
		})
	);
	return calls;
}

function show(props: ComponentProps<typeof SubscriptionMatrix>) {
	render(QueryHarness<ComponentProps<typeof SubscriptionMatrix>>, {
		props: {
			client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
			component: SubscriptionMatrix,
			props
		}
	});
}

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });
const bell = (name: string) => screen.getByRole('button', { name });

afterEach(() => {
	vi.unstubAllGlobals();
	toast.clear();
});

describe('SubscriptionMatrix (#142)', () => {
	it('shows a bell per event and channel, the In App channel first', () => {
		stubApi();
		show({ channels });
		const headers = [...screen.getByRole('table').querySelectorAll('thead th')];
		expect(headers.map((h) => h.querySelector('.name')?.textContent ?? h.textContent)).toEqual([
			'Event',
			'In App',
			'Mail',
			'Ops'
		]);
		expect(headers[1]).toHaveTextContent('Notices');
		expect(headers[2]).toHaveTextContent('Email · Off');
		expect(headers[3]).toHaveTextContent('Discord');
		expect(bell('Backups for Ops')).toHaveAttribute('aria-pressed', 'true');
		expect(bell('Other Jobs for Ops')).toHaveAttribute('aria-pressed', 'mixed');
		expect(bell('Other Jobs: Failure for Ops')).toHaveAttribute('aria-pressed', 'true');
		expect(bell('Other Jobs: Resolved for Ops')).toHaveAttribute('aria-pressed', 'false');
		expect(bell('RAID for Ops')).toHaveAttribute('aria-pressed', 'false');
		expect(bell('RAID: Warning for In App')).toHaveAttribute('aria-pressed', 'true');
		// A kind whose label does not say all it covers explains it in an (i).
		expect(screen.getByRole('img', { name: /an API token started/ })).toBeInTheDocument();
		// Nothing changed: no save bar.
		expect(screen.queryByRole('button', { name: 'Save Changes' })).toBeNull();
	});

	it('switches a kind with its bell and saves only the changed channels', async () => {
		const user = setup();
		const calls = stubApi();
		show({ channels });
		await user.click(bell('Backups: Success for Ops'));
		expect(bell('Backups: Success for Ops')).toHaveAttribute('aria-pressed', 'false');
		expect(bell('Backups for Ops')).toHaveAttribute('aria-pressed', 'mixed');
		// Mixed: the kind's bell turns every outcome on, then all off.
		await user.click(bell('Backups for Ops'));
		expect(bell('Backups: Success for Ops')).toHaveAttribute('aria-pressed', 'true');
		await user.click(bell('Backups for Ops'));
		expect(bell('Backups: Failure for Ops')).toHaveAttribute('aria-pressed', 'false');
		await user.click(bell('RAID: Critical for Ops'));
		// Back and forth on the In App channel: not a change.
		await user.click(bell('Prune for In App'));
		await user.click(bell('Prune for In App'));
		expect(screen.getByText(/channel changed/)).toHaveTextContent('1 channel changed');

		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		await waitFor(() => expect(calls).toHaveLength(1));
		expect(calls[0]).toEqual({
			method: 'PATCH',
			path: '/api/v1/notification-channels/c-1',
			ifMatch: '"3"',
			body: {
				events: [
					{ kind: 'raid', outcomes: ['critical'] },
					{ kind: 'job_failed', outcomes: ['failure'] }
				]
			}
		});
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toContain('Saved What to Send')
		);
	});

	it('refuses to save a channel with nothing to send, and discards', async () => {
		const user = setup();
		const calls = stubApi();
		show({ channels });
		await user.click(bell('Backups for Ops'));
		await user.click(bell('Other Jobs for Ops'));
		await user.click(bell('Other Jobs for Ops'));
		expect(
			await screen.findByText('Ops needs at least one event to send.')
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled();
		await user.click(screen.getByRole('button', { name: 'Discard' }));
		expect(bell('Backups for Ops')).toHaveAttribute('aria-pressed', 'true');
		expect(screen.queryByRole('button', { name: 'Save Changes' })).toBeNull();
		expect(calls).toEqual([]);
	});
});
