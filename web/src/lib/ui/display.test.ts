import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import Chip from './Chip.svelte';
import KpiCard from './KpiCard.svelte';

describe('Chip', () => {
	it('is a toggle button when it has a selected state', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const onclick = vi.fn();
		const { rerender } = render(Chip, {
			props: { label: 'cloud', selected: false, count: 3, onclick }
		});
		const chip = screen.getByRole('button', { name: /^cloud\s*3$/ });
		expect(chip).toHaveAttribute('aria-pressed', 'false');
		await user.click(chip);
		expect(onclick).toHaveBeenCalledTimes(1);
		await rerender({ selected: true });
		expect(chip).toHaveAttribute('aria-pressed', 'true');
	});

	it('is a link with href and a plain tag otherwise', () => {
		const { unmount } = render(Chip, {
			props: { label: 'media', href: '/templates?tag=media' }
		});
		expect(screen.getByRole('link', { name: 'media' })).toHaveAttribute(
			'href',
			'/templates?tag=media'
		);
		unmount();
		render(Chip, { props: { label: 'files', size: 'sm' } });
		expect(screen.getByText('files')).toBeInTheDocument();
		expect(screen.queryByRole('button')).toBeNull();
		expect(screen.queryByRole('link')).toBeNull();
	});

	it('offers an × button on a removable tag', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const onremove = vi.fn();
		render(Chip, { props: { label: 'cloud', onremove } });
		await user.click(screen.getByRole('button', { name: 'Remove cloud' }));
		expect(onremove).toHaveBeenCalledTimes(1);
		expect(screen.getByText('cloud')).toBeInTheDocument();
	});
});

describe('KpiCard', () => {
	it('keeps label, value and secondary line on one line with the full text as tooltip', () => {
		render(KpiCard, {
			props: {
				label: 'Memory Used by All Containers',
				value: '1.8 GB',
				unit: '/ 8 GB',
				secondary: 'Of the host’s memory',
				tone: 'warn'
			}
		});
		const card = screen.getByRole('group', { name: 'Memory Used by All Containers' });
		expect(card).toBeInTheDocument();
		expect(screen.getByText('Memory Used by All Containers')).toHaveAttribute(
			'title',
			'Memory Used by All Containers'
		);
		expect(screen.getByText('1.8 GB')).toHaveAttribute('title', '1.8 GB / 8 GB');
		expect(screen.getByText('Of the host’s memory')).toHaveAttribute(
			'title',
			'Of the host’s memory'
		);
	});

	it('links its label to the list behind the figure', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const onclick = vi.fn((e: MouseEvent) => e.preventDefault());
		render(KpiCard, {
			props: { label: 'Containers Running', value: '8 / 9', href: '/containers', onclick }
		});
		const link = screen.getByRole('link', { name: 'Containers Running' });
		expect(link).toHaveAttribute('href', '/containers');
		await user.click(link);
		expect(onclick).toHaveBeenCalledTimes(1);
	});
});
