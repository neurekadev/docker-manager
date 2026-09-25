import { describe, expect, it } from 'vitest';
import { ApiRequestError, createApiClient, unwrap } from './client';
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

	it('throws the DockYard error shape', async () => {
		const f = fakeFetch(503, apiError, 'application/problem+json');
		const err = await unwrap(createApiClient(f.impl, base).GET('/api/v1/health')).catch(
			(e) => e
		);
		expect(err).toBeInstanceOf(ApiRequestError);
		expect(err).toMatchObject({ message: 'manager is starting', status: 503, apiError });
		expect(err.network).toBe(false);
	});

	it('falls back to the HTTP status for non-DockYard error bodies', async () => {
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
