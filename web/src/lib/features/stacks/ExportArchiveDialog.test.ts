// Export Archive (#313): the check runs at once, lists the volumes with an
// Include tick (unticking leaves a volume out and checks again; a volume
// the user may not download is left out from the start), problems keep
// Export Archive off, the newest archive can be downloaded, and a running
// export shows its progress when the dialog opens.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import type { Job } from '$lib/api/client';
import QueryHarness from '../../../test/QueryHarness.svelte';
import ExportArchiveDialog from './ExportArchiveDialog.svelte';
import type { StackExportPreview } from './archives';
import type { Stack } from './queries';
import { JobTray } from './tray.svelte';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

const stack = {
	id: 'st-1',
	environmentId: 'env-1',
	name: 'silo',
	displayName: 'Silo',
	status: 'deployed',
	view: 'full',
	actions: ['stack.export', 'stack.files.download', 'stack.definition.read'],
	revision: 3,
	environmentOnline: true
} as unknown as Stack;

// The check's answer for the volumes left out (`excludeVolumes`).
let answer: (excluded: string[]) => Partial<StackExportPreview>;
let checks: unknown[] = [];
let started: unknown[] = [];
let running: Job[] = [];
// The answer of GET /jobs/job-e.
let watched: Job;

function preview(excluded: string[], over: Partial<StackExportPreview> = {}): StackExportPreview {
	const data = !excluded.includes('data');
	return {
		stackId: 'st-1',
		allowed: true,
		blockers: [],
		warnings: [],
		volumes: [
			{
				key: 'data',
				name: 'silo_data',
				included: data,
				excluded: !data || undefined,
				bytes: 2048,
				entries: 3
			},
			{
				key: 'shared',
				name: 'shared',
				included: false,
				reason: 'external volume: it must exist where the stack is created',
				bytes: 0,
				entries: 0
			}
		],
		notIncluded: [{ kind: 'bind', name: '/srv/media', reason: 'outside the project folder' }],
		projectBytes: 1024,
		volumeBytes: data ? 2048 : 0,
		totalBytes: data ? 3072 : 1024,
		managerFreeBytes: -1,
		maxBytes: 1 << 30,
		running: ['web'],
		downtimeSeconds: 30,
		...over
	};
}

const exportJob = {
	id: 'job-e',
	kind: 'stack.export',
	state: 'running',
	origin: 'manual',
	executor: 'manager',
	environmentId: 'env-1',
	targets: [{ type: 'stack', id: 'st-1' }],
	attempt: 1,
	progress: { percent: 40, step: 'write_archive' },
	items: [],
	locks: [],
	locksHeld: true,
	cancelRequested: false,
	cancellable: true,
	retryable: false,
	createdAt: '2026-10-10T10:00:00Z',
	updatedAt: '2026-10-10T10:00:00Z'
} as Job;

beforeEach(() => {
	checks = [];
	started = [];
	running = [];
	watched = exportJob;
	answer = () => ({});
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const text = await req.text();
		const body = text ? JSON.parse(text) : undefined;
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname === '/api/v1/stacks/st-1/export-previews') {
			checks.push(body);
			const excluded: string[] = body?.excludeVolumes ?? [];
			return json(200, preview(excluded, answer(excluded)));
		}
		if (url.pathname === '/api/v1/stacks/st-1/exports' && req.method === 'POST') {
			started.push({ body, key: req.headers.get('Idempotency-Key') });
			return json(202, { ...exportJob, state: 'queued' });
		}
		if (url.pathname === '/api/v1/jobs')
			return json(200, { items: running, total: running.length });
		if (url.pathname === '/api/v1/jobs/job-e') return json(200, watched);
		return json(404, {
			code: 'not_found',
			message: 'no',
			details: [],
			requestId: 'r',
			retryable: false
		});
	});
});
afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

function dialog(tray = new JobTray()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: ExportArchiveDialog as unknown as Component<Record<string, unknown>>,
			props: { open: true, stack, tray }
		}
	});
	return tray;
}

