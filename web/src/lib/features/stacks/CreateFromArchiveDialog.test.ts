// Create Stack From Archive (#313): the chosen file uploads at once with
// its progress, the summary shows what the archive holds, the environment
// picker is hidden with one environment, the check runs on its own and
// its problems keep Create Stack off, Create Stack starts the job, and
// Cancel discards the upload.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import type { XhrLike } from './archive-upload';
import type { StackArchive, StackArchiveImportPreview } from './archives';
import CreateFromArchiveDialog from './CreateFromArchiveDialog.svelte';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

class FakeXhr implements XhrLike {
	static last: FakeXhr | null = null;
	method = '';
	url = '';
	body: Blob | null = null;
	status = 0;
	responseText = '';
	upload: XhrLike['upload'] = { onprogress: null };
	onload: (() => void) | null = null;
	onerror: (() => void) | null = null;
	onabort: (() => void) | null = null;

	constructor() {
		FakeXhr.last = this;
	}
	open(m: string, u: string) {
		this.method = m;
		this.url = u;
	}
	setRequestHeader() {}
	send(b: Blob) {
		this.body = b;
	}
	abort() {
		this.onabort?.();
	}
	respond(status: number, body: unknown) {
		this.status = status;
		this.responseText = JSON.stringify(body);
		this.onload?.();
	}
}

const archive: StackArchive = {
	id: 'ar-1',
	size: 4096,
	sha256: 'abc',
	createdAt: '2026-10-10T10:00:00Z',
	expiresAt: '2026-10-11T10:00:00Z',
	exportedAt: '2026-10-09T08:00:00Z',
	name: 'silo',
	displayName: 'Silo',
	projectBytes: 1024,
	projectEntries: 4,
	volumes: [
		{ key: 'data', name: 'silo_data', bytes: 1024, entries: 2 },
		{ key: 'cache', name: 'silo_cache', bytes: 2048, entries: 2 }
	],
	notIncluded: [{ kind: 'bind', name: '/srv/media', reason: 'outside the project folder' }],
	services: ['web', 'db']
};

let checks: unknown[] = [];
let imports: unknown[] = [];
let deleted: string[] = [];
let check: Partial<StackArchiveImportPreview> = {};

beforeEach(() => {
	checks = [];
	imports = [];
	deleted = [];
	check = {};
	FakeXhr.last = null;
	vi.stubGlobal('XMLHttpRequest', FakeXhr);
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
		if (req.method === 'DELETE') {
			deleted.push(url.pathname);
			return new Response(null, { status: 204 });
		}
		if (url.pathname === '/api/v1/stack-archives/ar-1/import-previews') {
			checks.push(body);
			return json(200, {
				archiveId: 'ar-1',
				environmentId: body.environmentId,
				name: body.name,
				allowed: true,
				blockers: [],
				warnings: [],
				volumes: [
					{ key: 'data', source: 'silo_data', name: `${body.name}_data`, bytes: 1024 },
					{ key: 'cache', source: 'silo_cache', name: `${body.name}_cache`, bytes: 2048 }
				],
				projectBytes: 1024,
				volumeBytes: 3072,
				stacksFreeBytes: -1,
				volumesFreeBytes: -1,
				...check
			});
		}
		if (url.pathname === '/api/v1/stack-archives/ar-1/imports') {
			imports.push(body);
			return json(202, {
				id: 'job-i',
				state: 'queued',
				kind: 'stack.import_archive',
				environmentId: 'env-1',
				items: [],
				targets: [{ type: 'stack', id: 'st-9' }]
			});
		}
		switch (url.pathname) {
			case '/api/v1/me/permissions':
				return json(200, { owner: true, entries: [], environments: [], catalogVersion: 1 });
			case '/api/v1/environments':
				return json(200, {
					items: [
						{
							id: 'env-1',
							name: 'nas',
							online: true,
							status: 'active',
							view: 'full',
							actions: []
						}
					]
				});
			case '/api/v1/jobs':
				return json(200, { items: [], total: 0 });
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

function dialog() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: CreateFromArchiveDialog as unknown as Component<Record<string, unknown>>,
			props: { open: true, environmentId: 'env-1' }
		}
	});
}

