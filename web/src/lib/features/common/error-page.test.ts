import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ErrorPageBody from './ErrorPageBody.svelte';

describe('ErrorPageBody (#22 error pages)', () => {
	it('says an address is unknown once, with Back and the dashboard but no Reload', () => {
		render(ErrorPageBody, { props: { status: 404 } });
		expect(screen.getAllByRole('heading')).toHaveLength(1);
		expect(
			screen.getByRole('heading', { level: 1, name: 'Page Not Found' })
		).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Reload' })).not.toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Back' })).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Go to the Dashboard' })).toHaveAttribute(
			'href',
			'/'
		);
	});

	it('offers Reload when a page failed', () => {
		render(ErrorPageBody, { props: { status: 500 } });
		expect(
			screen.getByRole('heading', { level: 1, name: 'This page failed to load' })
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Reload' })).toBeInTheDocument();
	});
});
