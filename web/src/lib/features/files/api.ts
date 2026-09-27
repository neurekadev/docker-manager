// The scoped file API (#15, docs/internal/api/files.md) for both roots through the
// generated client: a stack's project directory (/stacks/{id}/files) and a
// volume (/environments/{env}/volumes/{volume}/files) share every route
// suffix, body and answer, so the file manager is written once against
// FileScope. Query keys follow the live conventions (liveKeys.files), so a
// files.changed event refreshes listings and open files (#23).
import { infiniteQueryOptions, queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys, type FileScopeRef } from '$lib/live/keys';

export type FileEntry = Schema<'FileEntry'>;
export type FileListing = Schema<'FileListing'>;
export type FileContent = Schema<'FileContent'>;
export type FilePreview = Schema<'FilePreview'>;
export type FileUploadResult = Schema<'FileUploadResult'>;
export type FileJob = Schema<'Job'>;

/** The root of a file manager. */
export type FileScope =
	| { kind: 'stack'; stackId: string; environmentId: string }
	| { kind: 'volume'; environmentId: string; volume: string };

export function liveScopeOf(s: FileScope): FileScopeRef {
	return s.kind === 'stack'
		? { kind: 'stack', id: s.stackId }
		: { kind: 'volume', id: `${s.environmentId}/${s.volume}` };
}

/** A stable identity of the scope (clipboard, drag data, tabs). */
export function scopeKey(s: FileScope): string {
	return s.kind === 'stack' ? `stack:${s.stackId}` : `volume:${s.environmentId}/${s.volume}`;
}

/** The capability prefix of the root: stack.files. or volume.files. */
export function filesCapability(s: FileScope, verb: FileVerb): string {
	return `${s.kind}.files.${verb}`;
}

export type FileVerb =
	| 'read'
	| 'download'
	| 'write'
	| 'copy'
	| 'move'
	| 'delete'
	| 'archive'
	| 'extract'
	| 'chmod'
	| 'chown';

/** The URL prefix of the root's file routes (downloads, uploads). */
export function filesBase(s: FileScope): string {
	const e = encodeURIComponent;
	return s.kind === 'stack'
		? `/api/v1/stacks/${e(s.stackId)}/files`
		: `/api/v1/environments/${e(s.environmentId)}/volumes/${e(s.volume)}/files`;
}

export type ListSort =
	'name' | '-name' | 'size' | '-size' | 'modified' | '-modified' | 'type' | '-type';

export interface ListFilters {
	sort: ListSort;
	q: string;
	hidden: boolean;
}

export const PAGE_SIZE = 200;

/** Idempotency key of one job request (never reused for another request). */
function idempotencyKey(): string {
	return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
}

async function withEtag<T>(
	call: Promise<{ data?: T; error?: unknown; response: Response }>
): Promise<{ data: T; etag: string | null }> {
	const data = await unwrap(call);
	const { response } = await call;
	return { data, etag: response.headers.get('ETag') };
}

export class FilesApi {
	readonly scope: FileScope;
	readonly client: ApiClient;

	constructor(scope: FileScope, client: ApiClient = api) {
		this.scope = scope;
		this.client = client;
	}

	private get stackPath() {
		const s = this.scope as Extract<FileScope, { kind: 'stack' }>;
		return { stackId: s.stackId };
	}

	private get volumePath() {
		const s = this.scope as Extract<FileScope, { kind: 'volume' }>;
		return { environmentId: s.environmentId, volumeId: s.volume };
	}

