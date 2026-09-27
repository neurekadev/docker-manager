import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { Component } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import type { Stack } from './queries';
import type { RenamePreview } from './rename';
import RenameStackDialog from './RenameStackDialog.svelte';
import { JobTray } from './tray.svelte';

vi.mock('$app/navigation', () => ({ goto: vi.fn() }));

const setup = () => userEvent.setup({ pointerEventsCheck: 0 });

interface Seen {
	method: string;
	path: string;
	headers: Headers;
	body: { name?: string } | undefined;
}
let seen: Seen[] = [];
// The preview the scripted manager answers for a requested name.
let planFor: (name: string) => Partial<RenamePreview> = () => ({});

beforeEach(() => {
	seen = [];
	planFor = () => ({});
	vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
		const req = input instanceof Request ? input : new Request(String(input), init);
		const url = new URL(req.url);
		const text = await req.text();
		const body = text ? JSON.parse(text) : undefined;
		seen.push({ method: req.method, path: url.pathname, headers: req.headers, body });
		const json = (status: number, b: unknown) =>
			new Response(JSON.stringify(b), {
				status,
				headers: { 'Content-Type': 'application/json' }
			});
		if (url.pathname.endsWith('/rename-previews')) {
			const to = body?.name ?? '';
			return json(200, {
				from: 'silo',
				to,
				fromDir: 'silo',
				toDir: to,
				running: ['web'],
				volumes: [
					{ key: 'data', name: 'silo_data', newName: `${to}_data`, action: 'move' }
				],
				containers: [],
				blockers: [],
				warnings: [],
				...planFor(to)
			});
		}
		if (url.pathname.endsWith('/renames'))
			return json(202, {
				id: 'job-r',
				state: 'queued',
				kind: 'stack.rename',
				items: [],
				targets: []
			});
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

const stack = {
	id: 'st-1',
	environmentId: 'env-1',
	name: 'silo',
	status: 'deployed',
	view: 'full',
	actions: ['stack.read', 'stack.rename'],
	revision: 3
} as Stack;

function dialog(tray = new JobTray()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness, {
		props: {
			client,
			component: RenameStackDialog as unknown as Component<Record<string, unknown>>,
			props: { open: true, stack, tray }
		}
	});
	return tray;
}

async function previewName(user: ReturnType<typeof setup>, name: string) {
	const d = await screen.findByRole('alertdialog', { name: 'Rename silo?' });
	await user.type(within(d).getByLabelText('New project name', { exact: false }), name);
	await user.click(within(d).getByRole('button', { name: 'Preview changes' }));
	return d;
}

describe('RenameStackDialog', () => {
	it('previews the rename, then renames with If-Match after typing the current name', async () => {
		const user = setup();
		const tray = dialog();
		const d = await previewName(user, 'store');
		expect(await within(d).findByText('silo_data → store_data')).toBeInTheDocument();
		expect(within(d).getByText('Its data moves to the new name.')).toBeInTheDocument();
		expect(within(d).getByText('silo → store')).toBeInTheDocument();
		const confirm = within(d).getByRole('button', { name: 'Rename stack' });
		expect(confirm).toBeDisabled();
		await user.type(within(d).getByLabelText('Type silo to confirm'), 'silo');
		expect(confirm).toBeEnabled();
		await user.click(confirm);
		await waitFor(() => expect(tray.jobs).toHaveLength(1));
		const post = seen.find((s) => s.path === '/api/v1/stacks/st-1/renames');
		expect(post?.body).toEqual({ name: 'store' });
		expect(post?.headers.get('If-Match')).toBe('"3"');
		expect(post?.headers.get('Idempotency-Key')).toBeTruthy();
		expect(tray.jobs[0]).toMatchObject({
			id: 'job-r',
			title: 'Rename silo to store',
			success: 'Renamed silo to store',
			failure: 'silo was not renamed'
		});
	});

	it('shows blockers and keeps the confirmation disabled', async () => {
		const user = setup();
		planFor = () => ({
			blockers: [
				{
					code: 'target_volume_exists',
					message: 'A volume named store_data exists already.'
				}
			]
		});
		dialog();
		const d = await previewName(user, 'store');
		expect(
			await within(d).findByText('A volume named store_data exists already.')
		).toBeInTheDocument();
		await user.type(within(d).getByLabelText('Type silo to confirm'), 'silo');
		expect(within(d).getByRole('button', { name: 'Rename stack' })).toBeDisabled();
		expect(seen.some((s) => s.path.endsWith('/renames'))).toBe(false);
	});

	it('locks the name to the one the Compose file sets', async () => {
		const user = setup();
		planFor = (to) => ({
			declaredName: 'cloud',
			blockers:
				to === 'cloud'
					? []
					: [{ code: 'declared_name', message: 'Its Compose file sets name: cloud.' }]
		});
		dialog();
		const d = await previewName(user, 'other');
		await waitFor(() =>
			expect(
				seen.filter((s) => s.path.endsWith('/rename-previews')).map((s) => s.body?.name)
			).toEqual(['other', 'cloud'])
		);
		const input = within(d).getByLabelText('New project name', {
			exact: false
		}) as HTMLInputElement;
		await waitFor(() => expect(input.value).toBe('cloud'));
		expect(input).toHaveAttribute('readonly');
		expect(
			within(d).getByText(/Its Compose file sets name: cloud\. The stack can only take/)
		).toBeInTheDocument();
		await user.type(within(d).getByLabelText('Type silo to confirm'), 'silo');
		expect(within(d).getByRole('button', { name: 'Rename stack' })).toBeEnabled();
	});

	it('refuses an invalid name without asking the server', async () => {
		const user = setup();
		dialog();
		const d = await previewName(user, 'Not Valid');
		expect(await within(d).findByText(/Use lower-case letters/)).toBeInTheDocument();
		expect(seen).toEqual([]);
	});
});
