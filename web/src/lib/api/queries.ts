// Svelte Query integration for the generated client (docs/web.md).
//
// Each server resource gets a query-key factory entry and an options
// factory built with queryOptions(), so components (createQuery) and
// imperative code (queryClient.fetchQuery / invalidateQueries) share one
// definition. Resource keys follow the live conventions (liveKeys,
// src/lib/live/keys.ts), so the #23 live client refreshes them.
import {
	MutationCache,
	QueryCache,
	QueryClient,
	queryOptions,
	type QueryKey
} from '@tanstack/svelte-query';
import { liveKeys } from '$lib/live/keys';
import {
	api,
	ApiRequestError,
	unwrap,
	type ApiClient,
	type Environment,
	type EnvironmentSystem,
	type Job,
	type MyPermissions,
	type Overview,
	type SchedulePreview,
	type SearchResults,
	type Session
} from './client';

/**
 * Query keys. Invalidate by prefix, e.g. queryKeys.environments.all.
 * Server resources use liveKeys shapes ([topic, 'list', ...filters],
 * [topic, 'item', id, ...sub]) so live events refresh them; session, setup
 * and health are not resources of the live stream.
 */
export const queryKeys = {
	health: ['health'] as const,
	setupStatus: ['setup', 'status'] as const,
	session: ['session'] as const,
	me: ['me'] as const,
	overview: ['overview'] as const,
	myPermissions: liveKeys.myPermissions,
	environments: {
		all: ['environments'] as const,
		list: () => liveKeys.list('environments'),
		detail: (id: string) => liveKeys.item('environments', id),
		system: (id: string) => liveKeys.item('environments', id, 'system')
	},
	jobs: {
		all: ['jobs'] as const,
		detail: (id: string) => liveKeys.item('jobs', id)
	},
	search: (q: string, environmentId: string | null) =>
		['search', q, environmentId ?? ''] as const,
	schedulePreview: (cron: string, timeZone: string, kind?: string) =>
		['schedules', 'preview', cron, timeZone, kind ?? ''] as const
} satisfies Record<string, unknown>;

/** GET /api/v1/health (liveness, public). */
export function healthQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.health,
		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/health', { signal })),
		staleTime: 30_000
	});
}

/** GET /api/v1/setup/status (public): whether the owner exists. */
export function setupStatusQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.setupStatus,
		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/setup/status', { signal })),
		staleTime: 10_000
	});
}

/**
 * GET /api/v1/auth/session. Resolves to null when there is no session
 * (401), so "signed out" is data, not an error.
 */
export function sessionQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.session,
		queryFn: async ({ signal }): Promise<Session | null> => {
			try {
				return await unwrap(client.GET('/api/v1/auth/session', { signal }));
			} catch (e) {
				if (e instanceof ApiRequestError && e.status === 401) return null;
				throw e;
			}
		},
		staleTime: 60_000
	});
}

/** GET /api/v1/me/permissions: effective permissions and visible environments. */
export function myPermissionsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.myPermissions,
		queryFn: ({ signal }): Promise<MyPermissions> =>
			unwrap(client.GET('/api/v1/me/permissions', { signal })),
		staleTime: 60_000
	});
}

/** GET /api/v1/overview: counts and usage of the visible environments. */
export function overviewQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.overview,
		queryFn: ({ signal }): Promise<Overview> =>
			unwrap(client.GET('/api/v1/overview', { signal })),
		staleTime: 15_000
	});
}

/** Every active environment the caller may see (all pages). */
export function environmentsQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.environments.list(),
		queryFn: async ({ signal }): Promise<Environment[]> => {
			const out: Environment[] = [];
			let cursor: string | undefined;
			do {
				const page = await unwrap(
					client.GET('/api/v1/environments', {
						params: { query: { limit: 200, cursor } },
						signal
					})
				);
				out.push(...page.items);
				cursor = page.nextCursor;
			} while (cursor);
			return out;
		},
		staleTime: 30_000
	});
}

/** GET /environments/{id}/system (environment.system.read): Engine version etc. */
export function environmentSystemQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.environments.system(id),
		queryFn: ({ signal }): Promise<EnvironmentSystem> =>
			unwrap(
				client.GET('/api/v1/environments/{environmentId}/system', {
					params: { path: { environmentId: id } },
					signal
				})
			),
		staleTime: 60_000
	});
}

/** GET /api/v1/jobs/{jobId}. */
export function jobQuery(id: string, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.jobs.detail(id),
		queryFn: ({ signal }): Promise<Job> =>
			unwrap(client.GET('/api/v1/jobs/{jobId}', { params: { path: { jobId: id } }, signal }))
	});
}

/** GET /api/v1/search (command palette). */
export function searchQuery(q: string, environmentId: string | null, client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.search(q, environmentId),
		queryFn: ({ signal }): Promise<SearchResults> =>
			unwrap(
				client.GET('/api/v1/search', {
					params: { query: { q, limit: 20, environmentId: environmentId ?? undefined } },
					signal
				})
			),
		enabled: q.trim().length > 0,
		staleTime: 15_000
	});
}

/** POST /api/v1/schedules/previews: next runs of a cron expression (a read). */
export function schedulePreviewQuery(
	cron: string,
	timeZone: string,
	kind?: string,
	client: ApiClient = api
) {
	return queryOptions({
		queryKey: queryKeys.schedulePreview(cron, timeZone, kind),
		queryFn: ({ signal }): Promise<SchedulePreview> =>
			unwrap(
				client.POST('/api/v1/schedules/previews', {
					body: { cron, timeZone: timeZone || undefined, kind, count: 5 },
					signal
				})
			),
		enabled: cron.trim().length > 0,
		staleTime: 60_000,
		retry: false
	});
}

const MAX_RETRIES = 2;

/**
 * Retry policy: network failures and 5xx/408/429 are retried a bounded
 * number of times; other client errors (400, 401, 403, 404, 409, 412, ...)
 * are final. Mutations are never retried automatically (duplicate Docker
 * operations); they use idempotency keys instead (#4).
 */
export function shouldRetry(failureCount: number, error: unknown): boolean {
	if (failureCount >= MAX_RETRIES) return false;
	if (!(error instanceof ApiRequestError)) return false;
	if (error.status === null) return true;
	return error.status >= 500 || error.status === 408 || error.status === 429;
}

export type QueryOutcome = { ok: true } | { ok: false; error: unknown };

export interface QueryClientHooks {
	/**
	 * Called when a request answered 401 (the session ended or expired).
	 * The session query itself is excluded: it reports "signed out" as data.
	 */
	onUnauthenticated?: (key: QueryKey | undefined) => void;
}

function isUnauthenticated(error: unknown): boolean {
	return error instanceof ApiRequestError && error.status === 401;
}

/**
 * The app-wide QueryClient (one per page load). `observe` receives every
 * query outcome; the root layout feeds it to the connectivity state.
 */
export function createQueryClient(
	observe?: (outcome: QueryOutcome) => void,
	hooks: QueryClientHooks = {}
): QueryClient {
	return new QueryClient({
		queryCache: new QueryCache({
			onSuccess: () => observe?.({ ok: true }),
			onError: (error, query) => {
				observe?.({ ok: false, error });
				if (isUnauthenticated(error)) hooks.onUnauthenticated?.(query.queryKey);
			}
		}),
		mutationCache: new MutationCache({
			onError: (error) => {
				if (isUnauthenticated(error)) hooks.onUnauthenticated?.(undefined);
			}
		}),
		defaultOptions: {
			queries: { retry: shouldRetry, refetchOnWindowFocus: true },
			mutations: { retry: false }
		}
	});
}
