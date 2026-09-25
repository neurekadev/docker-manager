// The editor's buffers and conflicts (#15, #23): an external change keeps
// the unsaved buffer and blocks saving until resolved; saves carry If-Match;
// unsaved edits are critical work.
import { describe, expect, it } from 'vitest';
import { ApiRequestError } from '$lib/api/client';
import { criticalWork } from '$lib/live';
import type { FileContent, FileEntry } from './api';
import { EditorSession, isDirty, SaveBlockedError, type EditorFiles } from './editor.svelte';

function entry(path: string, modifiedAt = '2026-09-25T10:00:00Z'): FileEntry {
	return { name: path, path, type: 'file', size: 1, mode: '0644', uid: 0, gid: 0, modifiedAt };
}

function content(path: string, text: string, extra: Partial<FileContent> = {}): FileContent {
	return {
		entry: entry(path),
		binary: false,
		truncated: false,
		offset: 0,
		content: text,
		...extra
	};
}

class FakeFiles implements EditorFiles {
	disk = new Map<string, { text: string; etag: string }>();
	calls: { path: string; text: string; pre: unknown }[] = [];
	#n = 1;

	put(path: string, text: string) {
		const etag = `"e${this.#n++}"`;
		this.disk.set(path, { text, etag });
		return etag;
	}

	async write(path: string, text: string, pre: { ifMatch: string } | { create: true }) {
		this.calls.push({ path, text, pre });
		const cur = this.disk.get(path);
		if ('create' in pre && cur) throw new ApiRequestError('exists', 412);
		if ('ifMatch' in pre && cur?.etag !== pre.ifMatch) throw new ApiRequestError('stale', 412);
		const etag = this.put(path, text);
		return { data: entry(path), etag };
	}
}

