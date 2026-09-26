import { describe, expect, it } from 'vitest';
import {
	CACHE_PREFIX,
	cacheName,
	handleFetch,
	isNetworkOnlyPath,
	isSkipWaitingMessage,
	OFFLINE_SHELL_HEADER,
	precache,
	precachePaths,
	removeStaleCaches,
	route,
	SHELL_PATH,
	type CacheLike,
	type CacheStorageLike,
	type SwDeps
} from './sw-core';

const origin = 'https://docker-manager.example';

class FakeCache implements CacheLike {
	entries = new Map<string, Response>();
	puts: string[] = [];
	async match(req: RequestInfo | URL) {
		const key = req instanceof Request ? req.url : String(req);
		return this.entries.get(key)?.clone();
	}
	async put(req: RequestInfo | URL, res: Response) {
		const key = req instanceof Request ? req.url : String(req);
		this.puts.push(key);
		this.entries.set(key, res);
	}
}

class FakeCacheStorage implements CacheStorageLike {
	caches = new Map<string, FakeCache>();
	async open(name: string) {
		let c = this.caches.get(name);
		if (!c) this.caches.set(name, (c = new FakeCache()));
		return c;
	}
	async match(req: RequestInfo | URL) {
		for (const c of this.caches.values()) {
			const hit = await c.match(req);
			if (hit) return hit;
		}
		return undefined;
	}
	async keys() {
		return [...this.caches.keys()];
	}
	async delete(name: string) {
		return this.caches.delete(name);
	}
	/** Every URL stored in any cache. */
	allUrls() {
		return [...this.caches.values()].flatMap((c) => [...c.entries.keys()]);
	}
}

type FetchImpl = (req: Request) => Promise<Response>;

function setup(fetchImpl: FetchImpl = async () => new Response('net')) {
	const storage = new FakeCacheStorage();
	const fetched: Request[] = [];
	const deps: SwDeps = {
		caches: storage,
		fetch: async (req) => {
			fetched.push(req);
			return fetchImpl(req);
		},
		origin,
		cacheName: cacheName('v2'),
		precached: precachePaths([
			{ url: 'index.html', revision: 'abc' },
			{ url: 'manifest.webmanifest', revision: 'def' },
			{ url: '_app/immutable/entry/start.X1.js', revision: null },
			{ url: '_app/immutable/chunks/lazy.Y2.js', revision: null },
			{ url: 'icons/pwa-192x192.png', revision: '1' }
		])
	};
	return { storage, fetched, deps };
}

function get(path: string, mode: RequestMode = 'cors', init: RequestInit = {}) {
	// Node's Request forbids constructing mode 'navigate'; the router only
	// reads method/url/mode, so pass a plain object for navigations.
	if (mode === 'navigate') {
		return { method: init.method ?? 'GET', url: origin + path, mode } as unknown as Request;
	}
	return new Request(origin + path, { mode, ...init });
}

describe('route', () => {
	const pre = new Set(['/_app/immutable/a.js', '/index.html']);
	const r = (path: string, mode = 'cors', method = 'GET', base = origin) =>
		route({ method, url: base + path, mode }, origin, pre).kind;

	it('never handles API or agent paths, whatever the request mode', () => {
		for (const p of [
			'/api',
			'/api/',
			'/api/v1/health',
			'/api/v1/stacks?x=1',
			'/api/v1/live/stream',
			'/agent',
			'/agent/v1/session',
			'/api/../api/v1/health'
		]) {
			for (const mode of ['cors', 'navigate', 'same-origin', 'no-cors']) {
				expect(r(p, mode), `${p} ${mode}`).toBe('network');
			}
		}
	});

	it('does not treat look-alike paths as API paths', () => {
		expect(isNetworkOnlyPath('/apis')).toBe(false);
		expect(isNetworkOnlyPath('/agents/list')).toBe(false);
		expect(r('/apis', 'navigate')).toBe('navigate');
	});

	it('handles only same-origin GET requests', () => {
		for (const m of ['POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']) {
			expect(r('/_app/immutable/a.js', 'cors', m), m).toBe('network');
			expect(r('/stacks', 'navigate', m), m).toBe('network');
		}
		expect(r('/_app/immutable/a.js', 'cors', 'GET', 'https://evil.example')).toBe('network');
		expect(r('/stacks', 'navigate', 'GET', 'http://docker-manager.example')).toBe('network');
	});

	it('routes navigations (deep links) to the network-first shell handler', () => {
		expect(r('/', 'navigate')).toBe('navigate');
		expect(r('/stacks/abc/files', 'navigate')).toBe('navigate');
	});

	it('serves only precached paths from the precache', () => {
		expect(
			route(
				{ method: 'GET', url: origin + '/_app/immutable/a.js?v=1', mode: 'cors' },
				origin,
				pre
			)
		).toEqual({
			kind: 'precache',
			path: '/_app/immutable/a.js'
		});
		expect(r('/_app/immutable/other.js')).toBe('network');
		expect(r('/_app/version.json')).toBe('network');
		expect(r('/robots.txt')).toBe('network');
	});

	it('ignores unparsable URLs', () => {
		expect(route({ method: 'GET', url: 'not a url', mode: 'cors' }, origin, pre).kind).toBe(
			'network'
		);
	});
});

