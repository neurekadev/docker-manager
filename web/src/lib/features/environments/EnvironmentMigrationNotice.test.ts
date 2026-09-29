// The environment page's notice about old copies (#35): moved stacks whose
// stopped copies are still on the environment, with "Review the
// migration"; nothing once they are removed or for callers the list
// refuses; and a job event (the jobs list key) refreshing it.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import { liveKeys } from '$lib/live/keys';
import QueryHarness from '../../../test/QueryHarness.svelte';
import EnvironmentMigrationNotice from './EnvironmentMigrationNotice.svelte';
import type { EnvironmentMigration } from './environment-migration';

const record = (
	stacks: EnvironmentMigration['stacks'],
	over: Partial<EnvironmentMigration> = {}
): EnvironmentMigration => ({
	id: 'job-e',
	sourceEnvironmentId: 'env-1',
	targetEnvironmentId: 'env-2',
	state: 'completed',
	groups: [],
	stacks,
	networks: [],
	createdAt: '2026-09-29T10:00:00Z',
	updatedAt: '2026-09-29T10:00:00Z',
	...over
});

const moved = (stackId: string, name: string, sourceRemoved = false) => ({
	stackId,
	name,
	state: 'moved' as const,
	migrationId: `m-${stackId}`,
	sourceRemoved: sourceRemoved || undefined
});

let recent: EnvironmentMigration[] = [];
let status = 200;
let listed = 0;

beforeEach(() => {
	recent = [];
	status = 200;
	listed = 0;
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const path = new URL(req.url).pathname;
		const json = (code: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status: code,
				headers: { 'Content-Type': 'application/json' }
			});
		if (path === '/api/v1/environments')
			return json(200, {
				items: [
					{
						id: 'env-1',
						name: 'homelab',
						online: true,
						status: 'active',
						view: 'full',
						actions: []
					},
					{
						id: 'env-2',
						name: 'nas',
						online: true,
						status: 'active',
						view: 'full',
						actions: []
					}
				]
			});
		if (path === '/api/v1/environments/env-1/migrations') {
			listed++;
			if (status !== 200)
				return json(status, {
					code: 'forbidden',
					message: 'not permitted',
					details: [],
					requestId: 'r',
					retryable: false
				});
			return json(200, { items: recent });
		}
		return json(404, {
			code: 'not_found',
			message: 'no',
			details: [],
			requestId: 'r',
			retryable: false
		});
	});
});
afterEach(() => vi.unstubAllGlobals());

function notice(props: Record<string, unknown> = {}) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: EnvironmentMigrationNotice as unknown as Component<Record<string, unknown>>,
			props: { environmentId: 'env-1', ...props }
		}
	});
	return client;
}

describe('EnvironmentMigrationNotice', () => {
	it('says how many stacks moved and links to the migration', async () => {
		recent = [record([moved('st-1', 'proxy'), moved('st-2', 'app')])];
		notice();

		expect(await screen.findByText('2 stacks moved to nas')).toBeInTheDocument();
		expect(screen.getByText('Their old copies are still on this server.')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'Review the migration' })).toHaveAttribute(
			'href',
			'/environments/env-1/migrate'
		);
	});

	it('refreshes on job events and goes away once the copies are removed', async () => {
		recent = [record([moved('st-1', 'proxy'), moved('st-2', 'app', true)])];
		const client = notice();

		expect(await screen.findByText('1 stack moved to nas')).toBeInTheDocument();
		expect(screen.getByText('Its old copy is still on this server.')).toBeInTheDocument();

		// The removal ended: its job event invalidates the jobs lists.
		recent = [record([moved('st-1', 'proxy', true), moved('st-2', 'app', true)])];
		await client.invalidateQueries({ queryKey: liveKeys.list('jobs') });
		await waitFor(() => expect(screen.queryByText('1 stack moved to nas')).toBeNull());
		expect(screen.queryByRole('link', { name: 'Review the migration' })).toBeNull();
	});

	it('shows nothing while a migration still runs, when refused or when not asked', async () => {
		recent = [record([moved('st-1', 'proxy')], { state: 'running' })];
		notice();
		await waitFor(() => expect(listed).toBe(1));
		expect(screen.queryByRole('link', { name: 'Review the migration' })).toBeNull();

		status = 403;
		notice();
		await waitFor(() => expect(listed).toBe(2));
		expect(screen.queryByRole('link', { name: 'Review the migration' })).toBeNull();

		notice({ enabled: false });
		expect(listed).toBe(2);
		expect(screen.queryByRole('link', { name: 'Review the migration' })).toBeNull();
	});
});
