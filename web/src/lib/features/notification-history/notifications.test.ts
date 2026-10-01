// The Notifications tab (NotificationsView): finished runs grouped by day,
// each with its outcome, values, environment and time; filters applied by
// the server; "Load more" follows the cursor.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import { LIST_FILTERS_PREFIX } from '$lib/features/resources/list-filters.svelte';
import NotificationsView from './NotificationsView.svelte';
import type { Notification } from './model';
import { failedUpdate, nightlyBackup, sampleNotification, sep, weeklyPrune } from './test/samples';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });
const json = (body: unknown) =>
	new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });

// Local noon, so "Today" and "Yesterday" do not depend on the time zone.
const now = new Date(2026, 8, 30, 12, 0);

let requests: URLSearchParams[] = [];

function stub(page: (search: URLSearchParams) => { items: Notification[]; nextCursor?: string }) {
	requests = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (req: Request) => {
			const url = new URL(req.url);
			if (url.pathname === '/api/v1/environments')
				return json({
					items: [
						{ id: 'e1', name: 'homelab', online: true, actions: [] },
						{ id: 'e2', name: 'edge', online: true, actions: [] }
					]
				});
			if (url.pathname === '/api/v1/notifications') {
				requests.push(url.searchParams);
				return json(page(url.searchParams));
			}
			return new Response('{}', { status: 404 });
		})
	);
}

function mount(props: ComponentProps<typeof NotificationsView>) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	type P = ComponentProps<typeof NotificationsView>;
	return render(QueryHarness<P>, { props: { client, component: NotificationsView, props } });
}

beforeEach(() => sessionStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe('Notifications tab', () => {
	it('groups the runs by day with their outcome, values, environment and time', async () => {
		stub(() => ({ items: [nightlyBackup, weeklyPrune, failedUpdate] }));
		mount({ environmentId: null, now });
		const today = await screen.findByRole('heading', { name: 'Today' });
		expect(today.tagName).toBe('H3');
		expect(screen.getByRole('heading', { name: 'Yesterday' })).toBeInTheDocument();
		expect(screen.getByRole('heading', { name: 'Sun, Sep 27' })).toBeInTheDocument();
		expect(requests[0].get('limit')).toBe('50');
		expect(requests[0].get('kind')).toBeNull();

		// A prune lists what it removed per kind of object.
		const yesterday = screen.getByRole('list', { name: 'Notifications of Yesterday' });
		const prune = within(yesterday).getByRole('link', {
			name: 'Prune Weekly on edge reclaimed 4.2 GiB'
		});
		expect(prune).toHaveAttribute('href', '/jobs/job-n2');
		const item = prune.closest('li')!;
		for (const [name, value] of [
			['Reclaimed', '4.2 GiB'],
			['Containers', '3 containers · 12 MB'],
			['Images', '7 images · 4.1 GiB'],
			['Volumes', '1 volume'],
			['Networks', '2 networks'],
			['Build cache', '14 entries · 96 MB']
		]) {
			expect(within(item).getByText(name).tagName).toBe('DT');
			expect(within(item).getByText(value).tagName).toBe('DD');
		}
		expect(within(item).getByText('Done')).toBeInTheDocument();
		expect(within(item).getByText('edge')).toBeInTheDocument();
		// The environment shows once, not again as a value.
		expect(within(item).queryByText('Environment')).toBeNull();
		expect(within(item).getByText(/ago/).closest('time')).toHaveAttribute(
			'datetime',
			weeklyPrune.createdAt
		);

		// A failed update run: its outcome, detail and the services on lines.
		const update = screen
			.getByRole('link', { name: 'Update of Silo on homelab failed' })
			.closest('li')!;
		expect(within(update).getByText('Failed', { selector: '.badge' })).toBeInTheDocument();
		expect(update).toHaveTextContent('1 of 2 services could not be updated.');
		expect(within(update).getByText('worker')).toBeInTheDocument();
		expect(within(update).getByText('homelab')).toBeInTheDocument();
		expect(screen.getAllByText('3 notifications').length).toBeGreaterThan(0);
		expect(screen.queryByRole('button', { name: 'Load more notifications' })).toBeNull();
	});

	it('asks the server for the stored filters and hides the environment for one', async () => {
		sessionStorage.setItem(
			LIST_FILTERS_PREFIX + 'notifications',
			JSON.stringify({ q: '', values: { kind: 'prune', outcome: 'failure' } })
		);
		stub(() => ({ items: [] }));
		mount({ environmentId: 'e2', now });
		await screen.findByRole('heading', { name: /match the search and filters/ });
		expect(requests[0].get('kind')).toBe('prune');
		expect(requests[0].get('outcome')).toBe('failure');
		expect(requests[0].get('environmentId')).toBe('e2');
		expect(screen.queryByRole('combobox', { name: 'Environment' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Environment' })).toBeNull();
	});

	it('says that finished runs show up here while there are none', async () => {
		stub(() => ({ items: [] }));
		mount({ environmentId: null, now });
		expect(
			await screen.findByRole('heading', { name: 'No notifications yet.' })
		).toBeInTheDocument();
		expect(
			screen.getByText('Finished backups, restores, prunes and updates show up here.')
		).toBeInTheDocument();
	});

	it('loads older notifications with Load more', async () => {
		const user = setup();
		const older = sampleNotification({
			id: 'n9',
			title: 'Restore of data on homelab succeeded',
			createdAt: sep(20, 8)
		});
		stub((s) =>
			s.get('cursor') === 'c2'
				? { items: [older] }
				: { items: [nightlyBackup], nextCursor: 'c2' }
		);
		mount({ environmentId: null, now });
		await screen.findByRole('link', { name: 'Backup Nightly on homelab succeeded' });
		await user.click(screen.getByRole('button', { name: 'Load more notifications' }));
		expect(
			await screen.findByRole('link', { name: 'Restore of data on homelab succeeded' })
		).toBeInTheDocument();
		expect(screen.getByRole('heading', { name: 'Sun, Sep 20' })).toBeInTheDocument();
		await waitFor(() => expect(requests.at(-1)?.get('cursor')).toBe('c2'));
		expect(screen.queryByRole('button', { name: 'Load more notifications' })).toBeNull();
	});
});
