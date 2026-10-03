import { describe, expect, it } from 'vitest';
import { pageState, type PageMeta } from './page.svelte';

const meta = (title: string): PageMeta => ({ title, crumbs: [{ label: title }] });

describe('pageState', () => {
	it('shows the innermost page and the layout again once the page leaves', () => {
		const layout = pageState.add({ get: () => meta('Silo') });
		const logs = pageState.add({ get: () => meta('Silo logs') });
		expect(pageState.current.title).toBe('Silo logs');

		// A tab without its own title: the layout's crumbs come back.
		logs();
		expect(pageState.current.crumbs).toEqual([{ label: 'Silo' }]);

		layout();
		expect(pageState.current.title).toBe('Docker Manager');
	});

	it('keeps the arriving page when the previous one leaves after it', () => {
		const layout = pageState.add({ get: () => meta('Silo') });
		const files = pageState.add({ get: () => meta('Silo files') });
		const logs = pageState.add({ get: () => meta('Silo logs') });
		files();
		expect(pageState.current.title).toBe('Silo logs');
		logs();
		layout();
	});
});
