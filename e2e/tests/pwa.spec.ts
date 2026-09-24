// PWA shell behaviour through the TLS proxy (#11): installable manifest,
// service worker registration and scope, deep-link reloads, API responses
// never cached, the offline shell, and lazily loaded UI libraries.
import { expect, test, type Page, type Response } from '@playwright/test';
import {
	cacheContents,
	cachedUrls,
	checkManifest,
	checkServiceWorker,
	waitForServiceWorkerControl
} from '../helpers/pwa.js';

/** Collects every /api/ and /agent/ response the page receives. */
function recordApiResponses(page: Page): Response[] {
	const seen: Response[] = [];
	page.on('response', (r) => {
		const path = new URL(r.url()).pathname;
		if (path.startsWith('/api/') || path.startsWith('/agent/')) seen.push(r);
	});
	return seen;
}

test.describe('PWA shell', () => {
	test('manifest is valid and served with the right headers', async ({ page, request }) => {
		await page.goto('/');
		const m = await checkManifest(page, request);
		expect(m.problems).toEqual([]);
		expect(m.manifest).toMatchObject({
			id: '/',
			name: 'DockYard',
			short_name: 'DockYard',
			start_url: '/',
			scope: '/',
			display: 'standalone'
		});

		const res = await request.get('/manifest.webmanifest');
		expect(res.headers()['content-type']).toBe('application/manifest+json');
		expect(res.headers()['cache-control']).toBe('no-cache');

		const icons = (m.manifest!.icons as { src: string; sizes: string; purpose?: string }[]) ?? [];
		expect(icons.some((i) => i.purpose === 'maskable')).toBe(true);
		for (const icon of icons) {
			const r = await request.get(icon.src);
			expect(r.status(), icon.src).toBe(200);
			expect(r.headers()['content-type'], icon.src).toBe('image/png');
		}
		await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', /^#/);
	});

	test('service worker registers at the root scope under the TLS proxy', async ({
		page,
		request,
		baseURL
	}) => {
		await page.goto('/');
		const sw = await checkServiceWorker(page);
		expect(sw.registered).toBe(true);
		expect(sw.scope).toBe(new URL('/', baseURL).toString());
		expect(sw.scriptURL).toBe(new URL('/service-worker.js', baseURL).toString());

		// The worker claims the page once its precache is complete.
		await waitForServiceWorkerControl(page);

		const script = await request.get('/service-worker.js', {
			headers: { 'Service-Worker': 'script' }
		});
		expect(script.status()).toBe(200);
		expect(script.headers()['content-type']).toBe('text/javascript; charset=utf-8');
		expect(script.headers()['cache-control']).toBe('no-cache');
		expect(script.headers()['service-worker-allowed']).toBeUndefined();

		// A fresh install is not an update: no reload prompt.
		await expect(page.getByRole('button', { name: 'Reload to update' })).toHaveCount(0);

		// Service workers need a secure context: this ran through the TLS proxy
		// (checked last so local plain-HTTP localhost runs exercise the rest).
		expect(page.url()).toMatch(/^https:\/\//);
	});

	test('deep links reload into the app shell, online and offline', async ({ page, context }) => {
		const first = await page.goto('/stacks/demo/files?path=compose.yaml');
		expect(first?.status()).toBe(200);
		await waitForServiceWorkerControl(page);

		const online = await page.reload();
		expect(online?.status()).toBe(200);
		expect(online?.fromServiceWorker()).toBe(true); // network-first through the worker
		expect(online?.headers()['x-dockyard-shell']).toBeUndefined();
		// The SPA boots; its router renders the route or its not-found view.
		await expect(page).toHaveTitle('DockYard');
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
		await expect(page.getByRole('status', { name: 'Connection status' })).toBeAttached();
		expect(new URL(page.url()).pathname).toBe('/stacks/demo/files');

		await context.setOffline(true);
		const offline = await page.reload();
		expect(offline?.status()).toBe(200);
		expect(offline?.headers()['x-dockyard-shell']).toBe('offline');
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
		await expect(page.getByTestId('connection-status')).toHaveAttribute('data-state', 'offline');
		expect(new URL(page.url()).pathname).toBe('/stacks/demo/files');
		await context.setOffline(false);
	});

	test('API responses are never served from or stored in Cache Storage', async ({ page }) => {
		const api = recordApiResponses(page);
		await page.goto('/');
		await waitForServiceWorkerControl(page);
		await page.reload();
		await expect(page.getByText(/^Manager ok/)).toBeVisible();

		// More API traffic from the controlled page, including a 404 and a
		// non-GET request.
		const statuses = await page.evaluate(async () => {
			const out: number[] = [];
			for (const [url, init] of [
				['/api/v1/health', {}],
				['/api/v1/openapi.json', {}],
				['/api/v1/does-not-exist', {}],
				['/api/v1/health', { method: 'POST' }],
				['/agent/v1/session', {}]
			] as [string, RequestInit][]) {
				out.push((await fetch(url, init)).status);
			}
			return out;
		});
		expect(statuses).toEqual([200, 200, 404, 405, 404]);

		expect(api.length).toBeGreaterThan(0);
		for (const r of api) {
			expect(r.fromServiceWorker(), r.url()).toBe(false);
			expect(r.headers()['cache-control'], r.url()).toBe('no-store');
		}
		expect(await cachedUrls(page)).toEqual([]);

		// Positive control: the precache holds the shell and versioned assets only.
		const contents = await cacheContents(page);
		const names = Object.keys(contents);
		expect(names).toHaveLength(1);
		expect(names[0]).toMatch(/^dockyard-precache-/);
		const paths = contents[names[0]];
		expect(paths).toContain('/index.html');
		expect(paths).toContain('/manifest.webmanifest');
		expect(paths.some((p) => p.startsWith('/_app/immutable/'))).toBe(true);
		for (const p of paths) {
			expect(p, p).toMatch(
				/^\/(index\.html|manifest\.webmanifest|favicon\.ico|icons\/[\w.-]+|_app\/immutable\/.+)$/
			);
		}
	});

	test('offline shell shows when the network is cut', async ({ page, context }) => {
		await page.goto('/');
		await waitForServiceWorkerControl(page);
		await expect(page.getByTestId('connection-status')).toHaveAttribute('data-state', 'online');

		await context.setOffline(true);
		// The indicator reacts without a reload...
		await expect(page.getByTestId('connection-status')).toHaveAttribute('data-state', 'offline');
		await expect(page.getByRole('status', { name: 'Connection status' })).toContainText('Offline');

		// ...and a reload while offline boots the precached shell.
		const res = await page.reload();
		expect(res?.status()).toBe(200);
		expect(res?.fromServiceWorker()).toBe(true);
		expect(res?.headers()['x-dockyard-shell']).toBe('offline');
		await expect(page.getByRole('heading', { level: 1, name: 'DockYard' })).toBeVisible();
		await expect(page.getByTestId('connection-status')).toHaveAttribute('data-state', 'offline');
		await expect(page.getByText('Waiting for the network…')).toBeVisible();
		expect(await cachedUrls(page)).toEqual([]);

		// Back online: the indicator clears and the typed API call succeeds.
		await context.setOffline(false);
		await expect(page.getByTestId('connection-status')).toHaveAttribute('data-state', 'online');
		await expect(page.getByText(/^Manager ok/)).toBeVisible();
	});
});

test.describe('lazy-loaded libraries (#11 proof page)', () => {
	test('CodeMirror, ECharts and xterm.js load on demand; Bits UI menu works', async ({
		page
	}) => {
		const scripts = new Set<string>();
		page.on('request', (r) => {
			if (r.resourceType() === 'script') scripts.add(new URL(r.url()).pathname);
		});
		await page.goto('/lazy-proof');
		await expect(page.getByTestId('loaded')).toHaveText('Loaded: none');
		await expect(page.locator('.cm-editor, .xterm, [data-testid="chart"] canvas')).toHaveCount(0);

		const before = scripts.size;
		await page.getByRole('button', { name: 'Load editor' }).click();
		await expect(page.locator('[data-testid="editor"] .cm-editor')).toBeVisible();
		await expect(page.locator('.cm-content')).toContainText('image: nginx');
		expect(scripts.size).toBeGreaterThan(before);

		await page.getByRole('button', { name: 'Load chart' }).click();
		await expect(page.locator('[data-testid="chart"] canvas').first()).toBeVisible();

		await page.getByRole('button', { name: 'Load terminal' }).click();
		await expect(page.locator('[data-testid="terminal"] .xterm')).toBeVisible();
		await expect(page.getByTestId('loaded')).toHaveText('Loaded: editor, chart, terminal');

		await page.getByTestId('context-target').click({ button: 'right' });
		await page.getByRole('menuitem', { name: 'Rename' }).click();
		await expect(page.getByTestId('selected')).toHaveText('Selected: Rename');
	});
});
