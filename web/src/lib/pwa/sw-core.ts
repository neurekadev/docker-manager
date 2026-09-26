// Service-worker routing and caching rules (#11, #23), kept free of
// service-worker globals so vitest can exercise them (sw-core.spec.ts).
// src/service-worker.ts wires them to the real events.
//
// Rules, in order:
//   1. Non-GET, cross-origin, /api/* and /agent/* requests are NOT handled:
//      the browser sends them straight to the network and nothing is stored.
//      API data, credentials, file contents, logs, terminals and job streams
//      never enter Cache Storage.
//   2. Navigations go to the network first (the manager serves index.html
//      with no-cache). Only when the network fails (offline, proxy 502-504)
//      is the precached app shell returned: the offline shell.
//   3. Precached files (content-hashed /_app/immutable/*, icons, manifest,
//      the shell) are served from the precache of this build.
//   4. Everything else is not handled (network only).
//
// The service worker never writes to a cache outside install, so no runtime
// response (authenticated or not) is ever persisted.

/** A precache manifest entry injected by @vite-pwa/sveltekit (workbox-build). */
export interface PrecacheEntry {
	url: string;
	revision: string | null;
}

/** Path prefixes that must never be answered from, or stored in, a cache. */
export const NETWORK_ONLY_PREFIXES = ['/api', '/agent'] as const;

/** Cache names start with this; anything else in Cache Storage is not ours. */
export const CACHE_PREFIX = 'docker-manager-precache-';

/** The precached SPA shell served for offline navigations. */
export const SHELL_PATH = '/index.html';

/** Statuses that mean "the manager is unreachable" rather than an answer. */
const GATEWAY_FAILURES = new Set([502, 503, 504]);

export type Route =
	| { kind: 'network' } // not handled by the service worker
	| { kind: 'navigate' }
	| { kind: 'precache'; path: string };

export interface RequestInfoLike {
	method: string;
	url: string;
	mode: RequestMode | string;
}

export function isNetworkOnlyPath(pathname: string): boolean {
	return NETWORK_ONLY_PREFIXES.some((p) => pathname === p || pathname.startsWith(p + '/'));
}

/** Decides how the service worker treats a request. */
export function route(req: RequestInfoLike, origin: string, precached: ReadonlySet<string>): Route {
	if (req.method !== 'GET') return { kind: 'network' };
	let url: URL;
	try {
		url = new URL(req.url);
	} catch {
		return { kind: 'network' };
	}
	if (url.origin !== origin) return { kind: 'network' };
	// Decode-free, normalised check: "/api/../x" was already resolved by URL.
	if (isNetworkOnlyPath(url.pathname)) return { kind: 'network' };
	if (req.mode === 'navigate') return { kind: 'navigate' };
	if (precached.has(url.pathname)) return { kind: 'precache', path: url.pathname };
	return { kind: 'network' };
}

/** Absolute paths of every precache entry, plus the shell. */
export function precachePaths(entries: readonly PrecacheEntry[]): Set<string> {
	const out = new Set<string>();
	for (const e of entries) {
		const path = new URL(e.url, 'https://sw.invalid/').pathname;
		if (isNetworkOnlyPath(path)) {
			// Never precache API or agent routes, even if a glob matched one.
			continue;
		}
		out.add(path);
	}
	return out;
}

/**
 * A cache name unique to this build's precache manifest, so a new build
 * installs into a fresh cache and the old one is deleted on activation.
 */
export function cacheName(version: string): string {
	return CACHE_PREFIX + version;
}

export interface CacheLike {
	match(request: RequestInfo | URL): Promise<Response | undefined>;
	put(request: RequestInfo | URL, response: Response): Promise<void>;
}

export interface CacheStorageLike {
	open(name: string): Promise<CacheLike>;
	match(request: RequestInfo | URL): Promise<Response | undefined>;
	keys(): Promise<string[]>;
	delete(name: string): Promise<boolean>;
}