describe('precachePaths', () => {
	it('drops API and agent entries even if a glob matched them', () => {
		const paths = precachePaths([
			{ url: 'index.html', revision: '1' },
			{ url: 'api/v1/health', revision: '1' },
			{ url: '/agent/v1/x', revision: '1' },
			{ url: '_app/immutable/a.js', revision: null }
		]);
		expect([...paths].sort()).toEqual(['/_app/immutable/a.js', '/index.html']);
	});
});

describe('install and activate', () => {
	it('precaches every entry into the build cache, bypassing the HTTP cache', async () => {
		const { storage, fetched, deps } = setup(async (req) => new Response(req.url));
		await precache(deps);
		const cache = storage.caches.get(cacheName('v2'))!;
		expect([...cache.entries.keys()].sort()).toEqual(
			[...deps.precached].map((p) => origin + p).sort()
		);
		expect(fetched.every((r) => r.cache === 'reload')).toBe(true);
		expect(storage.allUrls().some((u) => new URL(u).pathname.startsWith('/api'))).toBe(false);
	});

	it('reuses content-hashed files from an older build instead of downloading them', async () => {
		const { storage, fetched, deps } = setup(async (req) => new Response('fresh ' + req.url));
		const old = await storage.open(cacheName('v1'));
		await old.put(origin + '/_app/immutable/entry/start.X1.js', new Response('old start'));
		await old.put(origin + '/index.html', new Response('old shell'));
		await precache(deps);
		const paths = fetched.map((r) => new URL(r.url).pathname);
		expect(paths).not.toContain('/_app/immutable/entry/start.X1.js');
		// Unhashed files (the shell) are always downloaded again.
		expect(paths).toContain('/index.html');
		const cache = storage.caches.get(cacheName('v2'))!;
		expect(
			await (await cache.match(origin + '/_app/immutable/entry/start.X1.js'))!.text()
		).toBe('old start');
		expect(await (await cache.match(origin + SHELL_PATH))!.text()).toBe(
			'fresh ' + origin + '/index.html'
		);
	});

	it('fails the install on any non-OK or redirected response', async () => {
		const { deps } = setup(async (req) =>
			req.url.endsWith('.png') ? new Response('', { status: 404 }) : new Response('ok')
		);
		await expect(precache(deps)).rejects.toThrow('HTTP 404');

		const redirected = setup(async () => {
			const res = new Response('login page');
			Object.defineProperty(res, 'redirected', { value: true });
			return res;
		});
		await expect(precache(redirected.deps)).rejects.toThrow('redirected');
	});

	it('activation deletes only older Docker Manager caches', async () => {
		const { storage, deps } = setup();
		for (const n of [cacheName('v1'), cacheName('v2'), 'someone-else']) await storage.open(n);
		expect(await removeStaleCaches(deps)).toEqual([CACHE_PREFIX + 'v1']);
		expect(await storage.keys()).toEqual([cacheName('v2'), 'someone-else']);
	});
});

