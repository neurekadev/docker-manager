// Backup file picker (#10): folders list only when opened, ticks are
// tri-state, unticking a file inside a ticked folder keeps its siblings,
// and the review receives exactly the chosen paths.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import QueryHarness from '../../../test/QueryHarness.svelte';
import FilePickerDialog from './FilePickerDialog.svelte';
import type { Backup } from './model';

function json(body: unknown) {
	return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
}

const node = (path: string, type: 'dir' | 'file') => ({
	name: path.slice(path.lastIndexOf('/') + 1),
	path,
	type,
	size: 10,
	mode: 0o644,
	uid: 0,
	gid: 0,
	mtime: '2026-09-26T10:00:00Z'
});

// Like restic, a listing starts with the directory itself.
const listings: Record<string, ReturnType<typeof node>[]> = {
	'/vol/up/_data': [
		node('/vol/up/_data', 'dir'),
		node('/vol/up/_data/a.jpg', 'file'),
		node('/vol/up/_data/thumbs', 'dir')
	],
	'/vol/up/_data/thumbs': [
		node('/vol/up/_data/thumbs', 'dir'),
		node('/vol/up/_data/thumbs/1.png', 'file'),
		node('/vol/up/_data/thumbs/2.png', 'file')
	]
};

const backup = {
	id: 'bk1',
	kind: 'volume',
	volume: 'up',
	snapshotTime: '2026-09-26T10:00:00Z',
	repositoryId: 'r1',
	state: 'complete',
	view: 'full',
	actions: ['backup.restore', 'backup.contents.read'],
	volumePaths: { up: '/vol/up/_data' }
} as unknown as Backup;

afterEach(() => vi.unstubAllGlobals());

describe('FilePickerDialog (#10)', () => {
	it('lists folders when opened and hands over the chosen paths', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const listed: string[] = [];
		vi.stubGlobal(
			'fetch',
			vi.fn(async (input: Request) => {
				const path = new URL(input.url).searchParams.get('path') ?? '';
				listed.push(path);
				return json({ path, entries: listings[path] ?? [], truncated: false });
			})
		);
		const onnext = vi.fn();
		render(QueryHarness<ComponentProps<typeof FilePickerDialog>>, {
			props: {
				client: new QueryClient({ defaultOptions: { queries: { retry: false } } }),
				component: FilePickerDialog,
				props: { open: true, backup, onnext }
			}
		});
		expect(await screen.findByRole('checkbox', { name: 'a.jpg' })).not.toBeChecked();
		expect(listed).toEqual(['/vol/up/_data']);
		const review = screen.getByRole('button', { name: 'Review restore' });
		expect(review).toBeDisabled();

		await user.click(screen.getByRole('checkbox', { name: 'thumbs' }));
		expect(screen.getByRole('checkbox', { name: 'All of Volume up' })).toHaveProperty(
			'indeterminate',
			true
		);
		await user.click(screen.getByRole('button', { name: 'Open thumbs' }));
		expect(await screen.findByRole('checkbox', { name: '1.png' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: '2.png' })).toBeChecked();
		await user.click(screen.getByRole('checkbox', { name: '1.png' }));
		expect(screen.getByRole('checkbox', { name: 'thumbs' })).toHaveProperty(
			'indeterminate',
			true
		);
		expect(screen.getByText('1 item selected')).toBeInTheDocument();

		await user.click(screen.getByRole('checkbox', { name: 'a.jpg' }));
		await user.click(review);
		expect(onnext).toHaveBeenCalledWith(['/vol/up/_data/a.jpg', '/vol/up/_data/thumbs/2.png']);
	});
});
