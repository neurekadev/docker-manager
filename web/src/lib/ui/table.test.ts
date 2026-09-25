import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import TableHarness from '../../test/TableHarness.svelte';

const rows = [
	{ name: 'silo-web', cpu: 2.4, image: 'ghcr.io/silo/web:latest' },
	{ name: 'silo-api', cpu: 3.1, image: 'ghcr.io/silo/api:latest' },
	{ name: 'silo-db', cpu: null, image: 'postgres:16' }
];

function names(): string[] {
	const body = screen.getByRole('table').querySelector('tbody')!;
	return [...body.querySelectorAll('tr')].map(
		(tr) => tr.querySelectorAll('td')[0]?.textContent?.trim() ?? ''
	);
}

describe('Table', () => {
	it('sorts by header buttons and reports aria-sort', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(TableHarness, { props: { rows } });
		expect(screen.getByRole('table', { name: 'Services' })).toBeInTheDocument();
		const nameHeader = screen.getByRole('columnheader', { name: /Name/ });
		expect(nameHeader).toHaveAttribute('aria-sort', 'none');
		expect(screen.getByRole('columnheader', { name: 'Image' })).not.toHaveAttribute(
			'aria-sort'
		);

		await user.click(within(nameHeader).getByRole('button'));
		expect(nameHeader).toHaveAttribute('aria-sort', 'ascending');
		expect(names()).toEqual(['silo-api', 'silo-db', 'silo-web']);
		await user.click(within(nameHeader).getByRole('button'));
		expect(nameHeader).toHaveAttribute('aria-sort', 'descending');
		expect(names()).toEqual(['silo-web', 'silo-db', 'silo-api']);

		const cpu = screen.getByRole('columnheader', { name: /CPU/ });
		await user.click(within(cpu).getByRole('button'));
		expect(names()).toEqual(['silo-web', 'silo-api', 'silo-db']); // null last
		expect(nameHeader).toHaveAttribute('aria-sort', 'none');
	});

	it('selects rows with labelled checkboxes and a mixed header state', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		render(TableHarness, { props: { rows, selectable: true } });
		await user.click(screen.getByRole('checkbox', { name: 'Select silo-api' }));
		expect(screen.getByTestId('selected')).toHaveTextContent('silo-api');
		const all = screen.getByRole('checkbox', { name: 'Select all rows' }) as HTMLInputElement;
		expect(all.indeterminate).toBe(true);
		await user.click(all);
		expect(screen.getByTestId('selected')).toHaveTextContent('silo-api,silo-web,silo-db');
		expect(all.checked).toBe(true);
		await user.click(all);
		expect(screen.getByTestId('selected')).toHaveTextContent(/^$/);
	});

	it('renders stacked row cards on narrow layouts', () => {
		render(TableHarness, { props: { rows, layout: 'stacked' } });
		expect(screen.queryByRole('table')).toBeNull();
		const list = screen.getByRole('list', { name: 'Services' });
		const cards = within(list).getAllByRole('listitem');
		expect(cards).toHaveLength(3);
		// Title first, then label/value pairs.
		expect(within(cards[0]).getByText('silo-web')).toBeInTheDocument();
		expect(within(cards[0]).getByText('Image')).toBeInTheDocument();
		expect(within(cards[0]).getByText('ghcr.io/silo/web:latest')).toBeInTheDocument();
	});

	it('windows lists past 500 rows but reports the full size', () => {
		const many = Array.from({ length: 2000 }, (_, i) => ({
			name: `svc-${String(i).padStart(4, '0')}`,
			cpu: i,
			image: 'x'
		}));
		render(TableHarness, { props: { rows: many, maxHeight: '480px' } });
		const table = screen.getByRole('table');
		expect(table).toHaveAttribute('aria-rowcount', '2001');
		const rendered = table.querySelectorAll('tbody tr:not(.spacer)');
		expect(rendered.length).toBeGreaterThan(10);
		expect(rendered.length).toBeLessThan(60);
		expect(rendered[0]).toHaveAttribute('aria-rowindex', '2');
	});
});
