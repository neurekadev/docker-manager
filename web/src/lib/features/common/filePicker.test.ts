// The shared file picker: browses one place at a time, opens folders,
// chooses one regular file or several ticked files and folders, and lists
// a folder only when it opens.
import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { QueryClient } from '@tanstack/svelte-query';
import type { ComponentProps } from 'svelte';
import HardDrive from '@lucide/svelte/icons/hard-drive';
import Layers from '@lucide/svelte/icons/layers';
import QueryHarness from '../../../test/QueryHarness.svelte';
import FilePicker from './FilePicker.svelte';
import type { PickerEntry, PickerSource } from './filePicker';

const entry = (path: string, type: PickerEntry['type']): PickerEntry => ({
	name: path.slice(path.lastIndexOf('/') + 1),
	path,
	type,
	size: 10,
	mtime: '2026-09-26T10:00:00Z'
});

const listings: Record<string, PickerEntry[]> = {
	'/stacks/web': [
		entry('/stacks/web/compose.yaml', 'file'),
		entry('/stacks/web/conf', 'dir'),
		entry('/stacks/web/current', 'symlink')
	],
	'/stacks/web/conf': [
		entry('/stacks/web/conf/app.ini', 'file'),
		entry('/stacks/web/conf/site.ini', 'file')
	],
	'/vol/db': [entry('/vol/db/pg', 'dir'), entry('/vol/db/big', 'dir')],
	'/vol/db/big': [entry('/vol/db/big/1.dat', 'file'), entry('/vol/db/big/2.dat', 'file')]
};
// Listed only in part (more entries than the source returns).
const partial = new Set(['/vol/db/big']);
// Listings held back until the test releases them.
const gates = new Map<string, Promise<void>>();

const places = [
	{ path: '/stacks/web', label: 'web Project Files', icon: Layers },
	{ path: '/vol/db', label: 'Volume db', icon: HardDrive }
];

const place = (name: string | RegExp) =>
	within(screen.getByRole('navigation', { name: 'Places' })).getByRole('button', { name });

function setup(props: Partial<ComponentProps<typeof FilePicker>> = {}) {
	const listed: string[] = [];
	const source: PickerSource = {
		query: (dir) => ({
			queryKey: ['picker-test', dir],
			queryFn: async () => {
				listed.push(dir);
				await gates.get(dir);
				return { entries: listings[dir] ?? [], truncated: partial.has(dir) };
			}
		})
	};
	const onpick = vi.fn();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(QueryHarness<ComponentProps<typeof FilePicker>>, {
		props: {
			client,
			component: FilePicker,
			props: {
				open: true,
				title: 'Choose',
				places,
				source,
				confirmLabel: 'Choose File',
				onpick,
				unchoosableReason: () => 'Links cannot be chosen.',
				...props
			}
		}
	});
	return { client, listed, onpick, user: userEvent.setup({ pointerEventsCheck: 0 }) };
}

describe('FilePicker: one file', () => {
	it('opens folders, selects a file and chooses it', async () => {
		const { listed, onpick, user } = setup();
		await screen.findByRole('option', { name: /compose\.yaml/ });
		expect(listed).toEqual(['/stacks/web']);
		const choose = screen.getByRole('button', { name: 'Choose File' });
		expect(choose).toBeDisabled();
		expect(screen.getByRole('button', { name: 'Up One Folder' })).toBeDisabled();
		expect(screen.getByRole('option', { name: /current/ })).toHaveAttribute(
			'aria-disabled',
			'true'
		);

		await user.click(screen.getByRole('option', { name: /conf/ }));
		const ini = await screen.findByRole('option', { name: /app\.ini/ });
		await user.click(ini);
		expect(ini).toHaveAttribute('aria-selected', 'true');
		expect(screen.getByText('/stacks/web/conf/app.ini')).toBeInTheDocument();

		await user.click(choose);
		expect(onpick).toHaveBeenCalledWith(['/stacks/web/conf/app.ini']);
	});

	it('moves with the keyboard and goes up with Backspace', async () => {
		const { onpick, user } = setup();
		const list = await screen.findByRole('listbox');
		await screen.findByRole('option', { name: /compose\.yaml/ });
		list.focus();
		await user.keyboard('{Home}{Enter}');
		await screen.findByRole('option', { name: /site\.ini/ });
		await user.keyboard('{Backspace}');
		await screen.findByRole('option', { name: /compose\.yaml/ });
		await user.keyboard('{ArrowDown}{Enter}');
		expect(onpick).toHaveBeenCalledWith(['/stacks/web/compose.yaml']);
	});

	it("opens on the chosen file's folder, switches places and filters", async () => {
		const { user } = setup({ value: ['/stacks/web/conf/site.ini'] });
		expect(await screen.findByRole('option', { name: /site\.ini/ })).toHaveAttribute(
			'aria-selected',
			'true'
		);
		await user.click(place('Volume db'));
		expect(await screen.findByRole('option', { name: /pg/ })).toBeInTheDocument();
		await user.type(screen.getByRole('searchbox', { name: 'Filter This Folder' }), 'zz');
		expect(screen.getByText('Nothing here matches the filter.')).toBeInTheDocument();
	});

	it('never preselects a chosen path that is not a regular file', async () => {
		setup({ value: [' /stacks/web/conf '] });
		expect(await screen.findByRole('option', { name: /conf/ })).toHaveAttribute(
			'aria-selected',
			'false'
		);
		expect(screen.getByRole('button', { name: 'Choose File' })).toBeDisabled();
		expect(screen.getByText('No file selected')).toBeInTheDocument();
	});
});

