// Stack archive upload (#313): the raw file in one POST with progress,
// the archive the manager answers with, errors in words, cancellation, and
// critical work plus the unload warning only while it uploads.
import { describe, expect, it } from 'vitest';
import { criticalWork } from '$lib/live';
import {
	ArchiveUploadError,
	STACK_ARCHIVES_URL,
	archiveUploadError,
	uploadStackArchive,
	type XhrLike
} from './archive-upload';

class FakeXhr implements XhrLike {
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
		this.responseText = typeof body === 'string' ? body : JSON.stringify(body);
		this.onload?.();
	}
}

class FakeWindow {
	listeners = 0;
	addEventListener() {
		this.listeners++;
	}
	removeEventListener() {
		this.listeners--;
	}
}

function start(file = new Blob(['archive'])) {
	const x = new FakeXhr();
	const w = new FakeWindow();
	const progress: [number, number][] = [];
	const up = uploadStackArchive(file, {
		xhr: () => x,
		unload: w,
		onprogress: (l, t) => progress.push([l, t])
	});
	return { x, w, up, progress, file };
}

describe('uploadStackArchive', () => {
	it('sends the raw file, reports progress and resolves with the archive', async () => {
		const before = criticalWork.items.length;
		const { x, w, up, progress, file } = start();
		expect(x.method).toBe('POST');
		expect(x.url).toBe(STACK_ARCHIVES_URL);
		expect(x.headers['Content-Type']).toBe('application/octet-stream');
		expect(x.body).toBe(file);
		expect(criticalWork.items.length).toBe(before + 1);
		expect(criticalWork.items.at(-1)?.kind).toBe('upload');
		expect(w.listeners).toBe(1);

		x.upload.onprogress?.({ loaded: 3, total: 7 });
		expect(progress).toEqual([[3, 7]]);
		x.respond(201, { id: 'ar-1', name: 'silo' });
		await expect(up.done).resolves.toMatchObject({ id: 'ar-1', name: 'silo' });
		expect(criticalWork.items.length).toBe(before);
		expect(w.listeners).toBe(0);
	});

	it('rejects with the manager’s message as a sentence', async () => {
		const { x, w, up } = start();
		x.respond(422, {
			code: 'invalid_stack_archive',
			message: 'the archive has no manifest'
		});
		await expect(up.done).rejects.toThrow('The archive has no manifest.');
		expect(w.listeners).toBe(0);
	});

	it('cancels on abort and releases the critical work', async () => {
		const before = criticalWork.items.length;
		const { x, up } = start();
		up.abort();
		expect(x.aborted).toBe(true);
		const err = await up.done.catch((e: unknown) => e);
		expect(err).toBeInstanceOf(ArchiveUploadError);
		expect((err as ArchiveUploadError).cancelled).toBe(true);
		expect(criticalWork.items.length).toBe(before);
		// A second abort after the end does nothing.
		x.aborted = false;
		up.abort();
		expect(x.aborted).toBe(false);
	});

	it('says when the connection was lost', async () => {
		const { x, up } = start();
		x.onerror?.();
		await expect(up.done).rejects.toThrow(
			'The connection to the manager was lost. Upload the archive again.'
		);
	});
});

describe('archiveUploadError', () => {
	it('explains a proxy’s refusal and other answers without a message', () => {
		expect(archiveUploadError(413, '<html>Request Entity Too Large</html>')).toMatch(
			/larger than the reverse proxy/
		);
		expect(
			archiveUploadError(
				413,
				JSON.stringify({
					code: 'payload_too_large',
					message: 'stack archives are limited to 2 GB'
				})
			)
		).toBe('Stack archives are limited to 2 GB.');
		expect(archiveUploadError(502, '')).toBe('The upload failed with HTTP 502.');
	});
});
