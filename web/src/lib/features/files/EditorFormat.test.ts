// The editor's Format split button (#15): the main part pretty-prints YAML
// and JSON; its menu offers Minify (JSON only; off for YAML with the reason
// under it) and Beautify. The result replaces the text through the editor
// (undoable, the tab turns unsaved); text that does not parse keeps the
// document and shows an error toast.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import EditorHarness from '../../../test/EditorHarness.svelte';
import type { CodeEditorHandle, CodeEditorOptions } from '$lib/lazy';
import { toast } from '$lib/ui';
import type { FileContent, FileEntry, FilesApi } from './api';
import { EditorSession } from './editor.svelte';

// A plain-text stand-in for CodeMirror: setText reports the change like
// the real editor's update listener does.
vi.mock('$lib/lazy', async (orig) => ({
	...(await orig<typeof import('$lib/lazy')>()),
	mountCodeEditor: vi.fn(async (_el: HTMLElement, doc: string, opts: CodeEditorOptions = {}) => {
		let text = doc;
		const handle: CodeEditorHandle = {
			destroy() {},
			text: () => text,
			setText(t) {
				text = t;
				opts.onChange?.(t);
			},
			focus() {},
			setLanguage: async () => {},
			setReadOnly() {},
			openSearch() {},
			setWrap() {}
		};
		return handle;
	})
}));

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

function filesWith(content: string): FilesApi {
	return {
		scope: { kind: 'stack', stackId: 's1', environmentId: 'e1' },
		read: async (path: string) => ({
			data: {
				entry: entry(path),
				binary: false,
				truncated: false,
				offset: 0,
				content
			} as FileContent,
			etag: '"e1"'
		}),
		write: async (path: string) => ({ data: entry(path), etag: '"e2"' }),
		downloadUrl: () => '/dl'
	} as unknown as FilesApi;
}

async function openFile(path: string, content: string) {
	const files = filesWith(content);
	const session = new EditorSession(files);
	session.open(path);
	render(EditorHarness, { props: { files, session } });
	await waitFor(() => expect(session.current?.status).toBe('ready'));
	await waitFor(() => expect(screen.getByRole('button', { name: 'Format' })).toBeEnabled());
	return session;
}

async function choose(user: ReturnType<typeof userEvent.setup>, item: string) {
	await user.click(screen.getByRole('button', { name: 'More Format Options' }));
	await user.click(await screen.findByRole('menuitem', { name: item }));
	await waitFor(() => expect(screen.queryByRole('menu')).toBeNull());
}

afterEach(() => toast.clear());

describe('EditorPane Format', () => {
	it('offers Minify, then Beautify, and minifies and beautifies JSON', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const session = await openFile('data.json', '{\n  "a": [1, 2],\n  "b": "x y"\n}\n');

		await user.click(screen.getByRole('button', { name: 'More Format Options' }));
		const items = await screen.findAllByRole('menuitem');
		expect(items.map((i) => i.textContent?.trim())).toEqual(['Minify', 'Beautify']);
		expect(items[0]).not.toHaveAttribute('aria-disabled', 'true');
		await user.click(items[0]);

		await waitFor(() => expect(session.current?.buffer).toBe('{"a":[1,2],"b":"x y"}\n'));
		expect(screen.getByText('Unsaved Changes')).toBeInTheDocument();

		await choose(user, 'Beautify');
		await waitFor(() =>
			expect(session.current?.buffer).toBe(
				'{\n  "a": [\n    1,\n    2\n  ],\n  "b": "x y"\n}\n'
			)
		);
	});

	it('formats with the main part of the button', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const session = await openFile('data.json', '{"a":1}');
		await user.click(screen.getByRole('button', { name: 'Format' }));
		await waitFor(() => expect(session.current?.buffer).toBe('{\n  "a": 1\n}\n'));
	});

	it('keeps Minify off for YAML, without an explanation', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const session = await openFile(
			'compose.yaml',
			'services:\n    web:\n        image: nginx\n'
		);

		await user.click(screen.getByRole('button', { name: 'More Format Options' }));
		const minify = await screen.findByRole('menuitem', { name: 'Minify' });
		expect(minify).toHaveAttribute('aria-disabled', 'true');
		expect(minify).not.toHaveAccessibleDescription();
		await user.click(minify);
		expect(session.current?.buffer).toBe('services:\n    web:\n        image: nginx\n');

		await user.click(screen.getByRole('menuitem', { name: 'Beautify' }));
		await waitFor(() =>
			expect(session.current?.buffer).toBe('services:\n  web:\n    image: nginx\n')
		);
	});

	it('keeps invalid JSON as it is and says what to fix', async () => {
		const user = userEvent.setup({ pointerEventsCheck: 0 });
		const session = await openFile('data.json', '{"a": }');

		await choose(user, 'Minify');
		await waitFor(() =>
			expect(toast.items.map((t) => t.title)).toEqual(['data.json could not be minified'])
		);
		expect(toast.items[0].tone).toBe('error');
		expect(toast.items[0].body).toMatch(
			/^This isn't valid JSON: .* Fix it and minify again\.$/
		);
		expect(session.current?.buffer).toBe('{"a": }');
		expect(screen.queryByText('Unsaved Changes')).toBeNull();
	});
});
