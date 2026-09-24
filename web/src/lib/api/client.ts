// Typed client for the DockYard /api/v1 contract.
//
// schema.d.ts is GENERATED from api/openapi.json by openapi-typescript; do
// not edit it. Regenerate with `bash scripts/generate.sh` after changing Go
// operations. Paths are absolute (/api/v1/...) and same-origin: the browser
// only ever talks to the manager's public origin.
import createClient from 'openapi-fetch';
import type { components, paths } from './schema';

export type Health = components['schemas']['HealthBody'];
export type ApiError = components['schemas']['Error'];

/**
 * Creates a client. The browser uses same-origin relative URLs (baseUrl '');
 * tests pass their own fetch and an absolute base URL.
 */
export function createApiClient(
	fetchImpl: typeof fetch = (...args) => globalThis.fetch(...args),
	baseUrl = ''
) {
	return createClient<paths>({
		baseUrl,
		fetch: fetchImpl,
		credentials: 'same-origin',
		headers: { Accept: 'application/json' }
	});
}

export const api = createApiClient();

export type Result<T> = { ok: true; data: T } | { ok: false; error: string; apiError?: ApiError };

/** Liveness of the manager (GET /api/v1/health). */
export async function getHealth(client = api): Promise<Result<Health>> {
	try {
		const { data, error, response } = await client.GET('/api/v1/health');
		if (data) return { ok: true, data };
		return {
			ok: false,
			error: error?.message ?? `HTTP ${response.status}`,
			apiError: error
		};
	} catch (e) {
		return { ok: false, error: e instanceof Error ? e.message : String(e) };
	}
}
