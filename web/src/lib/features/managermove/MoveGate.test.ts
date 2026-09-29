// The root layout's gate: a new manager waiting for a move sends every
// page to the status page and starts no background work (the live
// stream would only collect 503s); any other manager renders the app and
// lets it start, once.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import { createRawSnippet } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import MoveGate from './MoveGate.svelte';

const nav = vi.hoisted(() => ({
	page: { url: new URL('http://localhost/') },
	goto: vi.fn()
}));
vi.mock('$app/state', () => ({ page: nav.page }));
vi.mock('$app/navigation', () => ({ goto: nav.goto }));

function stubStatus(phase: string) {
	vi.stubGlobal(
		'fetch',
		async () =>
			new Response(
				JSON.stringify({
					phase,
					stacksMoved: 0,
					stacksTotal: 0,
					jobsRunning: 0,
					bytesReceived: 0,
					bytesTotal: 0,
					oldManagerConfirmed: false
				}),
				{ status: 200, headers: { 'Content-Type': 'application/json' } }
			)
	);
}

function gate(onready: () => void) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const children = createRawSnippet(() => ({ render: () => '<p>The app</p>' }));
	render(QueryHarness, {
		props: { client, component: MoveGate as never, props: { children, onready } }
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
	nav.goto.mockReset();
});

describe('MoveGate', () => {
	it('renders the app and lets it start once when nothing waits', async () => {
		stubStatus('none');
		const onready = vi.fn();
		gate(onready);

		expect(await screen.findByText('The app')).toBeInTheDocument();
		expect(onready).toHaveBeenCalledTimes(1);
		expect(nav.goto).not.toHaveBeenCalled();
	});

	it('sends a waiting manager to the status page and starts nothing', async () => {
		stubStatus('waiting');
		const onready = vi.fn();
		gate(onready);

		await waitFor(() =>
			expect(nav.goto).toHaveBeenCalledWith('/moving', { replaceState: true })
		);
		expect(screen.queryByText('The app')).toBeNull();
		expect(onready).not.toHaveBeenCalled();
	});
});
