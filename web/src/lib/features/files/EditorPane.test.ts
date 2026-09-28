// The editor's external-change conflict (#15, #22 brief): the banner reads
// "<file> changed on disk. Your edits are kept." with Compare, Reload from
// disk, Save as… and Overwrite (confirmed); Save stays off until resolved.
import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import EditorHarness from '../../../test/EditorHarness.svelte';
import type { FileContent, FileEntry, FilesApi } from './api';
import { EditorSession } from './editor.svelte';

function entry(path: string): FileEntry {
	return {
		name: path,
		path,
		type: 'file',
		size: 5,
		mode: '0644',
		uid: 0,
		gid: 0,
		modifiedAt: '2026-09-25T10:00:00Z'
	};
}

function fakeFiles() {
	const disk = { text: 'a: 1\n', etag: '"e1"' };
	const writes: { text: string; pre: unknown }[] = [];
	const api = {
		scope: { kind: 'stack', stackId: 's1', environmentId: 'e1' },
		read: async (path: string) => ({
			data: {
				entry: entry(path),
				binary: false,
				truncated: false,
				offset: 0,
				content: disk.text
			} as FileContent,
			etag: disk.etag
		}),
		write: async (path: string, text: string, pre: unknown) => {
			writes.push({ text, pre });
			disk.text = text;
			disk.etag = `"e${writes.length + 1}"`;
			return { data: entry(path), etag: disk.etag };
		},
		downloadUrl: () => '/dl'
	};
	return { api: api as unknown as FilesApi, disk, writes };
}

describe('EditorPane', () => {
	it('keeps unsaved edits on an external change and resolves by explicit overwrite', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const { api, writes } = fakeFiles();
		const session = new EditorSession(api);
		session.open('compose.yaml');
		render(EditorHarness, { props: { files: api, session } });
		await waitFor(() => expect(session.current?.status).toBe('ready'));
		expect(screen.getByRole('tab', { name: /compose\.yaml/ })).toHaveAttribute(
			'aria-selected',
			'true'
		);

		session.edit('compose.yaml', 'a: mine\n');
		await waitFor(() => expect(screen.getByText('Unsaved changes')).toBeInTheDocument());
		// Somebody changed it on the host: the next read carries a new ETag.
		session.apply(
			'compose.yaml',
			{
				entry: entry('compose.yaml'),
				binary: false,
				truncated: false,
				offset: 0,
				content: 'a: theirs\n'
			},
			'"host"'
		);
		const banner = await screen.findByRole('alert');
		expect(banner).toHaveTextContent('compose.yaml changed on disk. Your edits are kept.');
		for (const name of ['Compare', 'Reload from disk', 'Save as…', 'Overwrite'])
			expect(screen.getByRole('button', { name })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
		expect(session.current?.buffer).toBe('a: mine\n');

		await user.click(screen.getByRole('button', { name: 'Overwrite' }));
		const confirm = await screen.findByRole('alertdialog', {
			name: /Overwrite compose\.yaml on disk\?/
		});
		expect(confirm).toHaveTextContent('Records a new revision of Silo. Nothing is deployed.');
		await user.click(screen.getAllByRole('button', { name: 'Overwrite' }).at(-1)!);
		await waitFor(() => expect(writes).toHaveLength(1));
		expect(writes[0]).toEqual({ text: 'a: mine\n', pre: { ifMatch: '"host"' } });
		await waitFor(() => expect(screen.queryByText(/changed on disk/)).toBeNull());
	});

	it('says calmly that saving a Compose file does not deploy', async () => {
		const { api } = fakeFiles();
		const session = new EditorSession(api);
		session.open('compose.yaml');
		render(EditorHarness, { props: { files: api, session } });
		await waitFor(() => expect(session.current?.status).toBe('ready'));
		expect(screen.getByText("Saving doesn't deploy Silo.")).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: /Deploy/ })).toBeNull();
		expect(screen.getByRole('button', { name: 'Wrap long lines' })).toHaveAttribute(
			'aria-pressed',
			'false'
		);
	});

	it('offers Deploy while the saved definition is not deployed', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const { api } = fakeFiles();
		const session = new EditorSession(api);
		session.open('compose.yaml');
		let deploys = 0;
		render(EditorHarness, {
			props: {
				files: api,
				session,
				stack: {
					name: 'Silo',
					configFiles: [],
					undeployed: true,
					deploy: async () => void deploys++
				}
			}
		});
		await waitFor(() => expect(session.current?.status).toBe('ready'));
		expect(screen.getByText("Saved changes aren't deployed yet.")).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Deploy Silo' }));
		await waitFor(() => expect(deploys).toBe(1));
		// Unsaved edits first: no Deploy until they are saved.
		session.edit('compose.yaml', 'a: 2\n');
		await waitFor(() =>
			expect(screen.queryByRole('button', { name: 'Deploy Silo' })).toBeNull()
		);
	});

	it('shows no Deploy without the permission to deploy', async () => {
		const { api } = fakeFiles();
		const session = new EditorSession(api);
		session.open('compose.yaml');
		render(EditorHarness, {
			props: {
				files: api,
				session,
				stack: { name: 'Silo', configFiles: [], undeployed: true }
			}
		});
		await waitFor(() => expect(session.current?.status).toBe('ready'));
		expect(screen.getByText("Saved changes aren't deployed yet.")).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Deploy Silo' })).toBeNull();
	});

	it('hides saving for read-only access and offers Preview for Markdown', async () => {
		const { api } = fakeFiles();
		const session = new EditorSession(api);
		session.open('README.md');
		render(EditorHarness, { props: { files: api, session, canWrite: false } });
		await waitFor(() => expect(session.current?.status).toBe('ready'));
		expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Format' })).toBeNull();
		expect(screen.getByRole('button', { name: 'Preview' })).toBeInTheDocument();
		expect(screen.getByText('Read only')).toBeInTheDocument();
	});
});
