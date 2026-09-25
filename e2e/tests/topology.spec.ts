// Deployment topology (#27) behind each example reverse proxy: DockYard's
// own passkey enrollment and passkey sign-in (#16) with Chromium's virtual
// authenticator, PWA installability as Chromium judges it (#11, #23), and
// an agent enrollment created in the UI whose one-use token a real agent
// then enrolls with through the same proxy (test/proxy
// TestTLSProxyAgentSessions reads it from E2E_ENROLLMENT_TOKEN_DIR; the
// exec WebSocket, live-stream resume and agent-session reconnects through
// each proxy are checked there, with that agent).
//
// Locally against the Docker-free devstack (WebAuthn and service workers
// work on http://localhost, a secure context):
//
//   npm --prefix web run build && go run ./test/devstack -addr 127.0.0.1:8080
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/topology.spec.ts
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test, type Page } from '@playwright/test';
import { waitForServiceWorkerControl } from '../helpers/pwa.js';
import { addVirtualAuthenticator } from '../helpers/webauthn.js';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const tokenDir = process.env.E2E_ENROLLMENT_TOKEN_DIR ?? '';

async function signIn(page: Page) {
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
	await expect(page.getByRole('main')).toBeVisible();
}

test.describe
	.serial('Topology (#27): passkeys, PWA install and agent enrollment through the proxy', () => {
	test.beforeAll(async ({ request }) => {
		// ui.spec.ts completes first-run setup on a fresh manager; wait for it
		// (or do it here when this file runs alone).
		for (let i = 0; i < 30; i++) {
			const s = await (await request.get('/api/v1/setup/status')).json();
			if (s.setupComplete) return;
			await new Promise((r) => setTimeout(r, 2000));
		}
		const res = await request.post('/api/v1/setup/owner', {
			data: { username: owner, displayName: 'Homelab Owner', password }
		});
		expect([200, 201, 409]).toContain(res.status());
	});

	test('passkey: enroll one in the profile, then sign in with it', async ({
		page,
		baseURL
	}, testInfo) => {
		await page.goto('/sign-in');
		const auth = await addVirtualAuthenticator(page);
		await signIn(page);

		// Enrollment (#16): registration options, the browser ceremony and
		// the verification all go through the proxy.
		const name = `E2E ${testInfo.project.name} ${Date.now()}`;
		await page.goto('/settings/security');
		await page.getByLabel('Name of the new passkey').fill(name);
		await page.getByRole('button', { name: 'Add passkey' }).click();
		await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(
			`Added the passkey ${name}`
		);
		await expect(page.getByRole('table', { name: 'Your passkeys' })).toContainText(name);
		const creds = await auth.credentials();
		expect(creds.length).toBeGreaterThan(0);
		// The relying party is DOCKYARD_PUBLIC_URL's host, the proxy origin.
		expect(creds.every((c) => c.rpId === new URL(baseURL ?? page.url()).hostname)).toBe(true);
		expect(creds.every((c) => c.isResidentCredential)).toBe(true);

		// Username-less passkey sign-in on the same origin.
		await page.getByRole('button', { name: /^Account menu for/ }).click();
		await page.getByRole('menuitem', { name: 'Sign out' }).click();
		await expect(page).toHaveURL(/\/sign-in/);
		await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
		await expect(page).not.toHaveURL(/\/sign-in/);
		await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
		const me = await (await page.request.get('/api/v1/me')).json();
		expect(me.username ?? me.user?.username).toBe(owner);

		// A failed user verification signs nobody in (the browser reports it
		// like a cancelled prompt, so the page stays as it was).
		await page.getByRole('button', { name: /^Account menu for/ }).click();
		await page.getByRole('menuitem', { name: 'Sign out' }).click();
		await expect(page).toHaveURL(/\/sign-in/);
		await auth.setUserVerified(false);
		const passkeyButton = page.getByRole('button', { name: 'Sign in with a passkey' });
		await passkeyButton.click();
		await expect(passkeyButton).toBeEnabled();
		await expect(page).toHaveURL(/\/sign-in/);
		expect((await page.request.get('/api/v1/me')).status()).toBe(401);
		await auth.remove();
	});

	test('PWA: Chromium finds the app installable on the proxy origin', async ({
		page,
		baseURL
	}) => {
		await page.goto('/sign-in');
		await waitForServiceWorkerControl(page);
		const cdp = await page.context().newCDPSession(page);
		const manifest = (await cdp.send('Page.getAppManifest')) as {
			url: string;
			errors: { message: string }[];
		};
		expect(manifest.errors).toEqual([]);
		expect(manifest.url.startsWith(new URL(baseURL ?? page.url()).origin + '/')).toBe(true);
		const { installabilityErrors } = (await cdp.send('Page.getInstallabilityErrors')) as {
			installabilityErrors: { errorId: string }[];
		};
		expect(installabilityErrors.map((e) => e.errorId)).toEqual([]);
		await cdp.detach();
	});

	test('agent enrollment: the UI hands out a token for the public origin', async ({
		page,
		baseURL
	}, testInfo) => {
		await signIn(page);
		const origin = new URL(baseURL ?? page.url()).origin;
		const environmentName = `e2e-agent-${testInfo.project.name}`;
		await page.goto('/environments/add');
		await page.getByLabel('Environment name').fill(environmentName);
		await page.getByRole('button', { name: 'Create enrollment token' }).click();
		await expect(page.getByText('Waiting for the agent to connect.')).toBeVisible();
		const tokenBox = page.getByLabel('enrollment token', { exact: true });
		await expect(tokenBox).toContainText(/^dye_/);
		const token = ((await tokenBox.textContent()) ?? '').trim();
		// Remote agents dial the one public origin: the proxy's.
		await expect(page.getByLabel('Agent on another Docker host: command')).toContainText(
			`DOCKYARD_MANAGER_URL='${origin}'`
		);
		if (tokenDir) {
			mkdirSync(tokenDir, { recursive: true });
			writeFileSync(
				join(tokenDir, `${testInfo.project.name}.token`),
				JSON.stringify({ token, environmentName }),
				{ mode: 0o600 }
			);
		}
	});
});
