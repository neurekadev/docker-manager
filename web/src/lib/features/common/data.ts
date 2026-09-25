// Shared data helpers of the admin and automation screens (#22 track B5):
// cursor paging, idempotency keys and the resource lists several features
// pick targets from. Query keys follow the live conventions (liveKeys) so
// #23 invalidations refresh them; the second key element after 'list' is a
// feature marker, so the cached shape never collides with another view's
// list of the same topic.
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';

export type Stack = Schema<'Stack'>;
export type Container = Schema<'Container'>;
export type Volume = Schema<'Volume'>;

interface Page<T> {
	items: T[];
	nextCursor?: string;
}

/** Follows nextCursor until the last page (bounded by maxPages). */
export async function fetchAllPages<T>(
	page: (cursor: string | undefined) => Promise<Page<T>>,
	maxPages = 50
): Promise<T[]> {
	const out: T[] = [];
	let cursor: string | undefined;
	for (let i = 0; i < maxPages; i++) {
		const p = await page(cursor);
		out.push(...p.items);
		cursor = p.nextCursor;
		if (!cursor) break;
	}
	return out;
}

/** A fresh Idempotency-Key for a dangerous request (#4): one per user action. */
export function newIdempotencyKey(): string {
	if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID();
	return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

/**
 * The If-Match value of a revisioned resource (#4 conventions: the ETag is
 * the quoted revision). A stale revision answers 412 precondition_failed.
 */
export function ifMatch(revision: number | undefined): string {
	return `"${revision ?? 0}"`;
}

/** Every stack the caller may see (optionally of one environment). */
export function stacksQuery(environmentId: string | null = null, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('stacks', 'b5-all', environmentId ?? ''),
		queryFn: ({ signal }): Promise<Stack[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/stacks', {
						params: {
							query: { cursor, limit: 200, environmentId: environmentId ?? undefined }
						},
						signal
					})
				)
			),
		staleTime: 30_000
	});
}

/** Every container of an environment (503 environment_offline while it is offline). */
export function containersQuery(environmentId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('containers', 'b5-all', environmentId),
		queryFn: ({ signal }): Promise<Container[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/environments/{environmentId}/containers', {
						params: { path: { environmentId }, query: { cursor, limit: 200 } },
						signal
					})
				)
			),
		enabled: !!environmentId,
		staleTime: 30_000,
		retry: false
	});
}

/** Every volume of an environment. */
export function volumesQuery(environmentId: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: liveKeys.list('volumes', 'b5-all', environmentId),
		queryFn: ({ signal }): Promise<Volume[]> =>
			fetchAllPages((cursor) =>
				unwrap(
					client.GET('/api/v1/environments/{environmentId}/volumes', {
						params: { path: { environmentId }, query: { cursor, limit: 200 } },
						signal
					})
				)
			),
		enabled: !!environmentId,
		staleTime: 30_000,
		retry: false
	});
}

/** A lookup of environment names by ID ("Unknown environment" for a missing one). */
export function environmentName(
	environments: readonly { id: string; name: string }[] | undefined,
	id: string | undefined
): string {
	if (!id) return '—';
	return environments?.find((e) => e.id === id)?.name ?? 'Unknown environment';
}

/** Short form of a digest for tables: the first 12 hex digits after "sha256:". */
export function shortDigest(d: string | undefined | null): string {
	if (!d) return '—';
	const hex = d.replace(/^sha256:/, '');
	return hex.slice(0, 12);
}
