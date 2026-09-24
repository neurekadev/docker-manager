// Self-tests of the E2E helpers (#29), run through the TLS proxy so later
// suites (#16 passkeys/TOTP, #23 live updates/PWA, #27 proxy) can rely on
// them.
import { expect, test } from '@playwright/test';
import { cachedUrls, checkManifest, checkServiceWorker } from '../helpers/pwa.js';
import { collectSse, wsRoundTrip } from '../helpers/streams.js';
import { base32Decode, msUntilNextStep, totp } from '../helpers/totp.js';
import { addVirtualAuthenticator } from '../helpers/webauthn.js';

test.describe('TOTP helper', () => {
	// RFC 6238 Appendix B (SHA-1, 8 digits, seed "12345678901234567890").
	const seed = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ';
	const vectors: [number, string][] = [
		[59, '94287082'],
		[1111111109, '07081804'],
		[1111111111, '14050471'],
		[1234567890, '89005924'],
		[2000000000, '69279037'],
		[20000000000, '65353130']
	];
	for (const [t, want] of vectors) {
		test(`RFC 6238 vector T=${t}`, () => {
			expect(totp(seed, { timeMs: t * 1000, digits: 8 })).toBe(want);
		});
	}
	test('base32 and step helpers', () => {
		expect(base32Decode('gezd gnbv====').toString()).toBe('12345');
		expect(() => base32Decode('1!')).toThrow(/invalid base32/);
		expect(totp(seed, { timeMs: 59_000 })).toHaveLength(6);
		expect(msUntilNextStep(61_000)).toBe(29_000);
	});
});

test.describe('WebAuthn virtual authenticator', () => {
	test('creates and asserts a passkey on the proxied origin', async ({ page }) => {
		await page.goto('/');
		const auth = await addVirtualAuthenticator(page);
		const created = await page.evaluate(async () => {
			const cred = (await navigator.credentials.create({
				publicKey: {
					rp: { name: 'DockYard E2E', id: location.hostname },
					user: { id: new Uint8Array(16).fill(7), name: 'e2e@dockyard.test', displayName: 'E2E' },
					challenge: crypto.getRandomValues(new Uint8Array(32)),
					pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
					authenticatorSelection: { residentKey: 'required', userVerification: 'required' }
				}
			})) as PublicKeyCredential;
			return { id: cred.id, type: cred.type };
		});
		expect(created.type).toBe('public-key');
		const stored = await auth.credentials();
		expect(stored).toHaveLength(1);
		expect(stored[0].isResidentCredential).toBe(true);
		expect(stored[0].rpId).toBe('localhost');

		const assertion = async () =>
			page.evaluate(async () => {
				try {
					const a = (await navigator.credentials.get({
						publicKey: {
							challenge: crypto.getRandomValues(new Uint8Array(32)),
							rpId: location.hostname,
							userVerification: 'required'
						}
					})) as PublicKeyCredential;
					return { ok: true, id: a.id };
				} catch (e) {
					return { ok: false, id: (e as Error).name };
				}
			});
		expect(await assertion()).toEqual({ ok: true, id: created.id });

		// A failed user verification is reported, not silently passed.
		await auth.setUserVerified(false);
		expect((await assertion()).ok).toBe(false);

		await auth.clear();
		expect(await auth.credentials()).toHaveLength(0);
		await auth.remove();
	});
});

test.describe('streams through the TLS proxy', () => {
	test('SSE events arrive incrementally', async ({ page }) => {
		await page.goto('/');
		const events = await collectSse(page, '/__e2e/echo/sse?n=3&interval_ms=50', {
			eventTypes: ['ready', 'tick', 'done'],
			untilType: 'done'
		});
		expect(events.map((e) => e.type)).toEqual(['ready', 'tick', 'tick', 'tick', 'done']);
		expect(events.filter((e) => e.type === 'tick').map((e) => e.data)).toEqual(['1', '2', '3']);
	});

	test('WebSocket round trip', async ({ page }) => {
		await page.goto('/');
		const { replies } = await wsRoundTrip(page, '/__e2e/echo/ws', ['hello', 'dockyard']);
		expect(replies).toEqual(['echo:hello', 'echo:dockyard']);
	});
});

test.describe('PWA', () => {
	test('service worker never caches API data', async ({ page }) => {
		await page.goto('/');
		await page.request.get('/api/v1/health');
		expect(await cachedUrls(page)).toEqual([]);
	});

	test('web app manifest and service worker (#23)', async ({ page, request }) => {
		await page.goto('/');
		const m = await checkManifest(page, request);
		// Pending until #23 ships the manifest: reported as skipped, never
		// as passed.
		test.skip(m.href === null, 'pending #23: the UI does not link a web app manifest yet');
		expect(m.problems).toEqual([]);
		const sw = await checkServiceWorker(page);
		expect(sw.registered).toBe(true);
	});
});
