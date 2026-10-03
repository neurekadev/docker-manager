// The backup settings dialog (#10, #246): one screen saved once with Save
// Changes. The Primary and Secondary repositories are chosen from the
// repositories; the Secondary must differ and backups need a Primary to be
// turned on. Retention opens on the preset the rules match.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { choose } from '../../../test/select';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import BackupSettingsDialog from './BackupSettingsDialog.svelte';
import type { BackupSettings } from './model';

function json(body: unknown, status = 200) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

const settings: BackupSettings = {
	id: 'bs1',
	enabled: false,
	primaryRepositoryId: 'r1',
	secondaryRepositoryId: '',
	schedule: { cron: '0 2 * * *', timeZone: 'UTC' },
	excludeEnvironments: [],
	excludeStacks: [],
	excludeVolumes: [],
	anonymousVolumes: false,
	buildxVolumes: false,
	externalBinds: false,
	includeMetrics: false,
	shutdown: false,
	retention: { daily: 7 },
	recentSets: [],
	actions: ['backup_policy.manage'],
	revision: 3,
	updatedAt: '2026-09-27T00:00:00Z'
};

/** Stubs the API; returns the writes (method and body) in order. A write
 * answers `reject` when given. */
function stubApi(reject?: () => Response) {
	const writes: { method: string; path: string; body: unknown; ifMatch: string | null }[] = [];
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
				writes.push({
					method: input.method,
					path: url.pathname,
					body,
					ifMatch: input.headers.get('If-Match')
				});
				return reject?.() ?? json({ ...settings, ...(body as object), revision: 4 });
			}
			switch (url.pathname) {
				case '/api/v1/environments':
					return json({
						items: [
							{ id: 'e1', name: 'prod', status: 'active', online: true },
							{ id: 'e2', name: 'lab', status: 'active', online: false }
						],
						nextCursor: null
					});
				case '/api/v1/backup-repositories':
					return json({
						items: [
							{ id: 'r1', name: 'Offsite', state: 'ready', role: 'primary' },
							{ id: 'r2', name: 'NAS', state: 'ready' },
							{ id: 'r3', name: 'New', state: 'awaiting_confirmation' }
						],
						nextCursor: null
					});
				case '/api/v1/schedule-defaults':
					return json({ timeZone: 'UTC', kinds: [] });
				default:
					return json({ items: [], nextCursor: null });
			}
		})
	);
	return writes;
}

function renderDialog(props: ComponentProps<typeof BackupSettingsDialog>) {
	render(QueryHarness<ComponentProps<typeof BackupSettingsDialog>>, {
		props: {
			client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
			component: BackupSettingsDialog,
			props
		}
	});
}

afterEach(() => vi.unstubAllGlobals());

describe('BackupSettingsDialog (#246)', () => {
	it('shows every section on one screen and saves once with Save Changes', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const writes = stubApi();
		renderDialog({ settings });
		for (const section of ['What to Back Up', 'Where and When'])
			expect(
				await screen.findByRole('heading', { name: section, level: 3 })
			).toBeInTheDocument();
		for (const group of ['Repositories', 'Schedule', 'Retention'])
			expect(screen.getByRole('group', { name: group })).toBeInTheDocument();
		// The schedule shows only while backups run automatically.
		expect(screen.queryByRole('group', { name: 'Backup Schedule' })).toBeNull();
		// Rules no preset matches open as Custom with their fields.
		expect(screen.getByRole('radio', { name: /^Custom/ })).toBeChecked();
		expect(screen.getByRole('spinbutton', { name: /^Daily/ })).toHaveValue(7);
		// The manager state is always backed up: no switch for it.
		expect(screen.queryByRole('switch', { name: /Back Up the Manager State/ })).toBeNull();

		await choose(
			user,
			await screen.findByRole('combobox', { name: /^Secondary Repository/ }),
			/^NAS/
		);
		await user.click(screen.getByRole('switch', { name: /Back Up Automatically/ }));
		expect(screen.getByRole('group', { name: 'Backup Schedule' })).toBeInTheDocument();
		expect(writes).toEqual([]);
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		await vi.waitFor(() => expect(writes).toHaveLength(1));
		expect(writes[0]).toMatchObject({
			method: 'PATCH',
			path: '/api/v1/backup-settings',
			ifMatch: '"3"',
			body: {
				enabled: true,
				primaryRepositoryId: 'r1',
				secondaryRepositoryId: 'r2',
				excludeEnvironments: [],
				schedule: { cron: '0 2 * * *', timeZone: 'UTC' },
				retention: { daily: 7 }
			}
		});
	});

	it('needs a Primary repository to turn backups on', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const writes = stubApi();
		renderDialog({ settings: { ...settings, primaryRepositoryId: '' } });
		await screen.findByRole('heading', { name: 'Where and When', level: 3 });
		await user.click(screen.getByRole('switch', { name: /Back Up Automatically/ }));
		expect(await screen.findByText(/Choose a Primary repository/)).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled();
		expect(writes).toEqual([]);
	});

	it('saves with backups off and an emptied schedule, keeping the saved one', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const writes = stubApi();
		renderDialog({ settings: { ...settings, schedule: { cron: '', timeZone: 'UTC' } } });
		await screen.findByRole('heading', { name: 'Where and When', level: 3 });
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		await vi.waitFor(() => expect(writes).toHaveLength(1));
		expect(writes[0].body).not.toHaveProperty('schedule');
	});

	it('shows a schedule error the server returns while the schedule is hidden', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		stubApi(() =>
			json(
				{
					code: 'validation_failed',
					message: 'invalid request',
					requestId: 'r',
					retryable: false,
					details: [{ field: 'body.schedule.cron', message: 'Use five fields.' }]
				},
				422
			)
		);
		renderDialog({ settings });
		await screen.findByRole('heading', { name: 'Where and When', level: 3 });
		await user.click(screen.getByRole('button', { name: 'Save Changes' }));
		expect(await screen.findByRole('alert')).toHaveTextContent('Use five fields.');
	});
});
