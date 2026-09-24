// Svelte Query integration for the generated client (docs/web.md).
//
// Each server resource gets a query-key factory entry and an options
// factory built with queryOptions(), so components (createQuery) and
// imperative code (queryClient.fetchQuery / invalidateQueries) share one
// definition. Live invalidation from the manager event stream is #23.
import { QueryCache, QueryClient, queryOptions } from '@tanstack/svelte-query';
import { api, ApiRequestError, unwrap, type ApiClient } from './client';

/** Query keys. Invalidate by prefix, e.g. queryKeys.health. */
export const queryKeys = {
	health: ['health'] as const
};

/** GET /api/v1/health (liveness, public). */
export function healthQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: queryKeys.health,
		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/health', { signal })),
		staleTime: 30_000
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

/**
 * The app-wide QueryClient (one per page load). `observe` receives every
 * query outcome; the root layout feeds it to the connectivity state.
 */
export function createQueryClient(observe?: (outcome: QueryOutcome) => void): QueryClient {
	return new QueryClient({
		queryCache: new QueryCache({
			onSuccess: () => observe?.({ ok: true }),
			onError: (error) => observe?.({ ok: false, error })
		}),
		defaultOptions: {
			queries: { retry: shouldRetry, refetchOnWindowFocus: true },
			mutations: { retry: false }
		}
	});
}
