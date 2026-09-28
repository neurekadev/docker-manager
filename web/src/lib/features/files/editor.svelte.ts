// Open files of the file manager's editor (#15, #23): tabs with the text
// loaded from disk (`base` + its ETag) and the unsaved buffer. Rules:
//
//   - a refetched content whose ETag differs is an external change: a
//     clean buffer takes the new text; an unsaved buffer is kept and the
//     tab shows the conflict (Compare, Reload from disk, Save as…,
//     Overwrite). Text is never replaced silently.
//   - Save sends If-Match with the ETag the buffer was loaded from; a 412
//     (someone else saved) becomes the same conflict. While a conflict is
//     open, Save is refused until the user picks how to resolve it.
//   - an unsaved buffer is registered as critical work (#23) so the PWA
//     update prompt does not reload it away.
import { ApiRequestError } from '$lib/api/client';
import type { EditorLanguage } from '$lib/lazy';
import { criticalWork } from '$lib/live';
import { formatBytes } from '$lib/ui/format';
import type { FileContent, FileEntry } from './api';
import { detectLanguage } from './language';
import { basename } from './paths';

export type TabStatus = 'loading' | 'ready' | 'binary' | 'error';

export interface Conflict {
	/** external: seen on disk; save: the save was refused (412). */
	cause: 'external' | 'save';
	/** The current version on disk (null until it was read). */
	diskEtag: string | null;
	diskText: string | null;
	diskModifiedAt?: string;
}

export interface EditorTab {
	path: string;
	language: EditorLanguage;
	status: TabStatus;
	/** Text and ETag last loaded from (or saved to) disk. */
	base: string;
	baseEtag: string | null;
	buffer: string;
	entry: FileEntry | null;
	/** Only a bounded part of the file was read (read-only preview). */
	truncated: boolean;
	binary: boolean;
	error: unknown;
	conflict: Conflict | null;
	saving: boolean;
	/** Bumped when the buffer is replaced from outside the editor (reload). */
	revision: number;
	/** A clean buffer was refreshed from disk (shown briefly). */
	reloadedAt: number | null;
}

/** What the editor needs from the file API (FilesApi implements it). */
export interface EditorFiles {
	write(
		path: string,
		content: string,
		pre: { ifMatch: string } | { create: true }
	): Promise<{ data: FileEntry; etag: string | null }>;
}

export class SaveBlockedError extends Error {
	constructor(path: string) {
		super(`${basename(path)} changed on disk. Resolve the conflict before saving.`);
		this.name = 'SaveBlockedError';
	}
}

/**
 * Title of the read-only view of a file over the edit limit. The limit is
 * the root's (the listing's limits.editMaxBytes, set by the manager's
 * configuration); unknown until the listing loaded.
 */
export function truncatedTitle(editMaxBytes: number | undefined, size: number): string {
	return editMaxBytes
		? `Showing the first ${formatBytes(editMaxBytes)} of ${formatBytes(size)}`
		: `Showing the start of ${formatBytes(size)}`;
}

export function isDirty(t: EditorTab): boolean {
	return t.status === 'ready' && t.buffer !== t.base;
}

export class EditorSession {
	tabs = $state<EditorTab[]>([]);
	active = $state<string | null>(null);
	readonly current: EditorTab | null = $derived(
		this.tabs.find((t) => t.path === this.active) ?? null
	);
	readonly dirtyCount: number = $derived(this.tabs.filter(isDirty).length);

	#files: EditorFiles;
	// Release handles of critical-work registrations: bookkeeping, not rendered.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#releases = new Map<string, () => void>();
	#now: () => number;

	constructor(files: EditorFiles, now: () => number = () => Date.now()) {
		this.#files = files;
		this.#now = now;
	}

	get(path: string): EditorTab | undefined {
		return this.tabs.find((t) => t.path === path);
	}

