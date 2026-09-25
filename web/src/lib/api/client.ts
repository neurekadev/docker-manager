// Typed client for the DockYard /api/v1 contract.
//
// schema.d.ts is GENERATED from api/openapi.json by openapi-typescript; do
// not edit it. Regenerate with `bash scripts/generate.sh` after changing Go
// operations. Paths are absolute (/api/v1/...) and same-origin: the browser
// only ever talks to the manager's public origin. Workflow: docs/web.md.
import createClient from 'openapi-fetch';
import type { components, paths } from './schema';

/** Schema type by name: Schema<'Stack'>. */
export type Schema<K extends keyof components['schemas']> = components['schemas'][K];

export type Health = components['schemas']['HealthBody'];
export type ApiError = components['schemas']['Error'];
export type Session = Schema<'Session'>;
export type Account = Schema<'Account'>;
export type MyPermissions = Schema<'MyPermissions'>;
export type Environment = Schema<'Environment'>;
export type EnvironmentSystem = Schema<'EnvironmentSystem'>;
export type Job = Schema<'Job'>;
export type JobEvent = Schema<'JobEvent'>;
export type JobItem = Schema<'JobItem'>;
export type SearchHit = Schema<'SearchHit'>;
export type SearchResults = Schema<'SearchResults'>;
export type SchedulePreview = Schema<'SchedulePreview'>;
export type Overview = Schema<'Overview'>;
export type ApiClient = ReturnType<typeof createApiClient>;

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

// Same-origin absolute base (relative URLs also work in browsers; tests in
// jsdom need an absolute one).
export const api = createApiClient(undefined, globalThis.location?.origin ?? '');

/**
 * A failed API call. `status` is null when the request never produced an
 * HTTP response (network failure, manager or proxy unreachable, offline).
 * `apiError` is the DockYard error body when the server sent one; switch on
 * `apiError.code`, never on `message`.
 */
export class ApiRequestError extends Error {
	readonly status: number | null;
	readonly apiError?: ApiError;

	constructor(
		message: string,
		status: number | null,
		apiError?: ApiError,
		options?: ErrorOptions
	) {
		super(message, options);
		this.name = 'ApiRequestError';
		this.status = status;
		this.apiError = apiError;
	}

	/** True when no HTTP response arrived (connectivity, not a server answer). */
	get network(): boolean {
		return this.status === null;
	}
}

function isApiError(v: unknown): v is ApiError {
	return (
		typeof v === 'object' &&
		v !== null &&
		typeof (v as ApiError).code === 'string' &&
		typeof (v as ApiError).message === 'string'
	);
}

/**
 * Awaits an openapi-fetch call and returns its typed data, or throws an
 * ApiRequestError. This is the adapter between the generated client and
 * Svelte Query, whose queryFn must throw on failure.
 *
 *   queryFn: ({ signal }) => unwrap(api.GET('/api/v1/health', { signal }))
 */
export async function unwrap<T>(
	call: Promise<{ data?: T; error?: unknown; response: Response }>
): Promise<T> {
	let result: { data?: T; error?: unknown; response: Response };
	try {
		result = await call;
	} catch (e) {
		if (e instanceof DOMException && e.name === 'AbortError') throw e;
		const message = e instanceof Error ? e.message : String(e);
		throw new ApiRequestError(message, null, undefined, { cause: e });
	}
	const { data, error, response } = result;
	if (response.ok && data !== undefined) return data;
	const apiError = isApiError(error) ? error : undefined;
	throw new ApiRequestError(
		apiError?.message ?? `HTTP ${response.status}`,
		response.status,
		apiError
	);
}
