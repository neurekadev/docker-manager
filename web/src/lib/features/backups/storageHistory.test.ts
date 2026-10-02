// Storage over time on the Backups overview (#10): two lines (stored, and
// before compression), the last 30 days by default, another range asks
// the server for that range, and an empty history invites the next backup.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import type { TimeSeriesOptions } from '$lib/lazy';
import QueryHarness from '../../../test/QueryHarness.svelte';
import { choose } from '../../../test/select';
import StorageHistoryCard from './StorageHistoryCard.svelte';
import type { StorageHistory } from './storageHistory';

const lazy = vi.hoisted(() => ({ mounted: [] as TimeSeriesOptions[] }));

vi.mock('$lib/lazy', async (orig) => ({
	...(await orig<typeof import('$lib/lazy')>()),
	mountTimeSeries: vi.fn(async (_el: HTMLElement, o: TimeSeriesOptions) => {
		lazy.mounted.push(o);
		return { update() {}, resize() {}, destroy() {} };
	})
}));

afterEach(() => {
	vi.unstubAllGlobals();
	lazy.mounted.length = 0;
});

const GB = 1024 ** 3;
const DAY = 86_400_000;

const history: StorageHistory = {
	from: '2026-08-29T12:00:00Z',
	to: '2026-09-28T12:00:00Z',
	stepSeconds: 86400,
	timestamps: [
		'2026-08-29T12:00:00Z',
		'2026-08-30T00:00:00Z',
		'2026-08-31T00:00:00Z',
		'2026-09-28T12:00:00Z'
	],
	storedBytes: [null, 2 * GB, 2 * GB, 3 * GB],
	uncompressedBytes: [null, 5 * GB, 5 * GB, 7 * GB]
};

/** Stubs the history endpoint; returns the query strings it received. */
function stubHistory(body: StorageHistory = history) {
	const seen: URLSearchParams[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(String(input), init);
			const url = new URL(req.url);
			if (url.pathname === '/api/v1/backup-storage/history') seen.push(url.searchParams);
			return new Response(JSON.stringify(body), {
				status: 200,
				headers: { 'Content-Type': 'application/json' }
			});
		})
	);
	return seen;
}

function renderCard(props: ComponentProps<typeof StorageHistoryCard> = {}) {
	return render(QueryHarness<ComponentProps<typeof StorageHistoryCard>>, {
		props: {
			client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
			component: StorageHistoryCard,
			props
		}
	});
}

/** Days between a request's `from` and now. */
const daysBack = (q: URLSearchParams) => (Date.now() - Date.parse(q.get('from') ?? '')) / DAY;

describe('StorageHistoryCard (#10)', () => {
	it('shows the last 30 days as two lines, stored and before compression', async () => {
		const seen = stubHistory();
		renderCard({ environmentId: 'e1' });

		const fig = await screen.findByRole('figure', { name: 'Storage' });
		expect(seen).toHaveLength(1);
		expect(daysBack(seen[0])).toBeCloseTo(30, 1);
		expect(seen[0].get('environmentId')).toBe('e1');
		expect(screen.getByRole('combobox', { name: 'Range' })).toHaveTextContent('Last 30 Days');

		// The legend names both lines with their latest values.
		expect(fig).toHaveTextContent('Stored 3 GB');
		expect(fig).toHaveTextContent('Before Compression 7 GB');
		expect(
			screen.getByText('3 GB stored now, up 1 GB in the last 30 days.')
		).toBeInTheDocument();

		await waitFor(() => expect(lazy.mounted).toHaveLength(1));
		const o = lazy.mounted[0];
		expect(o.lines.map((l) => l.name)).toEqual(['Stored', 'Before Compression']);
		// The point before the first measurement is left out; the axis
		// still spans the whole range.
		expect(o.lines[0].values).toEqual([2 * GB, 2 * GB, 3 * GB]);
		expect(o.lines[1].values).toEqual([5 * GB, 5 * GB, 7 * GB]);
		expect(o.lines[1].dashed).toBe(true);
		expect(o.from).toBe(Date.parse(history.from));
		expect(o.to).toBe(Date.parse(history.to));

		// The figures as a table: changes and the latest, newest first.
		const table = screen.getByRole('table', { name: 'Storage Over Time', hidden: true });
		expect(table.querySelectorAll('tbody tr')).toHaveLength(2);
	});

	it('asks for another range when one is chosen', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const seen = stubHistory();
		renderCard();
		await screen.findByRole('figure', { name: 'Storage' });
		expect(seen[0].get('environmentId')).toBeNull();

		await choose(user, screen.getByRole('combobox', { name: 'Range' }), 'Last 7 Days');
		await waitFor(() => expect(seen).toHaveLength(2));
		expect(daysBack(seen[1])).toBeCloseTo(7, 1);

		await choose(user, screen.getByRole('combobox', { name: 'Range' }), 'Last Year');
		await waitFor(() => expect(seen).toHaveLength(3));
		expect(daysBack(seen[2])).toBeCloseTo(365, 1);
	});

	it('invites the next backup while nothing was measured', async () => {
		stubHistory({
			...history,
			storedBytes: history.storedBytes.map(() => null),
			uncompressedBytes: history.uncompressedBytes.map(() => null)
		});
		renderCard();
		expect(await screen.findByText('No history yet.')).toBeInTheDocument();
		expect(screen.queryByRole('figure')).toBeNull();
	});
});
