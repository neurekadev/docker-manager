// Component test of the one-off prune dialog (#14): safe starting rules,
// preview before anything is removed, the volume opt-in gate, the
// request bodies and a running prune found again after a reload.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import type { EnvironmentScope } from '$lib/features/resources/scope.svelte';
import PruneButton from './PruneButton.svelte';
import type { PruneTarget } from './model';

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': status >= 400 ? 'application/problem+json' : 'application/json' }
	});
}

const silo = { id: 'env-1', name: 'Silo', online: true };

function scope(allowed: boolean): EnvironmentScope {
	return {
		single: true,
		targets: [silo],
		creatable: () => (allowed ? [silo] : []),
		can: () => allowed
	} as unknown as EnvironmentScope;
}

const preview = {
	policyId: '',
	environmentId: 'env-1',
	at: '2026-09-26T10:00:00Z',
	remove: 2,
	bytes: 2048,
	notes: [],
	categories: [
		{
			category: 'dangling_images',
			remove: 2,
			protected: 0,
			excluded: 0,
			retained: 0,
			bytes: 2048,
			unknownSizes: 0,
			truncated: false,
			items: [
				{
					id: 'sha256:aaaaaaaaaaaaaaaa',
					decision: 'remove',
					reason: 'dangling',
					bytes: 1024
				},
				{
					id: 'sha256:bbbbbbbbbbbbbbbb',
					decision: 'remove',
					reason: 'dangling',
					bytes: 1024
				}
			]
		}
	]
};

function pruneJob(id: string, p: Record<string, unknown> = {}) {
	return {
		id,
		kind: 'prune.run',
		state: 'running',
		origin: 'manual',
		executor: 'agent',
		environmentId: 'env-1',
		targets: [],
		attempt: 1,
		progress: { percent: 40 },
		items: [],
		locks: [],
		locksHeld: true,
		cancelRequested: false,
		cancellable: true,
		retryable: false,
		createdAt: '2026-09-28T10:00:00Z',
		updatedAt: '2026-09-28T10:00:00Z',
		...p
	};
}

function mount(target: PruneTarget, allowed = true, running: ReturnType<typeof pruneJob>[] = []) {
	const calls: { url: string; body: unknown }[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request | string) => {
			const req = input instanceof Request ? input : new Request(input);
			const url = new URL(req.url).pathname;
			const body = req.method === 'POST' ? await req.json() : undefined;
			calls.push({ url, body });
			if (url === '/api/v1/jobs') return json({ items: running, total: running.length });
			const detail = running.find((j) => url === `/api/v1/jobs/${j.id}`);
			if (detail) return json(detail);
			if (url.endsWith('/prune-previews')) return json(preview);
			if (url.endsWith('/prunes'))
				return json({ id: 'job-1', kind: 'prune.run', state: 'queued' }, 202);
			return json({ code: 'forbidden', message: 'forbidden' }, 403);
		})
	);
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness<ComponentProps<typeof PruneButton>>, {
		props: { client, component: PruneButton, props: { target, scope: scope(allowed) } }
	});
	return calls;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('PruneButton (#14)', () => {
	it('is hidden without maintenance.run and maintenance.preview', () => {
		mount('images', false);
		expect(screen.queryByRole('button', { name: 'Prune images' })).not.toBeInTheDocument();
	});

	it('starts with all unused images and previews before removing anything', async () => {
		const user = setup();
		const calls = mount('images');
		await user.click(screen.getByRole('button', { name: 'Prune images' }));
		const dialog = await screen.findByRole('dialog', { name: 'Prune images' });
		expect(dialog).toBeInTheDocument();
		expect(await screen.findByRole('switch', { name: 'Dangling images' })).not.toBeChecked();
		expect(screen.getByRole('switch', { name: 'Unused images' })).toBeChecked();

		await user.click(screen.getByRole('button', { name: 'Preview' }));
		const remove = await screen.findByRole('button', { name: /^Remove 2 objects/ });
		const previewCall = calls.find((c) => c.url.endsWith('/prune-previews'));
		expect(previewCall?.url).toBe('/api/v1/environments/env-1/prune-previews');
		expect(previewCall?.body).toEqual({
			rules: [
				{ category: 'dangling_images', enabled: false, minAgeHours: 0 },
				{ category: 'unused_images', enabled: true, minAgeHours: 0 }
			]
		});
		expect(calls.some((c) => c.url.endsWith('/prunes'))).toBe(false);

		await user.click(remove);
		await waitFor(() => expect(calls.some((c) => c.url.endsWith('/prunes'))).toBe(true));
		const run = calls.find((c) => c.url.endsWith('/prunes'));
		const previewed = previewCall?.body as { rules: unknown[] } | undefined;
		expect(run?.body).toMatchObject({ confirm: true, rules: previewed?.rules });
	});

	it('does not run volume rules before their data-loss opt-in', async () => {
		const user = setup();
		mount('volumes');
		await user.click(screen.getByRole('button', { name: 'Prune volumes' }));
		await screen.findByRole('switch', { name: 'Anonymous volumes' });
		await user.click(screen.getByRole('button', { name: 'Preview' }));
		expect(await screen.findByRole('button', { name: /^Remove 2 objects/ })).toBeDisabled();
		expect(screen.getByText(/deletes their data/)).toBeInTheDocument();
	});

	it('finds a one-off prune running on the environment again and opens on its progress', async () => {
		const user = setup();
		mount('images', true, [pruneJob('0190-2', { policyId: 'pol-1' }), pruneJob('0190-1')]);
		const button = await screen.findByRole('button', { name: 'Pruning…' });
		await user.click(button);
		await screen.findByRole('dialog', { name: 'Prune images' });
		expect(
			await screen.findByRole('progressbar', { name: 'Prune on Silo progress' })
		).toBeInTheDocument();
		// A policy's prune is not this button's: one bar only.
		expect(screen.getAllByRole('progressbar')).toHaveLength(1);
		expect(
			screen.getByRole('button', { name: 'Continue in the background' })
		).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Preview' })).not.toBeInTheDocument();
	});
});
