// Smoke: the embedded UI shell loads through the TLS proxy and renders the
// manager health result (#11, #27, #29).
import { expect, test } from '@playwright/test';

test('UI shell renders the health result through the TLS proxy', async ({ page, baseURL }) => {
	const health = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/v1/health');
	const res = await page.goto('/');
	expect(res?.status()).toBe(200);

	// Browser hardening headers survive the proxy.
	const headers = res!.headers();
	expect(headers['content-security-policy']).toContain("default-src 'self'");
	expect(headers['x-frame-options']).toBe('DENY');

	await expect(page.getByRole('heading', { name: 'DockYard' })).toBeVisible();
	const api = await health;
	expect(api.status()).toBe(200);
	expect(api.headers()['cache-control']).toBe('no-store');
	const body = (await api.json()) as { status: string; version: string; commit: string };
	expect(body.status).toBe('ok');

	// The page shows exactly what the API returned.
	await expect(page.getByText(`Manager ok · version ${body.version} (${body.commit})`)).toBeVisible();
	await expect(page.getByRole('alert')).toHaveCount(0);
	// Served over TLS by the proxy (checked last so local HTTP runs still
	// exercise everything above).
	expect(page.url()).toMatch(/^https:\/\//);
	expect(new URL(baseURL!).protocol).toBe('https:');
});

test('readiness is green through the proxy', async ({ request }) => {
	const res = await request.get('/api/v1/health/ready');
	expect(res.status()).toBe(200);
	const body = (await res.json()) as { status: string; checks: { name: string; ok: boolean }[] };
	expect(body.status).toBe('ready');
	expect(body.checks.length).toBeGreaterThan(0);
	for (const c of body.checks) expect(c.ok, c.name).toBe(true);
});

test('deep links fall back to the UI shell', async ({ page }) => {
	const res = await page.goto('/environments/does-not-exist');
	expect(res?.status()).toBe(200);
	expect(res?.headers()['content-type']).toContain('text/html');
	// The SPA boots (its router then renders the page or its not-found view).
	await expect(page).toHaveTitle('DockYard');
	await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
});