describe('FilePicker: several items', () => {
	it('ticks files and folders, splits a ticked folder and hands over the paths', async () => {
		const { listed, onpick, user } = setup({ multiple: true, confirmLabel: 'Review Restore' });
		expect(await screen.findByRole('checkbox', { name: 'compose.yaml' })).not.toBeChecked();
		expect(listed).toEqual(['/stacks/web']);
		const review = screen.getByRole('button', { name: 'Review Restore' });
		expect(review).toBeDisabled();

		await user.click(screen.getByRole('checkbox', { name: 'conf' }));
		expect(screen.getByRole('checkbox', { name: 'All of web Project Files' })).toHaveProperty(
			'indeterminate',
			true
		);
		await user.click(screen.getByRole('button', { name: 'conf' }));
		expect(await screen.findByRole('checkbox', { name: 'app.ini' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'site.ini' })).toBeChecked();
		await user.click(screen.getByRole('checkbox', { name: 'app.ini' }));
		expect(await screen.findByText('1 item selected')).toBeInTheDocument();
		await waitFor(() =>
			expect(screen.getByRole('checkbox', { name: 'All of conf' })).toHaveProperty(
				'indeterminate',
				true
			)
		);

		await user.click(place(/web Project Files/));
		await user.click(await screen.findByRole('checkbox', { name: 'compose.yaml' }));
		expect(screen.getByText('2 items selected')).toBeInTheDocument();
		await user.click(review);
		expect(onpick).toHaveBeenCalledWith([
			'/stacks/web/compose.yaml',
			'/stacks/web/conf/site.ini'
		]);
	});

	it('never splits a ticked folder that is listed only in part', async () => {
		// The preset folder's parent opens first.
		const { user } = setup({ multiple: true, value: ['/vol/db/big'] });
		await user.click(await screen.findByRole('button', { name: 'big' }));
		const one = await screen.findByRole('checkbox', { name: '1.dat' });
		expect(one).toBeChecked();
		await user.click(one);
		expect(
			await screen.findByText(/listed only in part, so it is chosen whole/)
		).toBeInTheDocument();
		expect(screen.getByRole('checkbox', { name: '1.dat' })).toBeChecked();
		expect(screen.getByText('1 item selected')).toBeInTheDocument();
	});

	it('lists a dropped folder before splitting it and holds the confirm meanwhile', async () => {
		const { client, onpick, user } = setup({
			multiple: true,
			value: ['/stacks/web'],
			confirmLabel: 'Review Restore'
		});
		await user.click(await screen.findByRole('button', { name: 'conf' }));
		const app = await screen.findByRole('checkbox', { name: 'app.ini' });
		// The cache dropped the place's listing; listing it again waits.
		client.removeQueries({ queryKey: ['picker-test', '/stacks/web'] });
		let release = () => {};
		gates.set('/stacks/web', new Promise<void>((r) => (release = r)));
		await user.click(app);
		const review = screen.getByRole('button', { name: 'Review Restore' });
		expect(review).toBeDisabled();
		release();
		gates.clear();
		expect(await screen.findByText('3 items selected')).toBeInTheDocument();
		await waitFor(() => expect(review).toBeEnabled());
		await user.click(review);
		expect(onpick).toHaveBeenCalledWith([
			'/stacks/web/compose.yaml',
			'/stacks/web/conf/site.ini',
			'/stacks/web/current'
		]);
	});

	it('starts with the given paths ticked and clears them', async () => {
		const { user } = setup({ multiple: true, value: ['/stacks/web/conf'] });
		expect(await screen.findByRole('checkbox', { name: 'conf' })).toBeChecked();
		await user.click(screen.getByRole('button', { name: 'Clear Selection' }));
		expect(screen.getByRole('checkbox', { name: 'conf' })).not.toBeChecked();
		expect(screen.getByText('Nothing selected')).toBeInTheDocument();
	});
});