	#patch(path: string, p: Partial<EditorTab>) {
		this.tabs = this.tabs.map((t) => (t.path === path ? { ...t, ...p } : t));
		this.#track(path);
	}

	/** Keeps the critical-work registration in step with the dirty state. */
	#track(path: string) {
		const t = this.get(path);
		const dirty = !!t && (isDirty(t) || (!!t.conflict && t.buffer !== t.base));
		const has = this.#releases.has(path);
		if (dirty && !has) this.#releases.set(path, criticalWork.register('unsaved-edit', path));
		if (!dirty && has) {
			this.#releases.get(path)?.();
			this.#releases.delete(path);
		}
	}

	/** Opens (or focuses) a tab; its content arrives through apply(). */
	open(path: string) {
		if (!this.get(path)) {
			this.tabs = [
				...this.tabs,
				{
					path,
					language: detectLanguage(path),
					status: 'loading',
					base: '',
					baseEtag: null,
					buffer: '',
					entry: null,
					truncated: false,
					binary: false,
					error: null,
					conflict: null,
					saving: false,
					revision: 0,
					reloadedAt: null
				}
			];
		}
		this.active = path;
	}

	/** Closes a tab (the caller confirmed discarding unsaved edits). */
	close(path: string) {
		const i = this.tabs.findIndex((t) => t.path === path);
		if (i < 0) return;
		this.#releases.get(path)?.();
		this.#releases.delete(path);
		this.tabs = this.tabs.filter((t) => t.path !== path);
		if (this.active === path)
			this.active = this.tabs[Math.min(i, this.tabs.length - 1)]?.path ?? null;
	}

	closeAll() {
		for (const r of this.#releases.values()) r();
		this.#releases.clear();
		this.tabs = [];
		this.active = null;
	}

	setLanguage(path: string, language: EditorLanguage) {
		this.#patch(path, { language });
	}

	/** The user edited the buffer. */
	edit(path: string, text: string) {
		const t = this.get(path);
		if (!t || t.buffer === text) return;
		this.#patch(path, { buffer: text });
	}

	/** The content query failed (denied, gone, offline). */
	fail(path: string, error: unknown) {
		const t = this.get(path);
		if (!t) return;
		// A loaded tab keeps its buffer; only a first load shows the error.
		if (t.status === 'loading' || t.status === 'error')
			this.#patch(path, { status: 'error', error });
	}

	/** A (re)read of the file from disk arrived. */
	apply(path: string, content: FileContent, etag: string | null) {
		const t = this.get(path);
		if (!t) return;
		const text = content.binary ? '' : (content.content ?? '');
		const disk = { entry: content.entry, truncated: content.truncated, binary: content.binary };
		if (t.status === 'loading' || t.status === 'error') {
			this.#patch(path, {
				...disk,
				status: content.binary ? 'binary' : 'ready',
				base: text,
				baseEtag: etag,
				buffer: text,
				error: null,
				revision: t.revision + 1
			});
			return;
		}
		// Our own save is in flight: its answer sets the new base (a refetch
		// triggered by the save's own invalidation must not look external;
		// a real concurrent change makes the save fail with 412 instead).
		if (t.saving) return;
		if (etag === t.baseEtag) {
			// Same revision (a refetch): a pending save conflict learns the disk text.
			if (t.conflict && t.conflict.diskEtag === etag && t.conflict.diskText === null)
				this.#patch(path, { conflict: { ...t.conflict, diskText: text } });
			return;
		}
		if (t.conflict?.diskEtag === etag && t.conflict.diskText !== null) return;
		if (content.binary || t.status === 'binary') {
			this.#patch(path, {
				...disk,
				status: content.binary ? 'binary' : 'ready',
				base: text,
				baseEtag: etag,
				buffer: text,
				revision: t.revision + 1
			});
			return;
		}
		if (!isDirty(t) && !t.conflict) {
			this.#patch(path, {
				...disk,
				base: text,
				baseEtag: etag,
				buffer: text,
				revision: t.revision + 1,
				reloadedAt: this.#now()
			});
			return;
		}
		this.#patch(path, {
			conflict: {
				cause: t.conflict?.cause ?? 'external',
				diskEtag: etag,
				diskText: text,
				diskModifiedAt: content.entry.modifiedAt
			}
		});
	}

	/** Saves the buffer against the version it was loaded from. */
	async save(path: string): Promise<FileEntry> {
		const t = this.get(path);
		if (!t) throw new Error('no such tab');
		if (t.conflict) throw new SaveBlockedError(path);
		if (!t.baseEtag)
			throw new Error('The file has no revision to save against. Reload it first.');
		const text = t.buffer;
		this.#patch(path, { saving: true });
		try {
			const { data, etag } = await this.#files.write(path, text, { ifMatch: t.baseEtag });
			this.#patch(path, {
				saving: false,
				base: text,
				baseEtag: etag,
				entry: data,
				conflict: null
			});
			return data;
		} catch (e) {
			if (e instanceof ApiRequestError && e.status === 412) {
				this.#patch(path, {
					saving: false,
					conflict: { cause: 'save', diskEtag: null, diskText: null }
				});
				throw new SaveBlockedError(path);
			}
			this.#patch(path, { saving: false });
			throw e;
		}
	}

	/** Explicit overwrite: replaces the disk version the user just saw. */
	async overwrite(path: string): Promise<FileEntry> {
		const t = this.get(path);
		if (!t?.conflict?.diskEtag) throw new Error('The version on disk is not known yet.');
		const text = t.buffer;
		this.#patch(path, { saving: true });
		try {
			const { data, etag } = await this.#files.write(path, text, {
				ifMatch: t.conflict.diskEtag
			});
			this.#patch(path, {
				saving: false,
				base: text,
				baseEtag: etag,
				entry: data,
				conflict: null
			});
			return data;
		} catch (e) {
			this.#patch(path, { saving: false });
			if (e instanceof ApiRequestError && e.status === 412) {
				// Changed again meanwhile: stay in conflict, wait for the new version.
				this.#patch(path, { conflict: { cause: 'save', diskEtag: null, diskText: null } });
				throw new SaveBlockedError(path);
			}
			throw e;
		}
	}

	/** Drops the unsaved buffer and takes the version on disk. */
	reloadFromDisk(path: string) {
		const t = this.get(path);
		if (!t?.conflict || t.conflict.diskText === null) return;
		this.#patch(path, {
			base: t.conflict.diskText,
			baseEtag: t.conflict.diskEtag,
			buffer: t.conflict.diskText,
			conflict: null,
			revision: t.revision + 1
		});
	}

	/** Discards unsaved edits of a tab without a conflict. */
	revert(path: string) {
		const t = this.get(path);
		if (!t) return;
		this.#patch(path, { buffer: t.base, revision: t.revision + 1 });
	}

	/**
	 * Saves the buffer under a new name (create only) and opens it; the
	 * original tab then takes the version on disk.
	 */
	async saveAs(path: string, newPath: string): Promise<FileEntry> {
		const t = this.get(path);
		if (!t) throw new Error('no such tab');
		const text = t.buffer;
		const { data, etag } = await this.#files.write(newPath, text, { create: true });
		this.tabs = [
			...this.tabs,
			{
				...t,
				path: newPath,
				language: detectLanguage(newPath),
				status: 'ready',
				base: text,
				baseEtag: etag,
				buffer: text,
				entry: data,
				conflict: null,
				saving: false,
				revision: 0,
				reloadedAt: null
			}
		];
		if (t.conflict?.diskText != null) this.reloadFromDisk(path);
		else this.revert(path);
		this.active = newPath;
		return data;
	}
}
