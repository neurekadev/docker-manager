// ListCard (#22): the list header with the search and filters built in,
// "Clear filters" while any is set, and the state kept per list.
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { LIST_FILTERS_PREFIX } from './list-filters.svelte';
import ListCardHarness from './test/ListCardHarness.svelte';

function memoryStorage(initial: Record<string, string> = {}) {
	const data = new Map(Object.entries(initial));
	return {
		data,
		getItem: (k: string) => data.get(k) ?? null,
		setItem: (k: string, v: string) => void data.set(k, v),
		removeItem: (k: string) => void data.delete(k)
	};
}

const rows = [
	{ name: 'web', state: 'running', driver: 'local', labels: { tier: 'front' } },
	{ name: 'db', state: 'exited', driver: 'local' },
	{ name: 'media', state: 'running', driver: 'nfs' }
];

const listed = () =>
	within(screen.getByRole('list', { name: 'Things' }))
		.getAllByRole('listitem')
		.map((li) => li.textContent);

describe('ListCard', () => {
	it('titles the card and builds the search and every filter into its header', () => {
		render(ListCardHarness, { props: { rows, storage: memoryStorage() } });
		const card = screen.getByRole('region', { name: 'All things' });
		expect(within(card).getByRole('status')).toHaveTextContent('3 things');
		const search = within(card).getByRole('search', { name: 'Filter things' });
		expect(within(search).getByRole('searchbox', { name: 'Search things' })).toHaveAttribute(
			'placeholder',
			'Search by name'
		);
		expect(within(search).getByRole('combobox', { name: 'Status' })).toHaveValue('');
		expect(within(search).getByRole('combobox', { name: 'Driver' })).toBeInTheDocument();
		expect(within(search).getByRole('textbox', { name: 'Label' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Clear filters' })).not.toBeInTheDocument();
	});

	it('hides a filter built from the rows while it offers one choice', () => {
		render(ListCardHarness, {
			props: { rows: rows.slice(0, 2), storage: memoryStorage() }
		});
		expect(screen.queryByRole('combobox', { name: 'Driver' })).not.toBeInTheDocument();
	});

	it('filters, counts, and clears the search and every filter at once', async () => {
		const user = userEvent.setup();
		const storage = memoryStorage();
		render(ListCardHarness, { props: { rows, storage } });

		await user.selectOptions(screen.getByRole('combobox', { name: 'Status' }), 'running');
		expect(listed()).toEqual(['web', 'media']);
		await user.type(screen.getByRole('searchbox', { name: 'Search things' }), 'WE');
		expect(listed()).toEqual(['web']);
		expect(screen.getByRole('status')).toHaveTextContent('1 of 3 things');
		expect(storage.data.get(`${LIST_FILTERS_PREFIX}things`)).toBe(
			JSON.stringify({ q: 'WE', values: { status: 'running' } })
		);

		await user.click(screen.getByRole('button', { name: 'Clear filters' }));
		expect(listed()).toEqual(['web', 'db', 'media']);
		expect(screen.getByRole('searchbox', { name: 'Search things' })).toHaveValue('');
		expect(screen.getByRole('searchbox', { name: 'Search things' })).toHaveFocus();
		expect(screen.getByRole('combobox', { name: 'Status' })).toHaveValue('');
		expect(screen.queryByRole('button', { name: 'Clear filters' })).not.toBeInTheDocument();
		expect(storage.data.size).toBe(0);
	});

	it('offers the same clear action when nothing matches', async () => {
		const user = userEvent.setup();
		render(ListCardHarness, { props: { rows, storage: memoryStorage() } });
		await user.type(screen.getByRole('textbox', { name: 'Label' }), 'tier=back');
		expect(
			screen.getByRole('heading', { name: 'No things match the search and filters.' })
		).toBeInTheDocument();
		const clears = screen.getAllByRole('button', { name: 'Clear filters' });
		expect(clears).toHaveLength(2);
		await user.click(clears[1]);
		expect(listed()).toHaveLength(3);
		expect(screen.getByRole('textbox', { name: 'Label' })).toHaveValue('');
	});

	it('restores the stored search and filters of this list when it comes back', () => {
		const storage = memoryStorage({
			[`${LIST_FILTERS_PREFIX}things`]: JSON.stringify({
				q: '',
				values: { driver: 'nfs', status: 'bogus' }
			}),
			[`${LIST_FILTERS_PREFIX}other`]: JSON.stringify({ q: 'db', values: {} })
		});
		render(ListCardHarness, { props: { rows, storage } });
		expect(screen.getByRole('combobox', { name: 'Driver' })).toHaveValue('nfs');
		expect(screen.getByRole('combobox', { name: 'Status' })).toHaveValue('');
		expect(screen.getByRole('searchbox', { name: 'Search things' })).toHaveValue('');
		expect(listed()).toEqual(['media']);
		expect(screen.getByRole('button', { name: 'Clear filters' })).toBeInTheDocument();
	});
});