/** Chooses the archive file and lets the fake transfer reach `loaded` bytes. */
async function choose(user: ReturnType<typeof setup>) {
	await screen.findByRole('button', { name: 'Choose Archive' });
	const input = document.querySelector<HTMLInputElement>('input[type="file"]')!;
	const file = new File(['x'.repeat(1024)], 'silo-2026-10-09.tar.gz', {
		type: 'application/gzip'
	});
	await user.upload(input, file);
	await waitFor(() => expect(FakeXhr.last).not.toBeNull());
	return { xhr: FakeXhr.last!, file };
}

describe('CreateFromArchiveDialog', () => {
	it('uploads the chosen archive with its progress and shows what it holds', async () => {
		const user = setup();
		dialog();
		expect(
			await screen.findByRole('dialog', { name: 'Create Stack From Archive' })
		).toBeInTheDocument();
		const { xhr, file } = await choose(user);
		expect(xhr.method).toBe('POST');
		expect(xhr.url).toBe('/api/v1/stack-archives');
		expect(xhr.body).toBe(file);
		xhr.upload.onprogress?.({ loaded: 512, total: 1024 });
		expect(
			await screen.findByRole('progressbar', { name: 'Upload of silo-2026-10-09.tar.gz' })
		).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Cancel Upload' })).toBeInTheDocument();

		xhr.respond(201, archive);
		expect(await screen.findByRole('heading', { name: /Silo/ })).toBeInTheDocument();
		expect(screen.getByText('2 volumes · 4 KB')).toBeInTheDocument();
		expect(screen.getByText('2 services')).toBeInTheDocument();
		expect(screen.getByText('Not Included (1)')).toBeInTheDocument();
		// One environment: no picker.
		expect(screen.queryByRole('combobox', { name: 'Environment' })).toBeNull();
		expect(screen.getByRole('textbox', { name: /^Name/ })).toHaveValue('silo');
		expect(screen.getByRole('textbox', { name: /^Display Name/ })).toHaveValue('Silo');
		expect(screen.getByRole('checkbox', { name: 'Deploy After Creating' })).toBeChecked();

		// The check runs on its own.
		await waitFor(() =>
			expect(checks).toEqual([
				{ environmentId: 'env-1', name: 'silo', displayName: 'Silo', deploy: true }
			])
		);
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Create Stack' })).toBeEnabled()
		);
	});

	it('keeps Create Stack off while the check has problems', async () => {
		check = {
			allowed: false,
			blockers: [
				{ code: 'stack_name_conflict', message: 'nas already has a stack named silo' }
			]
		};
		const user = setup();
		dialog();
		const { xhr } = await choose(user);
		xhr.respond(201, archive);
		expect(await screen.findByText('To Fix Before Creating')).toBeInTheDocument();
		expect(screen.getByText('Nas already has a stack named silo')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Create Stack' })).toBeDisabled();
		expect(screen.getByText('Fix the problem first.')).toBeInTheDocument();
	});

	it('lists the volumes whose names follow a new name and creates the stack', async () => {
		const user = setup();
		dialog();
		const { xhr } = await choose(user);
		xhr.respond(201, archive);
		const name = await screen.findByRole('textbox', { name: /^Name/ });
		await user.clear(name);
		await user.type(name, 'cloud');
		await waitFor(() => expect(checks.at(-1)).toMatchObject({ name: 'cloud' }), {
			timeout: 2000
		});
		expect(await screen.findByText('cloud_data')).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Create Stack' })).toBeEnabled()
		);
		await user.click(screen.getByRole('button', { name: 'Create Stack' }));
		await waitFor(() =>
			expect(imports).toEqual([
				{ environmentId: 'env-1', name: 'cloud', displayName: 'Silo', deploy: true }
			])
		);
		expect(
			await screen.findByRole('progressbar', { name: 'Create cloud From Archive progress' })
		).toBeInTheDocument();
	});

	it('shows the manager’s refusal of an upload', async () => {
		const user = setup();
		dialog();
		const { xhr } = await choose(user);
		xhr.respond(422, {
			code: 'invalid_stack_archive',
			message: 'the file is not a stack archive'
		});
		expect(await screen.findByText('The archive was not uploaded.')).toBeInTheDocument();
		expect(screen.getByText('The file is not a stack archive.')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Choose Archive' })).toBeInTheDocument();
	});

	it('discards the upload when cancelled', async () => {
		const user = setup();
		dialog();
		const { xhr } = await choose(user);
		xhr.respond(201, archive);
		await screen.findByRole('button', { name: 'Choose Another' });
		await user.click(screen.getByRole('button', { name: 'Cancel' }));
		await waitFor(() => expect(deleted).toEqual(['/api/v1/stack-archives/ar-1']));
	});
});
