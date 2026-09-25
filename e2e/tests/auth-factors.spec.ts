// Sign-in factors in the browser (#16, #29 "TOTP from a known seed",
// "Chromium virtual WebAuthn authenticator"; #12 V14/V22): the owner sets
// up an authenticator app from the secret the profile screen shows, is
// then asked for its code at sign-in, finishes a sign-in once with a
// recovery code (a used code is refused), adds a passkey with a virtual
// authenticator and signs in with it alone.
//
// It changes the owner's factors, so it never runs next to other specs on
// a shared manager: it is skipped unless E2E_FACTORS=1. scripts/ci/
// e2e-devstack.sh sets it on a fresh devstack (group "auth-factors").
// Passkeys need a secure context: https, or http://localhost (the devstack).
//
//   E2E_FACTORS=1 E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/auth-factors.spec.ts
import { expect, test, type Page } from '@playwright/test';
import { totp } from '../helpers/totp.js';
import { addVirtualAuthenticator } from '../helpers/webauthn.js';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const STEP_MS = 30_000;

let seed = '';
let lastStep = -1;
let recoveryCodes: string[] = [];

/**
 * A code the server accepts now: its time step is within one step of the
 * current one (the server's skew window) and after the last step it
 * accepted (replay protection). Waits for the next step when needed.
 */
async function freshCode(page: Page): Promise<string> {
	const now = () => Math.floor(Date.now() / STEP_MS);
	const step = Math.max(now() - 1, lastStep + 1);
	while (step > now() + 1) await page.waitForTimeout(1_000);
	lastStep = step;
	return totp(seed, { timeMs: step * STEP_MS });
}

async function passwordStep(page: Page) {
	await page.context().clearCookies();
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
}

async function signedIn(page: Page) {
	await expect(page).not.toHaveURL(/\/sign-in/);
	await expect(page.getByRole('main')).toBeVisible();
	expect((await page.request.get('/api/v1/me')).status()).toBe(200);
}

async function signInWithCode(page: Page) {
	await passwordStep(page);
	await page.getByLabel('Authenticator code').fill(await freshCode(page));
	await page.getByRole('button', { name: 'Verify' }).click();
	await signedIn(page);
}

/** Answers the step-up dialog if the server asked for one. */
async function confirmIdentity(page: Page) {
	const dialog = page.getByRole('dialog', { name: "Confirm it's you" });
	if (!(await dialog.isVisible().catch(() => false))) return;
	await dialog.getByLabel('Password').fill(password);
	const code = dialog.getByLabel('Authenticator code');
	if (await code.count()) await code.fill(await freshCode(page));
	await dialog.getByRole('button', { name: 'Confirm with password' }).click();
}

test.describe.serial('sign-in factors (#16)', () => {
	test.skip(
		process.env.E2E_FACTORS !== '1',
		'changes the owner account: set E2E_FACTORS=1 on a dedicated manager'
	);
	// Real TOTP time steps: a test may wait up to one 30 s step.
	test.describe.configure({ timeout: 120_000 });

	test('authenticator app: set up from the shown secret, then asked for at sign-in', async ({
		page
	}) => {
		await passwordStep(page);
		await signedIn(page);
		await page.goto('/settings/security');
		await expect(
			page.getByRole('heading', { level: 1, name: 'Profile and security' })
		).toBeVisible();
		// The card's button opens the setup, whose own button starts it.
		await page.getByRole('button', { name: 'Set up authenticator app' }).click();
		await page.getByRole('button', { name: 'Set up authenticator app' }).click();
		await expect(
			page.getByRole('img', { name: 'QR code of the authenticator setup' })
		).toBeVisible();
		seed = (await page.locator('.secret .mono').innerText()).trim();
		expect(seed).toMatch(/^[A-Z2-7]{16,}=*$/);
		await page.getByLabel('Code from the app').fill(await freshCode(page));
		await page.getByRole('button', { name: 'Turn on', exact: true }).click();
		await expect(page.getByText('Turned on the authenticator app')).toBeVisible();
		await expect(
			page.getByRole('button', { name: 'Turn off authenticator app' })
		).toBeVisible();

		// A password alone no longer signs in; a wrong code is refused.
		await passwordStep(page);
		await expect(page.getByLabel('Authenticator code')).toBeVisible();
		expect((await page.request.get('/api/v1/me')).status()).toBe(401);
		const valid = new Set(
			[-1, 0, 1].map((d) => totp(seed, { timeMs: Date.now() + d * STEP_MS }))
		);
		await page
			.getByLabel('Authenticator code')
			.fill(['000000', '111111', '222222'].find((c) => !valid.has(c))!);
		await page.getByRole('button', { name: 'Verify' }).click();
		await expect(page.getByText('That code did not work.', { exact: false })).toBeVisible();
		expect((await page.request.get('/api/v1/me')).status()).toBe(401);
		await page.getByLabel('Authenticator code').fill(await freshCode(page));
		await page.getByRole('button', { name: 'Verify' }).click();
		await signedIn(page);
	});

	test('a recovery code finishes a password sign-in once', async ({ page }) => {
		await signInWithCode(page);
		await page.goto('/settings/security');
		await page.getByRole('button', { name: 'Generate new codes' }).click();
		await page
			.getByRole('alertdialog')
			.getByRole('button', { name: 'Generate new codes' })
			.click();
		await confirmIdentity(page);
		const dialog = page.getByRole('dialog', { name: 'Your new recovery codes' });
		const list = dialog.getByLabel('recovery codes', { exact: true });
		await expect(list).toBeVisible();
		recoveryCodes = (await list.innerText()).split(/\s+/).filter(Boolean);
		expect(recoveryCodes).toHaveLength(10);
		await dialog.getByRole('checkbox', { name: /I stored the recovery codes/ }).check();
		await dialog.getByRole('button', { name: 'Done' }).click();

		for (const use of ['first', 'second'] as const) {
			await passwordStep(page);
			await page.getByRole('button', { name: 'Use a recovery code' }).click();
			await page.getByLabel('Recovery code').fill(recoveryCodes[0]);
			await page.getByRole('button', { name: 'Use recovery code' }).click();
			if (use === 'first') {
				await signedIn(page);
			} else {
				await expect(
					page.getByText('That recovery code did not work.', { exact: false })
				).toBeVisible();
				expect((await page.request.get('/api/v1/me')).status()).toBe(401);
			}
		}
	});

	test('passkey: add one with a virtual authenticator, then sign in with it alone', async ({
		page
	}) => {
		const auth = await addVirtualAuthenticator(page);
		await signInWithCode(page);
		await page.goto('/settings/security');
		await page.getByLabel('Name of the new passkey').fill('E2E authenticator');
		await page.getByRole('button', { name: 'Add passkey' }).click();
		await confirmIdentity(page);
		await expect(page.getByText('Added the passkey E2E authenticator')).toBeVisible();
		expect(await auth.credentials()).toHaveLength(1);

		// Username-less passkey sign-in: no password, no code.
		await page.context().clearCookies();
		await page.goto('/sign-in');
		await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
		await signedIn(page);
		expect((await (await page.request.get('/api/v1/auth/session')).json()).state).toBe(
			'authenticated'
		);
		await auth.remove();
	});
});
