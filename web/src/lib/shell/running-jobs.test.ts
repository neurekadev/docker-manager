// The top bar's running jobs (docs/internal/web.md, "Job progress after
// reload"): a count from the running list linking to the jobs in progress,
// hidden while nothing runs.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../test/QueryHarness.svelte';
import RunningJobs from './RunningJobs.svelte';

function mount(items: unknown[], props: ComponentProps<typeof RunningJobs> = {}) {
	const fetch = vi.fn(
		async () =>
			new Response(JSON.stringify({ items, total: items.length }), {
				headers: { 'Content-Type': 'application/json' }
			})
	);
	vi.stubGlobal('fetch', fetch);
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness<ComponentProps<typeof RunningJobs>>, {
		props: { client, component: RunningJobs, props }
	});
	return fetch;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('RunningJobs', () => {
	it('links the count of running jobs to the jobs in progress', async () => {
		mount([{ id: 'j1' }, { id: 'j2' }]);
		const link = await screen.findByRole('link', {
			name: '2 jobs running. Open the jobs in progress'
		});
		expect(link).toHaveAttribute('href', '/jobs?state=active');
		expect(link).toHaveTextContent('2 running');
	});

	it('says job for one and shows only the count on phones', async () => {
		mount([{ id: 'j1' }], { compact: true });
		const link = await screen.findByRole('link', {
			name: '1 job running. Open the jobs in progress'
		});
		expect(link).toHaveTextContent(/^1$/);
	});

	it('is hidden while nothing runs', async () => {
		const fetch = mount([]);
		await vi.waitFor(() => expect(fetch).toHaveBeenCalled());
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('loads nothing when turned off (restricted users)', () => {
		const fetch = mount([{ id: 'j1' }], { enabled: false });
		expect(fetch).not.toHaveBeenCalled();
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});
});
