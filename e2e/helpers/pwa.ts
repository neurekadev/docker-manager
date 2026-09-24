// PWA checks: web app manifest and service worker (#23, #29).
import type { APIRequestContext, Page } from '@playwright/test';

export interface ManifestCheck {
	href: string | null;
	manifest: Record<string, unknown> | null;
	problems: string[];
}

/**
 * Finds <link rel="manifest">, fetches it through the page's origin and
 * checks the installability basics: name, start_url, display, and a
 * 192px and 512px icon. `href` is null when the page links no manifest.
 */
export async function checkManifest(page: Page, request: APIRequestContext): Promise<ManifestCheck> {
	const href = await page
		.locator('link[rel="manifest"]')
		.first()
		.getAttribute('href', { timeout: 1000 })
		.catch(() => null);
	if (!href) return { href: null, manifest: null, problems: ['no <link rel="manifest">'] };
	const url = new URL(href, page.url()).toString();
	const res = await request.get(url);
	const problems: string[] = [];
	if (!res.ok()) return { href, manifest: null, problems: [`manifest HTTP ${res.status()}`] };
	const manifest = (await res.json()) as Record<string, unknown>;
	for (const key of ['name', 'start_url', 'display']) {
		if (!manifest[key]) problems.push(`manifest lacks ${key}`);
	}
	const icons = (manifest.icons as { sizes?: string }[] | undefined) ?? [];
	for (const size of ['192x192', '512x512']) {
		if (!icons.some((i) => i.sizes?.split(/\s+/).includes(size))) problems.push(`no ${size} icon`);
	}
	return { href, manifest, problems };
}

export interface ServiceWorkerCheck {
	registered: boolean;
	scope: string | null;
	scriptURL: string | null;
	controlling: boolean;
}

/** Waits (up to timeoutMs) for a service worker registration for the page. */
export async function checkServiceWorker(page: Page, timeoutMs = 10_000): Promise<ServiceWorkerCheck> {
	return page.evaluate(async (timeoutMs) => {
		if (!('serviceWorker' in navigator)) {
			return { registered: false, scope: null, scriptURL: null, controlling: false };
		}
		const reg = await Promise.race([
			navigator.serviceWorker.ready,
			new Promise<null>((r) => setTimeout(() => r(null), timeoutMs))
		]);
		return {
			registered: !!reg,
			scope: reg ? reg.scope : null,
			scriptURL: reg?.active?.scriptURL ?? null,
			controlling: !!navigator.serviceWorker.controller
		};
	}, timeoutMs);
}

/**
 * Lists cache storage entries whose URL path starts with one of prefixes;
 * API data (/api/, /agent/) must never be cached by the service worker.
 */
export async function cachedUrls(page: Page, prefixes = ['/api/', '/agent/']): Promise<string[]> {
	return page.evaluate(async (prefixes) => {
		if (!('caches' in self)) return [];
		const hits: string[] = [];
		for (const name of await caches.keys()) {
			const cache = await caches.open(name);
			for (const req of await cache.keys()) {
				const path = new URL(req.url).pathname;
				if (prefixes.some((p) => path.startsWith(p))) hits.push(req.url);
			}
		}
		return hits;
	}, prefixes);
}
