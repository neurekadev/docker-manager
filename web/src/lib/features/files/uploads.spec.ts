// Upload queue (#15): create-only by default, resolved conflicts send
// their policy, progress, cancellation, bounded concurrency, errors in plain
// language and critical work while uploading.
import { describe, expect, it } from 'vitest';
import { criticalWork } from '$lib/live';
import { UploadQueue, type XhrLike } from './uploads.svelte';

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
});