describe('EditorSession', () => {
	it('opens, edits, saves with If-Match and tracks unsaved work', async () => {
		const files = new FakeFiles();
		const e1 = files.put('compose.yaml', 'a: 1\n');
		const s = new EditorSession(files);
		s.open('compose.yaml');
		expect(s.current?.status).toBe('loading');
		expect(s.current?.language).toBe('yaml');
		s.apply('compose.yaml', content('compose.yaml', 'a: 1\n'), e1);
		expect(s.current?.status).toBe('ready');
		const before = criticalWork.items.length;
		s.edit('compose.yaml', 'a: 2\n');
		expect(isDirty(s.current!)).toBe(true);
		expect(s.dirtyCount).toBe(1);
		expect(criticalWork.items.length).toBe(before + 1);
		expect(criticalWork.items.at(-1)).toMatchObject({
			kind: 'unsaved-edit',
			label: 'compose.yaml'
		});
		await s.save('compose.yaml');
		expect(files.calls[0]).toEqual({
			path: 'compose.yaml',
			text: 'a: 2\n',
			pre: { ifMatch: e1 }
		});
		expect(isDirty(s.current!)).toBe(false);
		expect(s.current?.baseEtag).toBe(files.disk.get('compose.yaml')?.etag);
		expect(criticalWork.items.length).toBe(before);
	});

	it('refreshes a clean buffer from disk; keeps an unsaved one and blocks saving', async () => {
		const files = new FakeFiles();
		const e1 = files.put('.env', 'A=1\n');
		const s = new EditorSession(files, () => 42);
		s.open('.env');
		s.apply('.env', content('.env', 'A=1\n'), e1);
		// Host-side edit while the buffer is clean: take it.
		const e2 = files.put('.env', 'A=2\n');
		s.apply('.env', content('.env', 'A=2\n'), e2);
		expect(s.current?.buffer).toBe('A=2\n');
		expect(s.current?.reloadedAt).toBe(42);
		expect(s.current?.conflict).toBeNull();
		// Unsaved edit, then another external change: the buffer is kept.
		s.edit('.env', 'A=mine\n');
		const e3 = files.put('.env', 'A=theirs\n');
		s.apply(
			'.env',
			content('.env', 'A=theirs\n', { entry: entry('.env', '2026-09-25T11:00:00Z') }),
			e3
		);
		expect(s.current?.buffer).toBe('A=mine\n');
		expect(s.current?.conflict).toEqual({
			cause: 'external',
			diskEtag: e3,
			diskText: 'A=theirs\n',
			diskModifiedAt: '2026-09-25T11:00:00Z'
		});
		// Save fails safe until the conflict is resolved: nothing is written.
		await expect(s.save('.env')).rejects.toBeInstanceOf(SaveBlockedError);
		expect(files.calls).toHaveLength(0);
		// Explicit overwrite replaces exactly the version the user saw.
		await s.overwrite('.env');
		expect(files.calls[0].pre).toEqual({ ifMatch: e3 });
		expect(files.disk.get('.env')?.text).toBe('A=mine\n');
		expect(s.current?.conflict).toBeNull();
	});

	it('turns a stale save (412) into the conflict and learns the disk version', async () => {
		const files = new FakeFiles();
		const e1 = files.put('app.json', '{}');
		const s = new EditorSession(files);
		s.open('app.json');
		s.apply('app.json', content('app.json', '{}'), e1);
		s.edit('app.json', '{"a":1}');
		const e2 = files.put('app.json', '{"b":2}'); // someone else saved; no event yet
		await expect(s.save('app.json')).rejects.toBeInstanceOf(SaveBlockedError);
		expect(s.current?.conflict).toEqual({ cause: 'save', diskEtag: null, diskText: null });
		expect(s.current?.buffer).toBe('{"a":1}');
		s.apply('app.json', content('app.json', '{"b":2}'), e2);
		expect(s.current?.conflict).toMatchObject({
			cause: 'save',
			diskEtag: e2,
			diskText: '{"b":2}'
		});
		// Reload from disk drops the edit.
		s.reloadFromDisk('app.json');
		expect(s.current?.buffer).toBe('{"b":2}');
		expect(s.current?.baseEtag).toBe(e2);
		expect(s.current?.conflict).toBeNull();
	});

	it('ignores refetches during its own save and saves under a new name', async () => {
		const files = new FakeFiles();
		const e1 = files.put('a.txt', 'one');
		const s = new EditorSession(files);
		s.open('a.txt');
		s.apply('a.txt', content('a.txt', 'one'), e1);
		s.edit('a.txt', 'two');
		const saving = s.save('a.txt');
		// The save's own invalidation refetches before its answer arrives.
		s.apply('a.txt', content('a.txt', 'two'), '"e99"');
		await saving;
		expect(s.current?.conflict).toBeNull();

		s.edit('a.txt', 'three');
		const e3 = files.put('a.txt', 'external');
		s.apply('a.txt', content('a.txt', 'external'), e3);
		await s.saveAs('a.txt', 'a (edited).txt');
		expect(files.calls.at(-1)).toMatchObject({
			path: 'a (edited).txt',
			text: 'three',
			pre: { create: true }
		});
		expect(s.active).toBe('a (edited).txt');
		expect(s.get('a.txt')?.buffer).toBe('external');
		expect(s.get('a.txt')?.conflict).toBeNull();
		s.closeAll();
		expect(s.tabs).toHaveLength(0);
	});

	it('shows binary files as previews and keeps loaded tabs on errors', () => {
		const s = new EditorSession(new FakeFiles());
		s.open('logo.png');
		s.apply('logo.png', content('logo.png', '', { binary: true, content: undefined }), '"b1"');
		expect(s.current?.status).toBe('binary');
		s.open('gone.txt');
		s.fail('gone.txt', new ApiRequestError('not found', 404));
		expect(s.current?.status).toBe('error');
		s.close('gone.txt');
		expect(s.active).toBe('logo.png');
	});
});
