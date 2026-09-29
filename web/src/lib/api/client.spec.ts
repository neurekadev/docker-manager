import { describe, expect, it } from 'vitest';
import { ApiRequestError, createApiClient, onApiFailure, unwrap } from './client';
import { createQueryClient, healthQuery, queryKeys, shouldRetry } from './queries';

const base = 'http://localhost:8080';

function fakeFetch(status: number, body: unknown, contentType = 'application/json') {
	const calls: Request[] = [];
	const impl = async (input: RequestInfo | URL, init?: RequestInit) => {
		const req =
			input instanceof Request
				? input
				: new Request(new URL(String(input), 'http://localhost'), init);
		calls.push(req);
		return new Response(JSON.stringify(body), {
			status,
			headers: { 'Content-Type': contentType }
		});
	};
	return { impl: impl as typeof fetch, calls };
}

const failing = (async () => {
	throw new TypeError('Failed to fetch');
}) as typeof fetch;

const apiError = {
	code: 'unavailable',
	message: 'manager is starting',
	details: [],
	requestId: 'req-1',
	retryable: true
};

describe('unwrap', () => {
	it('returns the typed health body', async () => {
		const f = fakeFetch(200, { status: 'ok', version: '0.0.0-edge', commit: 'abc' });
		const client = createApiClient(f.impl, base);
		const health = await unwrap(client.GET('/api/v1/health'));
		expect(health).toEqual({ status: 'ok', version: '0.0.0-edge', commit: 'abc' });
		expect(new URL(f.calls[0].url).pathname).toBe('/api/v1/health');
		expect(f.calls[0].headers.get('Accept')).toBe('application/json');
	});

	it('resolves 204 No Content to undefined', async () => {
		const client = createApiClient(
			(async () => new Response(null, { status: 204 })) as typeof fetch,
			base
		);
		await expect(
			unwrap(
				client.DELETE('/api/v1/registries/{registryId}', {
					params: { path: { registryId: 'r1' }, header: { 'If-Match': '"1"' } }
				})
			)
		).resolves.toBeUndefined();
	});

	it('throws the Docker Manager error shape', async () => {
		const f = fakeFetch(503, apiError, 'application/problem+json');
		const err = await unwrap(createApiClient(f.impl, base).GET('/api/v1/health')).catch(
			(e) => e
		);
		expect(err).toBeInstanceOf(ApiRequestError);
		expect(err).toMatchObject({ message: 'manager is starting', status: 503, apiError });
		expect(err.network).toBe(false);
	});

	it('tells failure listeners about every error answer, until they stop listening', async () => {
		const heard: (string | undefined)[] = [];
		const stop = onApiFailure((e) => heard.push(e.apiError?.code));
		const noisy = onApiFailure(() => {
			throw new Error('listener bug');
		});
		const moved = { ...apiError, code: 'manager_moved', retryable: false };
		const f = fakeFetch(409, moved);
		const err = await unwrap(createApiClient(f.impl, base).GET('/api/v1/health')).catch(
			(e) => e
		);
		// A failing listener never replaces the caller's error.
		expect(err).toMatchObject({ status: 409, apiError: moved });
		expect(heard).toEqual(['manager_moved']);
		stop();
		noisy();
		await unwrap(createApiClient(f.impl, base).GET('/api/v1/health')).catch(() => {});
		expect(heard).toEqual(['manager_moved']);
	});

	it('falls back to the HTTP status for non-Docker Manager error bodies', async () => {
		const f = fakeFetch(502, { oops: true });
		const err = await unwrap(createApiClient(f.impl, base).GET('/api/v1/health')).catch(
			(e) => e
		);
		expect(err).toMatchObject({ message: 'HTTP 502', status: 502, apiError: undefined });
	});

	it('reports network failures as status null', async () => {
		const err = await unwrap(createApiClient(failing, base).GET('/api/v1/health')).catch(
			(e) => e
		);
		expect(err).toBeInstanceOf(ApiRequestError);
		expect(err).toMatchObject({ message: 'Failed to fetch', status: null });
		expect(err.network).toBe(true);
	});
});

describe('Svelte Query integration', () => {
	it('healthQuery fetches through the generated client', async () => {
		const f = fakeFetch(200, { status: 'ok', version: '1', commit: 'c' });
		const qc = createQueryClient();
		const data = await qc.fetchQuery(healthQuery(createApiClient(f.impl, base)));
		expect(data.version).toBe('1');
		expect(qc.getQueryData(queryKeys.health)).toEqual(data);
		// The query passes an AbortSignal so unmounted views cancel requests.
		expect(f.calls[0].signal).toBeInstanceOf(AbortSignal);
	});

	it('does not retry client errors', async () => {
		const f = fakeFetch(404, { ...apiError, code: 'not_found', message: 'nope' });
		const qc = createQueryClient();
		await expect(qc.fetchQuery(healthQuery(createApiClient(f.impl, base)))).rejects.toThrow(
			'nope'
		);
		expect(f.calls).toHaveLength(1);
	});

	it('retry policy is bounded and skips non-retryable statuses', () => {
		const net = new ApiRequestError('x', null);
		expect(shouldRetry(0, net)).toBe(true);
		expect(shouldRetry(1, net)).toBe(true);
		expect(shouldRetry(2, net)).toBe(false);
		for (const s of [500, 502, 503, 408, 429]) {
			expect(shouldRetry(0, new ApiRequestError('x', s)), String(s)).toBe(true);
		}
		for (const s of [400, 401, 403, 404, 409, 412]) {
			expect(shouldRetry(0, new ApiRequestError('x', s)), String(s)).toBe(false);
		}
		expect(shouldRetry(0, new Error('bug'))).toBe(false);
	});
});
