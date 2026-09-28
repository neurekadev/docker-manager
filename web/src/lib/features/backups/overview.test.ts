// Backups overview components (#10): the running backup's current file
// line, the storage card and a set's details drawer.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import RunningBackups from './RunningBackups.svelte';
import SetsTable from './SetsTable.svelte';
import StorageCard from './StorageCard.svelte';
import type { BackupActivity, BackupSet } from './model';

afterEach(() => vi.unstubAllGlobals());

const envName = (id: string) => ({ e1: 'prod', e2: 'edge' })[id] ?? id;

function activity(currentFile?: string): BackupActivity {
	return {
		jobId: 'j1',
		kind: 'backup.run',
		state: 'running',
		setId: 's1',
		policyId: 'p1',
		environmentId: 'e1',
		percent: 20,
		itemCount: 2,
		current: {
			item: 'volume/media',
			kind: 'volume',
			volume: 'media',
			index: 1,
			percent: 50,
			filesDone: 1200,
			filesTotal: 2400,
			bytesDone: 1024 * 1024,
			bytesTotal: 2 * 1024 * 1024,
			secondsRemaining: 180,
			currentFile,
			reportedAt: '2026-09-27T01:00:00Z'
		}
	};
}

describe('RunningBackups (#10)', () => {
	it('shows the progress and, below it, the file being read', () => {
		render(RunningBackups, {
			props: {
				jobs: [activity('media/2026/holiday.jpg')],
				policyName: () => 'Nightly',
				environmentName: envName
			}
		});
		const bar = screen.getByRole('progressbar', { name: 'Backup progress of Nightly, prod' });
		expect(bar).toHaveAttribute('aria-valuenow', '75');
		expect(screen.getByText('Volume media')).toBeInTheDocument();
		expect(screen.getByText('2 of 2')).toBeInTheDocument();
		expect(screen.getByText('1,200 of 2,400 files')).toBeInTheDocument();
		expect(screen.getByText('About 3 min left')).toBeInTheDocument();
		expect(screen.getByText('media/2026/holiday.jpg')).toBeInTheDocument();
	});

	it('shows no path when the server withholds it', () => {
		render(RunningBackups, {
			props: { jobs: [activity()], policyName: () => 'Nightly', environmentName: envName }
		});
		expect(screen.getByText('Reading files')).toBeInTheDocument();
		expect(screen.queryByText(/holiday/)).toBeNull();
	});

	it('says why a job waits', () => {
		const waiting = { ...activity(), state: 'blocked' as const, current: undefined };
		render(RunningBackups, {
			props: { jobs: [waiting], policyName: () => 'Nightly', environmentName: envName }
		});
		expect(
			screen.getAllByText('Waiting for another job on the same data').length
		).toBeGreaterThan(0);
	});
});

describe('StorageCard (#10)', () => {
	it('leads with what is stored, then the data, savings and ratio', () => {
		const GiB = 1024 ** 3;
		render(StorageCard, {
			props: {
				totals: {
					sizeBytes: 79.6 * GiB,
					uncompressedBytes: 160 * GiB,
					freedBytes: 80.4 * GiB,
					ratio: 160 / 79.6,
					compressedPercent: 100,
					snapshots: 289,
					measuredAt: '2026-09-27T01:00:00Z',
					repositories: [
						{ id: 'a', name: 'Local', sizeBytes: 1, uncompressedBytes: 2, ratio: 2 },
						{ id: 'b', name: 'Offsite', sizeBytes: 1, uncompressedBytes: 2, ratio: 2 }
					]
				}
			}
		});
		expect(screen.getByText(/stored in/)).toHaveTextContent('79.6 GB stored in 2 repositories');
		expect(
			screen.getByRole('meter', { name: 'Stored size of the backed-up data' })
		).toHaveAttribute('aria-valuetext', '79.6 GB stored for 160 GB of data');
		const stat = (label: string) =>
			screen.getByText(label, { selector: 'dt' }).nextElementSibling;
		expect(stat('Unique data backed up')).toHaveTextContent('160 GB');
		expect(stat('Saved by compression')).toHaveTextContent('80.4 GB');
		expect(stat('Compression ratio')).toHaveTextContent('2.01x');
		// At most three figures: no snapshot count, no share compressed.
		expect(screen.getAllByRole('term')).toHaveLength(3);
		expect(screen.queryByText(/snapshot/i)).toBeNull();
		expect(
			within(screen.getByRole('list', { name: 'Storage per repository' })).getAllByRole(
				'listitem'
			)
		).toHaveLength(2);
	});

	it('invites the next backup before anything was measured', () => {
		render(StorageCard, { props: { totals: undefined } });
		expect(screen.getByText('Not measured yet.')).toBeInTheDocument();
	});
});

describe('SetsTable (#10)', () => {
	it('keeps each set on one line and opens its backups in a drawer', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => new Response('{}'))
		);
		const set: BackupSet & { policyId: string; policyName: string } = {
			id: 's1',
			state: 'partial',
			origin: 'scheduled',
			startedAt: '2026-09-27T01:00:00Z',
			finishedAt: '2026-09-27T01:02:00Z',
			policyId: 'p1',
			policyName: 'Nightly',
			members: [
				{
					item: 'volume/media',
					kind: 'volume',
					scope: 'env:e1',
					volume: 'media',
					environmentId: 'e1',
					state: 'complete'
				},
				{
					item: 'volume/db',
					kind: 'volume',
					scope: 'env:e2',
					volume: 'db',
					environmentId: 'e2',
					state: 'failed',
					errorClass: 'volume_missing'
				},
				{ item: 'manager', kind: 'manager_state', scope: 'manager', state: 'complete' }
			]
		};
		render(QueryHarness<ComponentProps<typeof SetsTable>>, {
			props: {
				client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
				component: SetsTable,
				props: {
					sets: [set],
					label: 'Recent backup sets',
					environmentName: envName,
					backups: [{ setId: 's1', bytes: 2048 }] as ComponentProps<
						typeof SetsTable
					>['backups']
				}
			}
		});
		expect(screen.getByText('2 of 3 complete · 2 environments')).toBeInTheDocument();
		expect(screen.queryByText('db')).toBeNull();
		await user.click(screen.getByRole('button', { name: 'Details' }));
		const drawer = await screen.findByRole('dialog');
		expect(within(drawer).getByRole('heading', { name: /prod/ })).toBeInTheDocument();
		expect(within(drawer).getByRole('heading', { name: /edge/ })).toBeInTheDocument();
		expect(within(drawer).getByRole('heading', { name: /Manager/ })).toBeInTheDocument();
		expect(within(drawer).getByText('db')).toBeInTheDocument();
		expect(within(drawer).getByText('2 min')).toBeInTheDocument();
		expect(within(drawer).getByText('2 KB')).toBeInTheDocument();
	});
});