describe('handleFetch', () => {
	it('leaves API, agent and mutation requests to the browser and stores nothing', async () => {
		const { storage, fetched, deps } = setup();
		await precache(deps);
		const before = storage.allUrls().length;
		for (const req of [
			get('/api/v1/health'),
			get('/api/v1/stacks', 'navigate'),
			get('/agent/v1/session'),
			get('/api/v1/stacks', 'cors', { method: 'POST', body: '{}' }),
			get('/stacks', 'cors', { method: 'DELETE' })
		]) {
			expect(handleFetch(req, deps), req.url).toBeNull();
		}
		expect(storage.allUrls().length).toBe(before);
		expect(
			fetched.map((r) => new URL(r.url).pathname).filter((p) => p.startsWith('/api'))
		).toEqual([]);
	});

	it('serves precached assets from the cache without the network', async () => {
		const { deps, fetched } = setup(async () => new Response('cached copy'));
		await precache(deps);
		fetched.length = 0;
		const res = await handleFetch(get('/_app/immutable/entry/start.X1.js'), deps)!;
		expect(await res.text()).toBe('cached copy');
		expect(fetched).toHaveLength(0);
	});

	it('falls back to the network for a precache miss without storing the response', async () => {
		const { deps, storage } = setup(async () => new Response('from network'));
		const res = await handleFetch(get('/_app/immutable/entry/start.X1.js'), deps)!;
		expect(await res.text()).toBe('from network');
		expect(storage.allUrls()).toEqual([]);
	});

	it('navigations use the network while online and never store the page', async () => {
		let online = true;
		const { deps, storage } = setup(async (req) => {
			if (!online) throw new TypeError('Failed to fetch');
			return new Response(req.url.endsWith('/index.html') ? 'precached shell' : 'live page');
		});
		await precache(deps);
		const before = storage.allUrls().length;

		const live = await handleFetch(get('/stacks/abc', 'navigate'), deps)!;
		expect(await live.text()).toBe('live page');
		expect(storage.allUrls().length).toBe(before);

		online = false;
		const offline = await handleFetch(get('/stacks/abc', 'navigate'), deps)!;
		expect(offline.status).toBe(200);
		expect(await offline.text()).toBe('precached shell');
		expect(offline.headers.get(OFFLINE_SHELL_HEADER)).toBe('offline');
		expect(live.headers.get(OFFLINE_SHELL_HEADER)).toBeNull();
	});

	it('shows the offline shell when the proxy reports the manager down', async () => {
		const answerWith = async (status: number) => {
			let installed = false;
			const { deps } = setup(async (req) =>
				!installed
					? new Response(req.url.endsWith('/index.html') ? 'shell' : 'asset')
					: new Response('upstream says ' + status, { status })
			);
			await precache(deps);
			installed = true;
			return handleFetch(get('/stacks/x', 'navigate'), deps)!;
		};
		for (const status of [502, 503, 504]) {
			const res = await answerWith(status);
			expect(res.status, String(status)).toBe(200);
			expect(await res.text(), String(status)).toBe('shell');
		}
		// Other statuses are real answers and pass through.
		for (const status of [200, 404, 500]) {
			const res = await answerWith(status);
			expect(res.status, String(status)).toBe(status);
			expect(await res.text()).toBe('upstream says ' + status);
		}
	});

	it('answers 503 no-store when offline before the shell was cached', async () => {
		const { deps } = setup(async () => {
			throw new TypeError('Failed to fetch');
		});
		const res = await handleFetch(get('/', 'navigate'), deps)!;
		expect(res.status).toBe(503);
		expect(res.headers.get('Cache-Control')).toBe('no-store');
	});
});

describe('isSkipWaitingMessage', () => {
	it('accepts only the explicit update message', () => {
		expect(isSkipWaitingMessage({ type: 'SKIP_WAITING' })).toBe(true);
		for (const m of [null, undefined, 'SKIP_WAITING', {}, { type: 'skip' }]) {
			expect(isSkipWaitingMessage(m)).toBe(false);
		}
	});
});