	list(dir: string, f: ListFilters, cursor?: string, signal?: AbortSignal): Promise<FileListing> {
		const query = {
			path: dir,
			sort: f.sort,
			q: f.q || undefined,
			hidden: f.hidden || undefined,
			limit: PAGE_SIZE,
			cursor
		};
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.GET('/api/v1/stacks/{stackId}/files', {
						params: { path: this.stackPath, query },
						signal
					})
				: this.client.GET('/api/v1/environments/{environmentId}/volumes/{volumeId}/files', {
						params: { path: this.volumePath, query },
						signal
					})
		);
	}

	/** Reads up to 512 KiB; `etag` is the content revision to save against. */
	read(path: string, signal?: AbortSignal): Promise<{ data: FileContent; etag: string | null }> {
		const query = { path };
		return withEtag(
			this.scope.kind === 'stack'
				? this.client.GET('/api/v1/stacks/{stackId}/files/content', {
						params: { path: this.stackPath, query },
						signal
					})
				: this.client.GET(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/content',
						{ params: { path: this.volumePath, query }, signal }
					)
		);
	}

	/**
	 * Saves text. `ifMatch` replaces exactly that revision (412 with the
	 * current ETag otherwise); `create` writes only when the name is free.
	 */
	write(
		path: string,
		content: string,
		pre: { ifMatch: string } | { create: true }
	): Promise<{ data: FileEntry; etag: string | null }> {
		const header =
			'ifMatch' in pre ? { 'If-Match': pre.ifMatch } : { 'If-None-Match': '*' as const };
		const query = { path };
		return withEtag(
			this.scope.kind === 'stack'
				? this.client.PUT('/api/v1/stacks/{stackId}/files/content', {
						params: { path: this.stackPath, query, header },
						body: { content }
					})
				: this.client.PUT(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/content',
						{ params: { path: this.volumePath, query, header }, body: { content } }
					)
		);
	}

	createEntry(path: string, type: 'file' | 'dir', content?: string): Promise<FileEntry> {
		const body = { path, type, content };
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.POST('/api/v1/stacks/{stackId}/files/entries', {
						params: { path: this.stackPath },
						body
					})
				: this.client.POST(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/entries',
						{ params: { path: this.volumePath }, body }
					)
		);
	}

	preview(body: {
		operation: 'copy' | 'move' | 'delete' | 'upload' | 'extract' | 'archive' | 'metadata';
		paths?: string[];
		destination?: string;
		names?: string[];
		recursive?: boolean;
	}): Promise<FilePreview> {
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.POST('/api/v1/stacks/{stackId}/files/conflict-previews', {
						params: { path: this.stackPath },
						body
					})
				: this.client.POST(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/conflict-previews',
						{ params: { path: this.volumePath }, body }
					)
		);
	}

	copy(
		paths: string[],
		destination: string,
		conflict: 'fail' | 'overwrite' | 'skip' | 'keep_both'
	): Promise<FileJob> {
		const body = { paths, destination, conflict };
		const header = { 'Idempotency-Key': idempotencyKey() };
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.POST('/api/v1/stacks/{stackId}/files/copies', {
						params: { path: this.stackPath, header },
						body
					})
				: this.client.POST(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/copies',
						{ params: { path: this.volumePath, header }, body }
					)
		);
	}

	/** Moves `paths` into `destination`; with `name` renames a single source. */
	move(
		paths: string[],
		destination: string,
		conflict: 'fail' | 'overwrite' | 'skip' | 'keep_both',
		name?: string
	): Promise<FileJob> {
		const body = { paths, destination, conflict, name };
		const header = { 'Idempotency-Key': idempotencyKey() };
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.POST('/api/v1/stacks/{stackId}/files/moves', {
						params: { path: this.stackPath, header },
						body
					})
				: this.client.POST(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/moves',
						{ params: { path: this.volumePath, header }, body }
					)
		);
	}

	remove(paths: string[]): Promise<FileJob> {
		const body = { paths };
		const header = { 'Idempotency-Key': idempotencyKey() };
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.POST('/api/v1/stacks/{stackId}/files/deletions', {
						params: { path: this.stackPath, header },
						body
					})
				: this.client.POST(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/deletions',
						{ params: { path: this.volumePath, header }, body }
					)
		);
	}

	archive(
		paths: string[],
		destination: string,
		format: 'zip' | 'tar.gz',
		conflict: 'fail' | 'overwrite' | 'keep_both'
	): Promise<FileJob> {
		const body = { paths, destination, format, conflict };
		const header = { 'Idempotency-Key': idempotencyKey() };
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.POST('/api/v1/stacks/{stackId}/files/archives', {
						params: { path: this.stackPath, header },
						body
					})
				: this.client.POST(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/archives',
						{ params: { path: this.volumePath, header }, body }
					)
		);
	}

	extract(
		path: string,
		destination: string,
		conflict: 'fail' | 'overwrite' | 'skip' | 'keep_both'
	): Promise<FileJob> {
		const body = { path, destination, conflict };
		const header = { 'Idempotency-Key': idempotencyKey() };
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.POST('/api/v1/stacks/{stackId}/files/extractions', {
						params: { path: this.stackPath, header },
						body
					})
				: this.client.POST(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/extractions',
						{ params: { path: this.volumePath, header }, body }
					)
		);
	}

	metadata(body: {
		paths: string[];
		recursive: boolean;
		chmod?: { mode: string; dirMode?: string };
		chown?: { uid: number; gid: number };
	}): Promise<FileJob> {
		const header = { 'Idempotency-Key': idempotencyKey() };
		return unwrap(
			this.scope.kind === 'stack'
				? this.client.PATCH('/api/v1/stacks/{stackId}/files/metadata', {
						params: { path: this.stackPath, header },
						body
					})
				: this.client.PATCH(
						'/api/v1/environments/{environmentId}/volumes/{volumeId}/files/metadata',
						{ params: { path: this.volumePath, header }, body }
					)
		);
	}

	/** URL of a download: one file raw, several paths or a format as an archive. */
	downloadUrl(paths: string[], format?: 'zip' | 'tar.gz'): string {
		const q = new URLSearchParams();
		for (const p of paths) q.append('path', p);
		if (format) q.set('format', format);
		return `${filesBase(this.scope)}/downloads?${q}`;
	}

	/** URL of an upload of one file into `dir`. */
	uploadUrl(dir: string, name: string, conflict?: 'overwrite' | 'skip' | 'keep_both'): string {
		const q = new URLSearchParams({ path: dir, name });
		if (conflict) q.set('conflict', conflict);
		return `${filesBase(this.scope)}/uploads?${q}`;
	}
}

/** Query key of a directory listing with its filters. */
export function listKey(scope: FileScope, dir: string, f: ListFilters) {
	return [...liveKeys.files(liveScopeOf(scope), 'list', dir), f.sort, f.q, f.hidden];
}

/** Paged listing of one directory (large directories load page by page). */
export function listingQuery(files: FilesApi, dir: string, f: ListFilters) {
	return infiniteQueryOptions({
		queryKey: listKey(files.scope, dir, f),
		queryFn: ({ pageParam, signal }) => files.list(dir, f, pageParam || undefined, signal),
		initialPageParam: '',
		getNextPageParam: (last: FileListing) => last.nextCursor || undefined,
		staleTime: 5_000
	});
}

/** An open file's content and ETag (refetched on files.changed, #23). */
export function contentQuery(files: FilesApi, path: string) {
	return queryOptions({
		queryKey: liveKeys.files(liveScopeOf(files.scope), 'content', path),
		queryFn: ({ signal }) => files.read(path, signal),
		staleTime: 2_000,
		retry: false
	});
}
