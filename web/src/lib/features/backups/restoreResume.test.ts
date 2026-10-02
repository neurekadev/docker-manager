// A restore that is still running comes back (docs/internal/web.md, "Job
// progress after reload"): the restore dialog of a stack's Backups tab,
// opened while a restore of that stack runs, shows its progress instead of
// asking to start another one.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import type { Backup } from './model';
import RestoreDialog from './RestoreDialog.svelte';

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

function restore(id: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'restore.run',
		state: 'running',
		origin: 'manual',
		executor: 'agent',
		environmentId: 'env-1',
		targets: [{ type: 'stack', id: 'st-1' }],
		attempt: 1,
		progress: { percent: 50 },
		items: [],
		locks: [],
		locksHeld: true,
		cancelRequested: false,
		cancellable: false,
		retryable: false,
		createdAt: '2026-09-28T10:00:00Z',
		updatedAt: '2026-09-28T10:00:00Z',
		...p
	} as Job;
}

const backup = {
	id: 'bk1',
	kind: 'stack',
	stackId: 'st-1',
	environmentId: 'env-1',
	volumes: ['silo_db'],
	snapshotTime: '2026-09-26T10:00:00Z',
	repositoryId: 'r1',
	state: 'complete',
	view: 'full',
	actions: ['backup.restore']
} as unknown as Backup;

function mount(running: Job[]) {
	const fetched: string[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request | string) => {
			const req = input instanceof Request ? input : new Request(input);
			const url = new URL(req.url).pathname;
			fetched.push(url);
			if (url === '/api/v1/jobs') return json({ items: running, total: running.length });
			const detail = running.find((j) => url === `/api/v1/jobs/${j.id}`);
			if (detail) return json(detail);
			return json({ code: 'not_found', message: 'not found' }, 404);
		})
	);
	render(QueryHarness<ComponentProps<typeof RestoreDialog>>, {
		props: {
			client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
			component: RestoreDialog,
			props: { open: true, backup, plan: { kind: 'full' }, subject: 'Silo' }
		}
	});
	return fetched;
}

afterEach(() => vi.unstubAllGlobals());

describe('RestoreDialog after a reload', () => {
	it('shows the progress of a running restore of the subject instead of the form', async () => {
		mount([restore('0190-2', { targets: [{ type: 'stack', id: 'st-2' }] }), restore('0190-1')]);
		expect(
			await screen.findByRole('progressbar', { name: 'Restore Silo progress' })
		).toBeInTheDocument();
		expect(screen.getAllByRole('progressbar')).toHaveLength(1);
		expect(
			screen.queryByRole('button', { name: 'Replace Everything' })
		).not.toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument();
	});

	it('offers the restore when nothing of the subject is being restored', async () => {
		const fetched = mount([restore('0190-2', { targets: [{ type: 'stack', id: 'st-2' }] })]);
		await waitFor(() => expect(fetched).toContain('/api/v1/jobs'));
		expect(
			await screen.findByRole('button', { name: 'Replace Everything' })
		).toBeInTheDocument();
		expect(screen.queryByRole('progressbar')).not.toBeInTheDocument();
	});
});