describe('ExportArchiveDialog', () => {
	it('checks at once and lists the volumes, the downtime and what is not included', async () => {
		dialog();
		expect(
			await screen.findByRole('dialog', { name: 'Export Silo as an Archive' })
		).toBeInTheDocument();
		const table = await screen.findByRole('table', { name: 'Volumes of Silo' });
		expect(checks).toEqual([{}]);
		const data = within(table).getByRole('checkbox', {
			name: 'Include the Data of Volume data'
		});
		expect(data).toBeChecked();
		expect(data).toBeEnabled();
		const shared = within(table).getByRole('checkbox', {
			name: 'Include the Data of Volume shared'
		});
		expect(shared).not.toBeChecked();
		expect(shared).toBeDisabled();
		expect(
			within(table).getByText('External volume: it must exist where the stack is created.')
		).toBeInTheDocument();
		expect(
			screen.getByText('Silo stops while the archive is written and starts again afterwards.')
		).toBeInTheDocument();
		expect(screen.getByText('Not Included (1)')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Export Archive' })).toBeEnabled();
	});

	it('leaves out an unticked volume and checks again without it', async () => {
		const user = setup();
		dialog();
		const box = await screen.findByRole('checkbox', {
			name: 'Include the Data of Volume data'
		});
		await user.click(box);
		await waitFor(() => expect(checks).toEqual([{}, { excludeVolumes: ['data'] }]), {
			timeout: 2000
		});
		await user.click(screen.getByRole('button', { name: 'Export Archive' }));
		await waitFor(() => expect(started).toHaveLength(1));
		expect(started[0]).toMatchObject({ body: { excludeVolumes: ['data'] } });
		expect((started[0] as { key: string }).key).toBeTruthy();
	});

	it('leaves out a volume the user may not download from the start', async () => {
		// The manager marks it in every answer; left in, it blocks the export.
		answer = (excluded) => {
			const out = !excluded.includes('data');
			const volumes = preview(excluded).volumes.map((v) =>
				v.key === 'data'
					? {
							...v,
							included: false,
							notPermitted: true,
							reason: out ? "you may not download this volume's files" : undefined
						}
					: v
			);
			return out
				? {
						volumes,
						allowed: false,
						blockers: [
							{
								code: 'volume_not_permitted',
								message:
									'you may not download the files of volume silo_data; leave it out'
							}
						]
					}
				: { volumes };
		};
		dialog();
		await waitFor(() => expect(checks).toEqual([{}, { excludeVolumes: ['data'] }]));
		const box = await screen.findByRole('checkbox', {
			name: 'Include the Data of Volume data'
		});
		expect(box).not.toBeChecked();
		expect(box).toBeDisabled();
		expect(screen.getByText("You can't download this volume's files.")).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Export Archive' })).toBeEnabled()
		);
	});

	it('keeps Export Archive off while the check has problems', async () => {
		answer = () => ({
			allowed: false,
			blockers: [
				{
					code: 'archive_too_large',
					message: 'the data (3 GB) exceeds the archive limit of 2 GB'
				}
			]
		});
		dialog();
		expect(await screen.findByText('To Fix Before Exporting')).toBeInTheDocument();
		expect(
			screen.getByText('The data (3 GB) exceeds the archive limit of 2 GB')
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Export Archive' })).toBeDisabled();
		expect(screen.getByText('Fix the problem first.')).toBeInTheDocument();
	});

	it('offers the newest archive for download', async () => {
		const user = setup();
		const clicked: string[] = [];
		vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
			this: HTMLAnchorElement
		) {
			clicked.push(this.getAttribute('href') ?? '');
		});
		answer = () => ({
			latest: {
				exportId: 'job-old',
				fileName: 'silo-2026-10-09.tar.gz',
				size: 1_048_576,
				sha256: 'abc',
				createdAt: '2026-10-09T10:00:00Z',
				expiresAt: '2026-10-10T10:00:00Z',
				volumes: ['data']
			}
		});
		dialog();
		expect(
			await screen.findByText('Latest archive: silo-2026-10-09.tar.gz')
		).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Download Archive' }));
		expect(clicked).toEqual(['/api/v1/stacks/st-1/exports/job-old']);
		expect(await screen.findByText('Downloading silo-2026-10-09.tar.gz')).toBeInTheDocument();
	});

	it('shows a running export when it opens, instead of the stack’s job tray', async () => {
		running = [exportJob];
		const tray = dialog();
		expect(
			await screen.findByRole('progressbar', { name: 'Export Silo as an Archive progress' })
		).toBeInTheDocument();
		expect(tray.jobs).toEqual([]);
		expect(screen.queryByRole('button', { name: 'Export Archive' })).toBeNull();
	});

	it('downloads the archive at once when the export it shows succeeds', async () => {
		const clicked: string[] = [];
		vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
			this: HTMLAnchorElement
		) {
			clicked.push(this.getAttribute('href') ?? '');
		});
		running = [exportJob];
		watched = { ...exportJob, state: 'succeeded' };
		answer = () => ({
			latest: {
				exportId: 'job-e',
				fileName: 'silo-2026-10-10.tar.gz',
				size: 2048,
				sha256: 'abc',
				createdAt: '2026-10-10T10:00:00Z',
				expiresAt: '2026-10-11T10:00:00Z',
				volumes: ['data']
			}
		});
		dialog();
		expect(await screen.findByText('Downloading silo-2026-10-10.tar.gz')).toBeInTheDocument();
		expect(clicked).toEqual(['/api/v1/stacks/st-1/exports/job-e']);
		expect(screen.getByText('Exported Silo as an archive.')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Download Archive' })).toBeInTheDocument();
	});

	it('downloads nothing when the manager names no archive after the export', async () => {
		const clicked: string[] = [];
		vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
			this: HTMLAnchorElement
		) {
			clicked.push(this.getAttribute('href') ?? '');
		});
		running = [exportJob];
		watched = { ...exportJob, state: 'succeeded' };
		dialog();
		expect(
			await screen.findByText("You can't download this archive's volumes.")
		).toBeInTheDocument();
		expect(clicked).toEqual([]);
		expect(screen.queryByRole('button', { name: 'Download Archive' })).toBeNull();
	});
});
