// Route tabs (TabNav): the current tab is marked, and a row that scrolls
// sideways fades out at the edge where more tabs are cut off.
import { describe, expect, it } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import TabNav from './TabNav.svelte';

const items = [
	{ href: '/profile', label: 'Account' },
	{ href: '/profile/tokens', label: 'API tokens' }
];

function size(el: HTMLElement, scrollWidth: number, clientWidth: number, scrollLeft = 0) {
	Object.defineProperty(el, 'scrollWidth', { configurable: true, value: scrollWidth });
	Object.defineProperty(el, 'clientWidth', { configurable: true, value: clientWidth });
	Object.defineProperty(el, 'scrollLeft', {
		configurable: true,
		writable: true,
		value: scrollLeft
	});
}

describe('TabNav', () => {
	it('marks the current tab', () => {
		render(TabNav, {
			props: { items, current: '/profile/tokens/new', label: 'Profile sections' }
		});
		expect(screen.getByRole('link', { name: 'API tokens' })).toHaveAttribute(
			'aria-current',
			'page'
		);
		expect(screen.getByRole('link', { name: 'Account' })).not.toHaveAttribute('aria-current');
	});

	it('fades the edges where tabs are cut off', async () => {
		render(TabNav, { props: { items, current: '/profile', label: 'Profile sections' } });
		const nav = screen.getByRole('navigation', { name: 'Profile sections' });
		size(nav, 600, 300);
		await fireEvent.scroll(nav);
		expect(nav).toHaveClass('fade-end');
		expect(nav).not.toHaveClass('fade-start');
		size(nav, 600, 300, 300);
		await fireEvent.scroll(nav);
		expect(nav).toHaveClass('fade-start');
		expect(nav).not.toHaveClass('fade-end');
		size(nav, 300, 300);
		await fireEvent.scroll(nav);
		expect(nav).not.toHaveClass('fade-start');
		expect(nav).not.toHaveClass('fade-end');
	});
});
