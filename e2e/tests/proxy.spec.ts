// Deployment topology through each example reverse proxy (#27). Runs once
// per proxy project (caddy, traefik, nginx; see playwright.config.ts)
// against the manager built with the e2e test routes
// (internal/manager/server/e2e_routes.go), with the proxies' configuration
// files from deploy/.
import { expect, test } from '@playwright/test';
import { sseTimeline, wsIdleRoundTrip } from '../helpers/streams.js';

interface RequestInfo {
	clientIp: string;
	peer: string;
	trustedPeer: boolean;
	scheme: string;
	host: string;
	requestId: string;
	cookiePresent: boolean;
	forwardedHeaders: string[];
	secureOrigin: string;
	httpVersion: string;
}

/** Longer than the 60 s idle/read timeouts of nginx and the Caddy example. */
const IDLE_MS = 70_000;
/** The manager's default heartbeat / ping interval (DOCKYARD_STREAM_HEARTBEAT). */
const HEARTBEAT_MS = 15_000;

/** Each project's manager reached directly, not through its proxy (e2e/compose.yaml). */
const DIRECT_URL: Record<string, string> = {
	caddy: 'http://127.0.0.1:18080',
	traefik: 'http://127.0.0.1:18081',
	nginx: 'http://127.0.0.1:18082'
};