export interface SwDeps {
	caches: CacheStorageLike;
	fetch: (request: Request) => Promise<Response>;
	origin: string;
	cacheName: string;
	precached: ReadonlySet<string>;
}

/** Content-hashed build output: the URL changes whenever the bytes do. */
export const IMMUTABLE_PREFIX = '/_app/immutable/';

/**
 * Install step: fills this build's cache with every precache path. Content-
 * hashed files already cached by an earlier build are copied over; all
 * other files are downloaded, bypassing the HTTP cache. Fails (and so
 * aborts the install) on any non-OK response, so a half-populated precache
 * is never activated.
 */
export async function precache(deps: SwDeps): Promise<void> {
	const cache = await deps.caches.open(deps.cacheName);
	await Promise.all(
		[...deps.precached].map(async (path) => {
			const url = new URL(path, deps.origin).toString();
			if (path.startsWith(IMMUTABLE_PREFIX)) {
				const previous = await deps.caches.match(url);
				if (previous) {
					await cache.put(url, previous);
					return;
				}
			}
			const res = await deps.fetch(
				new Request(url, { cache: 'reload', credentials: 'same-origin' })
			);
			if (!res.ok) throw new Error(`precache ${path}: HTTP ${res.status}`);
			if (res.redirected) throw new Error(`precache ${path}: redirected`);
			await cache.put(url, res);
		})
	);
}

/** Activate step: deletes precaches of older builds (only our own caches). */
export async function removeStaleCaches(
	deps: Pick<SwDeps, 'caches' | 'cacheName'>
): Promise<string[]> {
	const removed: string[] = [];
	for (const name of await deps.caches.keys()) {
		if (name.startsWith(CACHE_PREFIX) && name !== deps.cacheName) {
			await deps.caches.delete(name);
			removed.push(name);
		}
	}
	return removed;
}

/** Marks the precached shell served in place of a failed navigation. */
export const OFFLINE_SHELL_HEADER = 'X-Docker-Manager-Shell';

function markOfflineShell(shell: Response): Response {
	const headers = new Headers(shell.headers);
	headers.set(OFFLINE_SHELL_HEADER, 'offline');
	return new Response(shell.body, {
		status: shell.status,
		statusText: shell.statusText,
		headers
	});
}

function offlineResponse(): Response {
	return new Response('Docker Manager is offline and the app shell is not cached yet.', {
		status: 503,
		headers: { 'Content-Type': 'text/plain; charset=utf-8', 'Cache-Control': 'no-store' }
	});
}

/**
 * Fetch step. Returns null when the service worker must not handle the
 * request (the caller then leaves it to the browser), otherwise the
 * response to use. Never writes to a cache.
 */
export function handleFetch(request: Request, deps: SwDeps): Promise<Response> | null {
	const r = route(request, deps.origin, deps.precached);
	switch (r.kind) {
		case 'network':
			return null;
		case 'precache':
			return (async () => {
				const cache = await deps.caches.open(deps.cacheName);
				const hit = await cache.match(new URL(r.path, deps.origin).toString());
				return hit ?? deps.fetch(request);
			})();
		case 'navigate':
			return (async () => {
				try {
					const res = await deps.fetch(request);
					if (!GATEWAY_FAILURES.has(res.status)) return res;
				} catch {
					// Network failure: fall through to the offline shell.
				}
				const cache = await deps.caches.open(deps.cacheName);
				const shell = await cache.match(new URL(SHELL_PATH, deps.origin).toString());
				return shell ? markOfflineShell(shell) : offlineResponse();
			})();
	}
}

/** Message the page sends when the user accepts an update. */
export const SKIP_WAITING = 'SKIP_WAITING';

export function isSkipWaitingMessage(data: unknown): boolean {
	return (
		typeof data === 'object' &&
		data !== null &&
		(data as { type?: unknown }).type === SKIP_WAITING
	);
}
