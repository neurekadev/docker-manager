// Upload queue of the file manager (#15, docs/api/streams.md "Upload"):
// one POST …/files/uploads per file with the raw bytes, progress from
// XMLHttpRequest (fetch has no upload progress), cancellable at any time
// (the agent writes a temporary file and renames it only when complete, so
// a cancelled upload leaves nothing behind). A file whose name is free is
// sent with If-None-Match: * (never overwrites); a resolved conflict sends
// its policy. While anything uploads, criticalWork keeps the PWA from
// reloading (#23).
import { criticalWork } from '$lib/live';

export type UploadState = 'queued' | 'uploading' | 'done' | 'failed' | 'cancelled' | 'skipped';

export interface UploadItem {
	id: number;
	/** Target directory (root-relative) and file name. */
	dir: string;
	name: string;
	size: number;
	loaded: number;
	state: UploadState;
	error?: string;
	/** The name the file got (keep both may rename it). */
	storedAs?: string;
}

export interface UploadRequest {
	file: Blob;
	dir: string;
	name: string;
	/** Undefined: create only. */
	conflict?: 'overwrite' | 'keep_both';
	/** Skip without sending (the user chose "Skip" for its conflict). */
	skip?: boolean;
}

/** The parts of XMLHttpRequest the queue uses (fakeable in tests). */
export interface XhrLike {
	open(method: string, url: string): void;
	setRequestHeader(name: string, value: string): void;
	send(body: Blob): void;
	abort(): void;
	readonly status: number;
	readonly responseText: string;
	upload: { onprogress: ((e: { loaded: number; total: number }) => void) | null };
	onload: (() => void) | null;
	onerror: (() => void) | null;
	onabort: (() => void) | null;
}

export interface UploadQueueOptions {
	/** URL of an upload of `name` into `dir` with an optional conflict policy. */
	url: (dir: string, name: string, conflict?: 'overwrite' | 'keep_both') => string;
	xhr?: () => XhrLike;
	concurrency?: number;
	/** Called after each finished file (refresh listings, toasts). */
	onsettled?: (item: UploadItem) => void;
	/** Called once the queue became idle with the batch's items. */
	ondrained?: (items: UploadItem[]) => void;
}

function uploadError(status: number, body: string): string {
	try {
		const j = JSON.parse(body) as { code?: string; message?: string };
		if (j.code === 'precondition_failed')
			return 'A file with this name appeared meanwhile. Upload it again to choose what to do.';
		if (j.code === 'payload_too_large')
			return 'The file is larger than the upload limit of this Docker Manager.';
		if (j.message) return j.message.endsWith('.') ? j.message : `${j.message}.`;
	} catch {
		// not JSON
	}
	return `The upload failed with HTTP ${status}.`;
}

export class UploadQueue {
	items = $state<UploadItem[]>([]);
	readonly active: boolean = $derived(
		this.items.some((i) => i.state === 'queued' || i.state === 'uploading')
	);
	readonly totalBytes: number = $derived(this.items.reduce((n, i) => n + i.size, 0));
	readonly loadedBytes: number = $derived(
		this.items.reduce((n, i) => n + (i.state === 'done' ? i.size : i.loaded), 0)
	);

	#opts: UploadQueueOptions;
	#next = 1;
	#pending: { item: UploadItem; req: UploadRequest }[] = [];
	// Requests in flight (for cancel): bookkeeping, not rendered.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#running = new Map<number, XhrLike>();
	#release: (() => void) | null = null;

	constructor(opts: UploadQueueOptions) {
		this.#opts = opts;
	}

	/** Adds files; transfers start at once (bounded concurrency). */
	enqueue(reqs: UploadRequest[]) {
		if (reqs.length === 0) return;
		if (!this.active) this.items = this.items.filter((i) => i.state === 'failed');
		const added: UploadItem[] = [];
		for (const req of reqs) {
			const item: UploadItem = {
				id: this.#next++,
				dir: req.dir,
				name: req.name,
				size: req.file.size,
				loaded: 0,
				state: req.skip ? 'skipped' : 'queued'
			};
			added.push(item);
			if (!req.skip) this.#pending.push({ item, req });
		}
		this.items = [...this.items, ...added];
		if (this.#pending.length && !this.#release)
			this.#release = criticalWork.register('upload', `${this.#pending.length} uploads`);
		this.#pump();
		this.#maybeDrained();
	}

	cancel(id: number) {
		const x = this.#running.get(id);
		if (x) {
			x.abort();
			return;
		}
		const i = this.#pending.findIndex((p) => p.item.id === id);
		if (i >= 0) {
			this.#pending.splice(i, 1);
			this.#set(id, { state: 'cancelled' });
			this.#maybeDrained();
		}
	}

	cancelAll() {
		for (const p of [...this.#pending]) this.cancel(p.item.id);
		for (const id of [...this.#running.keys()]) this.cancel(id);
	}

	/** Drops finished entries from the list. */
	dismiss() {
		this.items = this.items.filter((i) => i.state === 'queued' || i.state === 'uploading');
	}

	#set(id: number, patch: Partial<UploadItem>) {
		this.items = this.items.map((i) => (i.id === id ? { ...i, ...patch } : i));
	}

	#pump() {
		const limit = this.#opts.concurrency ?? 2;
		while (this.#running.size < limit && this.#pending.length) {
			const next = this.#pending.shift();
			if (next) this.#start(next.item, next.req);
		}
	}

	#start(item: UploadItem, req: UploadRequest) {
		const x = this.#opts.xhr?.() ?? (new XMLHttpRequest() as unknown as XhrLike);
		this.#running.set(item.id, x);
		this.#set(item.id, { state: 'uploading' });
		const finish = (patch: Partial<UploadItem>) => {
			this.#running.delete(item.id);
			this.#set(item.id, patch);
			const done = this.items.find((i) => i.id === item.id);
			if (done) this.#opts.onsettled?.(done);
			this.#pump();
			this.#maybeDrained();
		};
		x.open('POST', this.#opts.url(req.dir, req.name, req.conflict));
		x.setRequestHeader('Content-Type', 'application/octet-stream');
		if (!req.conflict) x.setRequestHeader('If-None-Match', '*');
		x.upload.onprogress = (e) => this.#set(item.id, { loaded: e.loaded });
		x.onload = () => {
			if (x.status === 201) {
				let storedAs = req.name;
				try {
					storedAs =
						(JSON.parse(x.responseText) as { entry?: { name?: string } }).entry?.name ??
						req.name;
				} catch {
					// keep the requested name
				}
				finish({ state: 'done', loaded: item.size, storedAs });
			} else finish({ state: 'failed', error: uploadError(x.status, x.responseText) });
		};
		x.onerror = () =>
			finish({
				state: 'failed',
				error: 'The connection to the manager was lost. Upload the file again.'
			});
		x.onabort = () => finish({ state: 'cancelled' });
		x.send(req.file);
	}

	#maybeDrained() {
		if (this.active) return;
		this.#release?.();
		this.#release = null;
		this.#opts.ondrained?.(this.items);
	}
}