test.describe('one HTTPS origin', () => {
	test('serves the PWA shell and the API over HTTP/2', async ({ page, baseURL }) => {
		const res = await page.goto('/');
		expect(res?.status()).toBe(200);
		expect(page.url()).toMatch(/^https:\/\//);
		await expect(page.getByRole('heading', { name: 'DockYard' })).toBeVisible();
		const protocol = await page.evaluate(
			() =>
				(performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming)
					.nextHopProtocol
		);
		expect(protocol).toBe('h2');

		const api = await page.evaluate(async () => {
			const r = await fetch('/api/v1/health');
			return {
				status: r.status,
				cacheControl: r.headers.get('cache-control'),
				body: await r.json()
			};
		});
		expect(api.status).toBe(200);
		expect(api.cacheControl).toBe('no-store');
		expect(api.body.status).toBe('ok');

		// The manager sees the public origin: https via a trusted proxy, the
		// browser's host, so first-run setup would be allowed (#16).
		const info = await page.evaluate(async () =>
			(await fetch('/api/v1/__e2e/request-info')).json()
		);
		expect(info).toMatchObject({
			trustedPeer: true,
			scheme: 'https',
			host: new URL(baseURL!).host,
			secureOrigin: 'ok',
			forwardedHeaders: []
		});
	});
});

test.describe('forwarded headers', () => {
	test('spoofed X-Forwarded-* from the client are ignored', async ({ request, baseURL }) => {
		const plain = (await (
			await request.get('/api/v1/__e2e/request-info')
		).json()) as RequestInfo;
		expect(plain.trustedPeer).toBe(true);
		expect(plain.scheme).toBe('https');
		expect(plain.clientIp).not.toBe(plain.peer); // the client, not the proxy

		const res = await request.get('/api/v1/__e2e/request-info', {
			headers: {
				'X-Forwarded-For': '203.0.113.9',
				'X-Forwarded-Proto': 'http',
				'X-Forwarded-Host': 'evil.example',
				Forwarded: 'for=203.0.113.9;proto=http'
			}
		});
		const spoofed = (await res.json()) as RequestInfo;
		expect(spoofed.clientIp).toBe(plain.clientIp);
		expect(spoofed.clientIp).not.toBe('203.0.113.9');
		expect(spoofed.scheme).toBe('https');
		expect(spoofed.host).toBe(new URL(baseURL!).host);
		expect(spoofed.secureOrigin).toBe('ok');
		expect(spoofed.forwardedHeaders).toEqual([]);
	});

	test('an untrusted peer cannot claim HTTPS or another client IP', async ({
		request
	}, testInfo) => {
		const direct = DIRECT_URL[testInfo.project.name];
		test.skip(!direct, 'E2E_BASE_URL runs have no direct manager port');
		const res = await request.get(`${direct}/api/v1/__e2e/request-info`, {
			headers: {
				'X-Forwarded-For': '203.0.113.9',
				'X-Forwarded-Proto': 'https',
				'X-Forwarded-Host': 'localhost:8443',
				'X-Request-ID': 'spoofed-request-id'
			}
		});
		const info = (await res.json()) as RequestInfo;
		expect(info.trustedPeer).toBe(false);
		expect(info.clientIp).toBe(info.peer);
		expect(info.clientIp).not.toBe('203.0.113.9');
		expect(info.scheme).toBe('http');
		expect(info.host).toBe(new URL(direct).host);
		expect(info.requestId).not.toBe('spoofed-request-id');
		expect(res.headers()['x-request-id']).toBe(info.requestId);
		// First-run setup would refuse this request (#16).
		expect(info.secureOrigin).toBe('request_not_https');
	});
});

test.describe('credential separation', () => {
	test('agent credentials cannot call the public API', async ({ request }) => {
		for (const token of ['dya_e2e-fake-agent-credential', 'dye_e2e-fake-enrollment-token']) {
			const res = await request.get('/api/v1/health', {
				headers: { Authorization: `Bearer ${token}` }
			});
			expect(res.status()).toBe(401);
			expect(((await res.json()) as { code: string }).code).toBe('unauthenticated');
		}
		expect((await request.get('/api/v1/health')).status()).toBe(200);
	});

	test('browser cookies never reach agent routes; other bearer tokens are refused', async ({
		page,
		request
	}) => {
		await page.goto('/');
		await page.evaluate(() => {
			document.cookie = 'dockyard_session=e2e-fake-session; Path=/; Secure; SameSite=Strict';
		});
		const seen = await page.evaluate(async () => ({
			api: await (await fetch('/api/v1/__e2e/request-info')).json(),
			agent: await (await fetch('/agent/v1/__e2e/request-info')).json()
		}));
		expect(seen.api.cookiePresent).toBe(true); // the browser did send it
		expect(seen.agent.cookiePresent).toBe(false);

		const res = await request.get('/agent/v1/__e2e/request-info', {
			headers: { Authorization: 'Bearer dyt_e2e-fake-api-token' }
		});
		expect(res.status()).toBe(401);
		expect(await res.json()).toMatchObject({
			code: 'unauthenticated',
			message: 'agent authentication failed'
		});
	});
});

test.describe('streams stay open through the proxy', () => {
	test('SSE survives idle beyond the proxy timeout, unbuffered, with heartbeats', async ({
		page
	}) => {
		test.setTimeout(IDLE_MS + 60_000);
		await page.goto('/');
		const r = await sseTimeline(
			page,
			`/api/v1/__e2e/sse?idle_ms=${IDLE_MS}&n=2&interval_ms=100`,
			{
				until: 'done',
				timeoutMs: IDLE_MS + 40_000
			}
		);
		expect(r.status).toBe(200);
		expect(r.contentType).toBe('text/event-stream');
		expect(r.cacheControl).toBe('no-store');

		const at = (line: string) => r.lines.find((l) => l.line === line)?.t ?? -1;
		// Delivered as written: "ready" at once, heartbeats while idle.
		expect(at('event: ready')).toBeGreaterThanOrEqual(0);
		expect(at('event: ready')).toBeLessThan(5_000);
		const heartbeats = r.lines.filter((l) => l.line === ': heartbeat').map((l) => l.t);
		expect(heartbeats.length).toBeGreaterThanOrEqual(Math.floor(IDLE_MS / HEARTBEAT_MS) - 1);
		expect(heartbeats[0]).toBeLessThan(2 * HEARTBEAT_MS);
		expect(heartbeats[heartbeats.length - 1] - heartbeats[0]).toBeGreaterThan(2 * HEARTBEAT_MS);
		// The stream outlived the idle period and finished normally.
		expect(at('event: done')).toBeGreaterThan(IDLE_MS - 1_000);
		expect(r.lines.filter((l) => l.line === 'event: tick')).toHaveLength(2);
	});

	test('WebSocket under /agent/v1 survives idle beyond the proxy timeout', async ({ page }) => {
		test.setTimeout(IDLE_MS + 60_000);
		await page.goto('/');
		await page.evaluate(() => {
			document.cookie = 'dockyard_session=e2e-fake-session; Path=/; Secure; SameSite=Strict';
		});
		const r = await wsIdleRoundTrip(page, '/agent/v1/__e2e/echo?hello=1', {
			idleMs: IDLE_MS,
			protocols: ['dockyard.e2e']
		});
		expect(r.protocol).toBe('dockyard.e2e');
		const hello = JSON.parse(r.messages[0]) as RequestInfo;
		expect(hello.cookiePresent).toBe(false);
		expect(hello.trustedPeer).toBe(true);
		expect(hello.scheme).toBe('https');
		expect(r.messages.slice(1)).toEqual(['echo:before-idle', 'echo:after-idle']);
	});
});
