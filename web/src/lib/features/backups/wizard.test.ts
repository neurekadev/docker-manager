// Backup policy form (#10): creating walks through the wizard's steps
// (Cancel on each, visited steps reopen) and nothing is saved before the
// last one; editing is one screen saved once with Save changes. Retention
// starts on a preset; Custom reveals the rules.
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
	buildxVolumes: false,
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
			// The schedule preview is a read (POST with a body), not a write.
			if (url.pathname === '/api/v1/schedules/previews')
				return json({
					cron: '0 2 * * *',
					timeZone: 'UTC',
					from: '2026-09-27T00:00:00Z',
					runs: [],
					notes: []
				});
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
	it('editing shows every section on one screen and saves once with Save changes', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const writes = stubApi([policy]);
		const ondone = vi.fn();
		const oncancel = vi.fn();
		renderWizard({ policy, owner: false, ondone, oncancel });
		for (const section of ['Name and destination', 'What to back up', 'Schedule', 'Retention'])
			expect(
				await screen.findByRole('heading', { name: section, level: 3 })
			).toBeInTheDocument();
		// No wizard steps when editing.
		expect(screen.queryByRole('button', { name: 'Next' })).toBeNull();
		expect(screen.getByRole('textbox', { name: /^Name/ })).toHaveValue('Nightly');
		// The repositories load with the form: the policy's and the one for prod.
		await vi.waitFor(() =>
			expect(screen.getAllByRole('combobox', { name: /^Repository/ })).toHaveLength(2)
		);
		// Rules no preset matches open as Custom with their fields.
		expect(screen.getByRole('radio', { name: /^Custom/ })).toBeChecked();
		expect(screen.getByRole('spinbutton', { name: /^Daily/ })).toHaveValue(7);
		await user.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(oncancel).toHaveBeenCalledTimes(1);
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
		const oncancel = vi.fn();
		renderWizard({ owner: false, ondone, oncancel });
		await heading('Destination');
		expect(screen.queryByRole('button', { name: 'Back' })).toBeNull();
		await user.type(screen.getByRole('textbox', { name: /^Name/ }), 'Nightly');
		await choose(user, await screen.findByRole('combobox', { name: /^Repository/ }), /^Local/);
		await user.click(screen.getByRole('button', { name: 'Next' }));
		expect(await heading('What to back up')).toBeInTheDocument();
		// Consistency is part of this step now.
		expect(
			screen.getByRole('switch', { name: /Stop containers during backups/ })
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Back' })).toBeInTheDocument();
		await user.click(screen.getByRole('switch', { name: /Back up anonymous volumes/ }));
		for (const next of ['Schedule', 'Retention']) {
			await user.click(screen.getByRole('button', { name: 'Next' }));
			expect(await heading(next)).toBeInTheDocument();
		}
		// A visited step opens again from the step list.
		await user.click(screen.getByRole('button', { name: /^Destination/ }));
		expect(await heading('Destination')).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: /^Retention/ }));
		expect(await heading('Retention')).toBeInTheDocument();
		// New policies start on the recommended preset; Custom reveals the rules.
		expect(
			screen.getByRole('radio', { name: /7 daily, 4 weekly, 12 monthly \(recommended\)/ })
		).toBeChecked();
		expect(screen.queryByRole('spinbutton', { name: /^Daily/ })).toBeNull();
		await user.click(screen.getByRole('radio', { name: /^Custom/ }));
		expect(screen.getByRole('spinbutton', { name: /^Daily/ })).toHaveValue(7);
		await user.click(screen.getByRole('radio', { name: /^Keep the last 30/ }));
		expect(
			screen.getByText('Keep last 30; always keeps the newest 1 of each.')
		).toBeInTheDocument();
		expect(writes).toEqual([]);
		await user.click(screen.getByRole('button', { name: 'Cancel' }));
		expect(oncancel).toHaveBeenCalledTimes(1);
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
				shutdown: false,
				schedule: { enabled: false },
				retention: {
					last: 30,
					hourly: 0,
					daily: 0,
					weekly: 0,
					monthly: 0,
					yearly: 0,
					withinDays: 0,
					minKeep: 0
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
		await choose(user, await screen.findByRole('combobox', { name: /^Repository/ }), /^Local/);
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
