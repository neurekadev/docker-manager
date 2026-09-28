// Upload queue (#15): create-only by default, resolved conflicts send
// their policy, progress, cancellation, bounded concurrency, errors in plain
// language and critical work while uploading; the browser asks before the
// page unloads while anything uploads, and each root's queue outlives the
// file manager (in-app navigation keeps the transfers).
import { describe, expect, it, vi } from 'vitest';
import { criticalWork } from '$lib/live';
import {
	releaseUploadQueue,
	tooLargeMessage,
	uploadQueue,
	UploadQueue,
	warnBeforeUnload,
	type UnloadTarget,
	type XhrLike
} from './uploads.svelte';

class FakeXhr implements XhrLike {
	static all: FakeXhr[] = [];
	method = '';
	url = '';
	headers: Record<string, string> = {};
	body: Blob | null = null;
	status = 0;
	responseText = '';
	upload: XhrLike['upload'] = { onprogress: null };
	onload: (() => void) | null = null;
	onerror: (() => void) | null = null;
	onabort: (() => void) | null = null;
	aborted = false;

	constructor() {
		FakeXhr.all.push(this);
	}
	open(m: string, u: string) {
		this.method = m;
		this.url = u;
	}
	setRequestHeader(k: string, v: string) {
		this.headers[k] = v;
	}
	send(b: Blob) {
		this.body = b;
	}
	abort() {
		this.aborted = true;
		this.onabort?.();
	}
	respond(status: number, body: unknown) {
		this.status = status;
		this.responseText = JSON.stringify(body);
		this.onload?.();
	}
}

function queue(drained: unknown[][] = []) {
	FakeXhr.all = [];
	return new UploadQueue({
		url: (d, n, c) => `/up?path=${d}&name=${n}${c ? `&conflict=${c}` : ''}`,
		xhr: () => new FakeXhr(),
		concurrency: 2,
		ondrained: (items) => drained.push(items)
	});
}

const file = (text: string) => new Blob([text]);

