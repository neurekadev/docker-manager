// An image's update badge (#20) spins while its policy's update check runs,
// also for a check it did not start (after a reload, or started on another
// page or by the schedule): the running jobs list says so.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component, ComponentProps } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import ImageUpdateBadge from './ImageUpdateBadge.svelte';

function check(policyId: string): Job {
	return {
		id: `0190-${policyId}`,
		kind: 'update.check',
		state: 'running',
		origin: 'scheduled',
		executor: 'manager',
		environmentId: 'e1',
		policyId,
		targets: [{ type: 'stack', id: 'st-1' }],
		attempt: 1,
		items: [],
		locks: [],
		locksHeld: true,
		cancelRequested: false,
		cancellable: true,
		retryable: false,
		createdAt: '2026-09-28T10:00:00Z',
		updatedAt: '2026-09-28T10:00:00Z'
	} as unknown as Job;
}

function stub(running: Job[]) {
	const fetched: string[] = [];
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		fetched.push(url.pathname + url.search);
		const items = url.pathname === '/api/v1/jobs' ? running : [];
		return new Response(JSON.stringify({ items, total: items.length }), {
			status: 200,
			headers: { 'Content-Type': 'application/json' }
		});
	});
	return fetched;
}

function mount(props: ComponentProps<typeof ImageUpdateBadge>) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: ImageUpdateBadge as unknown as Component<Record<string, unknown>>,
			props: props as Record<string, unknown>
		}
	});
	return client;
}

afterEach(() => vi.unstubAllGlobals());

describe('ImageUpdateBadge', () => {
	it('spins for a running check of its policy from the running jobs list', async () => {
		stub([check('p1')]);
		mount({ status: 'up_to_date', image: 'nginx:1.27', policyId: 'p1', canCheck: true });

		const badge = await screen.findByRole('button', {
			name: 'Checking nginx:1.27 for updates'
		});
		expect(badge).toHaveAttribute('aria-busy', 'true');
		expect(badge).toHaveAttribute('aria-disabled', 'true');
	});

	it("does not spin for another policy's check", async () => {
		const fetched = stub([check('p2')]);
		const client = mount({
			status: 'up_to_date',
			image: 'nginx:1.27',
			policyId: 'p1',
			canCheck: true
		});

		await vi.waitFor(() => {
			expect(fetched.some((u) => u.startsWith('/api/v1/jobs?'))).toBe(true);
			const queries = client.getQueryCache().getAll();
			expect(queries.length).toBeGreaterThan(0);
			expect(queries.every((q) => q.state.status === 'success')).toBe(true);
		});
		const badge = screen.getByRole('button', {
			name: 'Up to date. Select to check again.'
		});
		expect(badge).not.toHaveAttribute('aria-busy');
	});

	it('does not load the running list where no check can start', async () => {
		const fetched = stub([check('p1')]);
		mount({ status: 'up_to_date', image: 'nginx:1.27', policyId: 'p1', canCheck: false });

		expect(screen.getByRole('img', { name: 'Up to date.' })).toBeInTheDocument();
		await new Promise((r) => setTimeout(r, 20));
		expect(fetched).toEqual([]);
	});
});
