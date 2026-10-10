// Uploading a stack archive (#313, POST /stack-archives): the raw file in
// one request, with progress from XMLHttpRequest (fetch has no upload
// progress) and cancellable at any time (the manager keeps nothing of an
// upload that ended early). The manager checks the archive while it
// arrives and answers with what it holds. While it uploads, criticalWork
// keeps the PWA from reloading (#23) and the browser asks before the page
// unloads (it would cancel the transfer), like the file manager's uploads.
import type { Schema } from '$lib/api/client';
import {
	warnBeforeUnload,
	type UnloadTarget,
	type XhrLike
} from '$lib/features/files/uploads.svelte';
import { criticalWork } from '$lib/live';
import { asSentence } from './importing';

export type { XhrLike };

export const STACK_ARCHIVES_URL = '/api/v1/stack-archives';

/** The upload ended without an archive: the reason in words. */
export class ArchiveUploadError extends Error {
	/** The user cancelled it. */
	readonly cancelled: boolean;

	constructor(message: string, cancelled = false) {
		super(message);
		this.name = 'ArchiveUploadError';
		this.cancelled = cancelled;
	}
}

export interface ArchiveUploadOptions {
	/** Bytes sent so far of `total`. */
	onprogress?: (loaded: number, total: number) => void;
	xhr?: () => XhrLike;
	/**
	 * Where the upload listens for `beforeunload` (default: the window;
	 * null: nowhere). A test seam.
	 */
	unload?: UnloadTarget | null;
}

export interface ArchiveUpload {
	/** The uploaded archive, or an ArchiveUploadError. */
	done: Promise<Schema<'StackArchive'>>;
	/** Cancels the transfer (`done` rejects with `cancelled`). */
	abort(): void;
}

/** A failed upload in words: the manager's message, or what went wrong on the way. */
export function archiveUploadError(status: number, body: string): string {
	try {
		const j = JSON.parse(body) as { code?: string; message?: string };
		if (j.message) return asSentence(j.message);
	} catch {
		// not JSON: a reverse proxy answered
	}
	if (status === 413)
		return 'The archive is larger than the reverse proxy in front of Docker Manager accepts. An administrator can raise its body size limit.';
	return `The upload failed with HTTP ${status}.`;
}

/** Uploads `file` as a stack archive; the transfer starts at once. */
export function uploadStackArchive(file: Blob, o: ArchiveUploadOptions = {}): ArchiveUpload {
	const x = o.xhr?.() ?? (new XMLHttpRequest() as unknown as XhrLike);
	const unload =
		o.unload !== undefined ? o.unload : typeof window === 'undefined' ? null : window;
	const name = (file as Partial<File>).name ?? 'stack archive';
	const releaseWork = criticalWork.register('upload', `Upload of ${name}`);
	const warn = (e: BeforeUnloadEvent) => warnBeforeUnload(e);
	unload?.addEventListener('beforeunload', warn);
	let settled = false;
	const release = () => {
		settled = true;
		releaseWork();
		unload?.removeEventListener('beforeunload', warn);
	};
	const done = new Promise<Schema<'StackArchive'>>((resolve, reject) => {
		const fail = (message: string, cancelled = false) => {
			release();
			reject(new ArchiveUploadError(message, cancelled));
		};
		x.open('POST', STACK_ARCHIVES_URL);
		x.setRequestHeader('Content-Type', 'application/octet-stream');
		x.setRequestHeader('Accept', 'application/json');
		x.upload.onprogress = (e) => o.onprogress?.(e.loaded, e.total || file.size);
		x.onload = () => {
			if (x.status !== 201) return fail(archiveUploadError(x.status, x.responseText));
			try {
				const archive = JSON.parse(x.responseText) as Schema<'StackArchive'>;
				release();
				resolve(archive);
			} catch {
				fail(
					'The manager answered with something other than the archive. Upload it again.'
				);
			}
		};
		x.onerror = () => fail('The connection to the manager was lost. Upload the archive again.');
		x.onabort = () => fail('The upload was cancelled.', true);
		x.send(file);
	});
	return {
		done,
		abort() {
			if (!settled) x.abort();
		}
	};
}
