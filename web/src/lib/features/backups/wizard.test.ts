// Backup policy wizard (#10): the steps advance, and nothing is saved
// before the last one (a new policy is created with every setting at once,
// an edited one is saved once).
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { choose } from '../../../test/select';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import PolicyWizard from './PolicyWizard.svelte';
import type { BackupPolicy } from './model';

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

const policy: BackupPolicy = {
	id: 'bp1',
	name: 'Nightly',
	scope: 'environment',
	environmentId: 'e1',
	excludeStacks: [],
	excludeVolumes: [],
	anonymousVolumes: false,
	enabled: false,
	view: 'full',
	actions: ['backup_policy.manage'],
	repositoryId: 'r1',
	includeManagerState: false,
	includeMetrics: false,
	stacks: [],
	volumes: [],
	shutdown: false,
	schedule: { cron: '0 2 * * *', timeZone: 'UTC', enabled: false },
	retention: { daily: 7, minKeep: 1 },
	revision: 3
};

/** Stubs the API; returns the writes (method and body) in order. */
function stubApi(existing: BackupPolicy[] = []) {
	const writes: { method: string; path: string; body: unknown }[] = [];
	vi.stubGlobal(
		'fetch',
		vi.fn(async (input: Request) => {
			const url = new URL(input.url);
			if (input.method !== 'GET') {
				const body = await input.json().catch(() => undefined);
				writes.push({ method: input.method, path: url.pathname, body });
				return json({ ...policy, ...(body as object), id: 'bp1', revision: 4 });
			}
			switch (url.pathname) {
				case '/api/v1/environments':
					return json({
						items: [{ id: 'e1', name: 'prod', status: 'active', online: true }],
						nextCursor: null
					});
				case '/api/v1/backup-repositories':
					return json({
						items: [
							{
								id: 'r1',
								name: 'Local',
								kind: 'local',
								state: 'ready',
								executor: 'e1'
							}
						],
						nextCursor: null
					});
				case '/api/v1/backup-policies':
					return json({ items: existing, nextCursor: null });
				case '/api/v1/schedule-defaults':
					return json({ timeZone: 'UTC', kinds: [] });
				default:
					return json({ items: [], nextCursor: null });
			}
		})
	);
	return writes;
}

function renderWizard(props: ComponentProps<typeof PolicyWizard>) {
	render(QueryHarness<ComponentProps<typeof PolicyWizard>>, {
		props: {
			client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
			component: PolicyWizard,
			props
		}
	});
}

const heading = (name: string) => screen.findByRole('heading', { name, level: 2 });

afterEach(() => vi.unstubAllGlobals());

describe('PolicyWizard (#10)', () => {
	it('editing moves on to what to back up and saves only at the end', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const writes = stubApi([policy]);
		const ondone = vi.fn();
		renderWizard({ policy, owner: false, ondone });
		expect(await heading('Destination')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Next' }));
		expect(await heading('What to back up')).toBeInTheDocument();
		expect(screen.queryByRole('textbox', { name: /^Name/ })).not.toBeInTheDocument();
		for (const next of ['Consistency', 'Schedule', 'Retention']) {
			await user.click(screen.getByRole('button', { name: 'Next' }));
			expect(await heading(next)).toBeInTheDocument();
		}
		expect(writes).toEqual([]);
		await user.click(screen.getByRole('button', { name: 'Save changes' }));
		await vi.waitFor(() => expect(ondone).toHaveBeenCalled());
		expect(writes.map((w) => `${w.method} ${w.path}`)).toEqual([
			'PATCH /api/v1/backup-policies/bp1'
		]);
	});

	it('creating writes nothing until Create policy, then creates it with every setting', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const writes = stubApi();
		const ondone = vi.fn();
		renderWizard({ owner: false, ondone });
		await heading('Destination');
		await user.type(screen.getByRole('textbox', { name: /^Name/ }), 'Nightly');
		await choose(user, await screen.findByRole('combobox', { name: /^Repository/ }), 'Local');
		await user.click(screen.getByRole('button', { name: 'Next' }));
		expect(await heading('What to back up')).toBeInTheDocument();
		await user.click(screen.getByRole('switch', { name: /Back up anonymous volumes/ }));
		for (const next of ['Consistency', 'Schedule', 'Retention']) {
			await user.click(screen.getByRole('button', { name: 'Next' }));
			expect(await heading(next)).toBeInTheDocument();
		}
		expect(writes).toEqual([]);
		await user.click(screen.getByRole('button', { name: 'Create policy' }));
		await vi.waitFor(() => expect(ondone).toHaveBeenCalled());
		expect(writes).toHaveLength(1);
		expect(writes[0]).toMatchObject({
			method: 'POST',
			path: '/api/v1/backup-policies',
			body: {
				name: 'Nightly',
				scope: 'all',
				repositoryId: 'r1',
				anonymousVolumes: true,
				schedule: { enabled: false },
				retention: {
					last: 168,
					hourly: 0,
					daily: 0,
					weekly: 0,
					monthly: 0,
					yearly: 0,
					withinDays: 0,
					minKeep: 1
				}
			}
		});
	});

	it('refuses a scope another policy covers before going on', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const writes = stubApi([
			{ ...policy, name: 'Everything', scope: 'all', environmentId: '' }
		]);
		renderWizard({ owner: false, ondone: vi.fn() });
		await heading('Destination');
		await user.type(screen.getByRole('textbox', { name: /^Name/ }), 'Nightly');
		await choose(user, await screen.findByRole('combobox', { name: /^Repository/ }), 'Local');
		// The existing policies load with the page; Next stays on this step.
		await screen.findByRole('button', { name: 'Next' });
		await vi.waitFor(async () => {
			await user.click(screen.getByRole('button', { name: 'Next' }));
			expect(
				screen.getByText(/Everything already covers all environments/)
			).toBeInTheDocument();
		});
		expect(await heading('Destination')).toBeInTheDocument();
		expect(writes).toEqual([]);
	});
});
