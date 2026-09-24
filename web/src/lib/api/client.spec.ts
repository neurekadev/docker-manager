import { describe, expect, it } from 'vitest';
import { createApiClient, getHealth } from './client';

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

describe('getHealth', () => {
	it('returns the typed health body', async () => {
		const f = fakeFetch(200, { status: 'ok', version: '0.0.0-edge', commit: 'abc' });
		const result = await getHealth(createApiClient(f.impl, base));
		expect(result).toEqual({
			ok: true,
			data: { status: 'ok', version: '0.0.0-edge', commit: 'abc' }
		});
		expect(new URL(f.calls[0].url).pathname).toBe('/api/v1/health');
	});

	it('surfaces the DockYard error shape', async () => {
		const apiError = {
			code: 'unavailable',
			message: 'manager is starting',
			details: [],
			requestId: 'req-1',
			retryable: true
		};
		const f = fakeFetch(503, apiError, 'application/problem+json');
		const result = await getHealth(createApiClient(f.impl, base));
		expect(result).toEqual({ ok: false, error: 'manager is starting', apiError });
	});

	it('reports network failures', async () => {
		const failing = (async () => {
			throw new TypeError('Failed to fetch');
		}) as typeof fetch;
		const result = await getHealth(createApiClient(failing, base));
		expect(result).toEqual({ ok: false, error: 'Failed to fetch' });
	});
});