describe('UploadQueue', () => {
	it('uploads create-only, sends chosen policies and skips without a request', () => {
		const drained: unknown[][] = [];
		const q = queue(drained);
		const before = criticalWork.items.length;
		q.enqueue([
			{ file: file('aaa'), dir: 'data', name: 'a.txt' },
			{ file: file('bb'), dir: 'data', name: 'b.txt', conflict: 'overwrite' },
			{ file: file('c'), dir: 'data', name: 'c.txt', skip: true },
			{ file: file('dddd'), dir: 'data', name: 'd.txt', conflict: 'keep_both' }
		]);
		expect(criticalWork.items.length).toBe(before + 1);
		expect(criticalWork.items.at(-1)?.kind).toBe('upload');
		// Bounded concurrency: two in flight.
		expect(FakeXhr.all).toHaveLength(2);
		const [a, b] = FakeXhr.all;
		expect(a.url).toBe('/up?path=data&name=a.txt');
		expect(a.headers['If-None-Match']).toBe('*');
		expect(a.headers['Content-Type']).toBe('application/octet-stream');
		expect(b.url).toBe('/up?path=data&name=b.txt&conflict=overwrite');
		expect(b.headers['If-None-Match']).toBeUndefined();
		a.upload.onprogress?.({ loaded: 2, total: 3 });
		expect(q.items[0].loaded).toBe(2);
		expect(q.loadedBytes).toBe(2);
		a.respond(201, { entry: { name: 'a.txt' }, skipped: false });
		expect(FakeXhr.all).toHaveLength(3);
		expect(FakeXhr.all[2].url).toBe('/up?path=data&name=d.txt&conflict=keep_both');
		b.respond(201, { entry: { name: 'b.txt' } });
		FakeXhr.all[2].respond(201, { entry: { name: 'd (1).txt' } });
		expect(q.items.map((i) => i.state)).toEqual(['done', 'done', 'skipped', 'done']);
		expect(q.items[3].storedAs).toBe('d (1).txt');
		expect(q.active).toBe(false);
		expect(criticalWork.items.length).toBe(before);
		expect(drained).toHaveLength(1);
	});

	it('cancels queued and running uploads and explains failures', () => {
		const q = queue();
		q.enqueue([
			{ file: file('1'), dir: '.', name: 'one' },
			{ file: file('2'), dir: '.', name: 'two' },
			{ file: file('3'), dir: '.', name: 'three' }
		]);
		const [one, two] = FakeXhr.all;
		q.cancel(q.items[2].id); // still queued: never sent
		expect(q.items[2].state).toBe('cancelled');
		one.respond(412, { code: 'precondition_failed', message: 'exists' });
		expect(q.items[0]).toMatchObject({ state: 'failed' });
		expect(q.items[0].error).toMatch(/appeared meanwhile/);
		q.cancel(q.items[1].id);
		expect(two.aborted).toBe(true);
		expect(q.items[1].state).toBe('cancelled');
		expect(FakeXhr.all).toHaveLength(2);
		q.enqueue([{ file: file('4'), dir: '.', name: 'four' }]);
		// A new batch keeps failures visible and drops finished entries.
		expect(q.items.map((i) => i.name)).toEqual(['one', 'four']);
		FakeXhr.all[2].respond(413, { code: 'payload_too_large', message: 'too big' });
		expect(q.items[1].error).toMatch(/upload limit/);
		q.dismiss();
		expect(q.items).toHaveLength(0);
	});

	it('refuses files over the served upload limit without sending them', () => {
		FakeXhr.all = [];
		const drained: unknown[][] = [];
		let max: number | undefined = undefined;
		const q = new UploadQueue({
			url: (d, n) => `/up?path=${d}&name=${n}`,
			xhr: () => new FakeXhr(),
			maxBytes: () => max,
			ondrained: (items) => drained.push(items)
		});
		// Unknown limit (listing not loaded): the server decides.
		q.enqueue([{ file: file('12345'), dir: '.', name: 'early' }]);
		expect(FakeXhr.all).toHaveLength(1);
		FakeXhr.all[0].respond(201, { entry: { name: 'early' } });
		max = 3;
		q.enqueue([
			{ file: file('1234'), dir: '.', name: 'big' },
			{ file: file('123'), dir: '.', name: 'fits' }
		]);
		expect(FakeXhr.all).toHaveLength(2);
		expect(FakeXhr.all[1].url).toBe('/up?path=.&name=fits');
		const big = q.items.find((i) => i.name === 'big');
		expect(big).toMatchObject({ state: 'failed', error: tooLargeMessage(3) });
		FakeXhr.all[1].respond(201, { entry: { name: 'fits' } });
		expect(drained.at(-1)).toHaveLength(2);
		// Only oversized files: they fail at once and the batch is over.
		q.enqueue([{ file: file('12345'), dir: '.', name: 'huge' }]);
		expect(FakeXhr.all).toHaveLength(2);
		expect(q.active).toBe(false);
	});

	it('names the limit in plain words', () => {
		expect(tooLargeMessage(2 * 1024 ** 3)).toBe(
			'The file is larger than the upload limit of 2 GB. An administrator can raise it.'
		);
	});

	it('asks before the page unloads only while it uploads', () => {
		FakeXhr.all = [];
		const listeners = new Set<(e: BeforeUnloadEvent) => void>();
		const unload: UnloadTarget = {
			addEventListener: (_t, l) => listeners.add(l),
			removeEventListener: (_t, l) => listeners.delete(l)
		};
		const q = new UploadQueue({
			url: (d, n) => `/up?path=${d}&name=${n}`,
			xhr: () => new FakeXhr(),
			unload
		});
		const other = new UploadQueue({
			url: (d, n) => `/other?path=${d}&name=${n}`,
			xhr: () => new FakeXhr(),
			unload
		});
		expect(listeners.size).toBe(0);
		q.enqueue([{ file: file('1'), dir: '.', name: 'one' }]);
		other.enqueue([{ file: file('2'), dir: '.', name: 'two' }]);
		expect(listeners.size).toBe(2);
		// One root finishing keeps the other root's warning.
		FakeXhr.all[0].respond(201, { entry: { name: 'one' } });
		expect(listeners.size).toBe(1);
		const event = { preventDefault: vi.fn(), returnValue: 'unset' };
		for (const l of listeners) l(event as unknown as BeforeUnloadEvent);
		expect(event.preventDefault).toHaveBeenCalled();
		expect(event.returnValue).toBe('');
		other.cancelAll();
		expect(listeners.size).toBe(0);
	});

	it('warns through preventDefault and an empty return value', () => {
		const event = { preventDefault: vi.fn(), returnValue: 'x' };
		warnBeforeUnload(event as unknown as BeforeUnloadEvent);
		expect(event.preventDefault).toHaveBeenCalledOnce();
		expect(event.returnValue).toBe('');
	});

	it("keeps a root's queue while it uploads and hands it the new file manager's callbacks", () => {
		FakeXhr.all = [];
		const drained: string[] = [];
		const opts = (who: string) => ({
			url: (d: string, n: string) => `/up?path=${d}&name=${n}`,
			xhr: () => new FakeXhr(),
			unload: null,
			ondrained: () => drained.push(who)
		});
		const q = uploadQueue('stack:keep', opts('first'));
		q.enqueue([{ file: file('1'), dir: '.', name: 'one' }]);
		// The file manager unmounts (another page) while the upload runs.
		releaseUploadQueue('stack:keep');
		const again = uploadQueue('stack:keep', opts('second'));
		expect(again).toBe(q);
		expect(again.items.map((i) => i.state)).toEqual(['uploading']);
		FakeXhr.all[0].respond(201, { entry: { name: 'one' } });
		expect(drained).toEqual(['second']);
		// Idle: the next file manager starts with a fresh queue.
		releaseUploadQueue('stack:keep');
		expect(uploadQueue('stack:keep', opts('third'))).not.toBe(q);
		// Roots never share a queue.
		expect(uploadQueue('volume:e1/pgdata', opts('x'))).not.toBe(
			uploadQueue('stack:keep', opts('y'))
		);
	});
});
