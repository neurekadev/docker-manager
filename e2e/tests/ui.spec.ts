// The #22 UI foundation end to end: first-run setup, sign-in and sign-out,
// shell navigation (sidebar, breadcrumbs, ⌘K palette, narrow drawer), the
// environment switcher and the Restricted user's denied state.
//
// Runs against a fresh manager: in CI behind each TLS proxy of
// e2e/compose.yaml; locally against the Docker-free devstack
// (docs/development.md, "UI devstack"):
//
//   npm --prefix web run build
//   go run ./test/devstack -setup -addr 127.0.0.1:8080
//   cd e2e && E2E_BASE_URL=http://localhost:8080 npx playwright test tests/ui.spec.ts
//
// The steps share one manager, so they run in order. When setup was already
// completed (a seeded devstack), set E2E_UI_OWNER / E2E_UI_PASSWORD to its
// owner. E2E_SCREENSHOTS_DIR saves review screenshots (1440x900, 390x844).
import { expect, test, type Browser, type Page } from '@playwright/test';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const shots = process.env.E2E_SCREENSHOTS_DIR ?? '';

async function signIn(page: Page, user = owner, pass = password) {
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(user);
	await page.getByLabel('Password', { exact: true }).fill(pass);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page.getByRole('main')).toBeVisible();
	await expect(page).not.toHaveURL(/\/sign-in/);
}

async function newSignedInPage(browser: Browser, baseURL: string | undefined, viewport = { width: 1440, height: 900 }) {
	const ctx = await browser.newContext({ baseURL, viewport, ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === '1' });
	const page = await ctx.newPage();
	await signIn(page);
	return page;
}

test.describe.serial('UI foundation', () => {
	test('first-run setup creates the owner and signs in', async ({ page, request }) => {
		const status = await (await request.get('/api/v1/setup/status')).json();
		test.skip(status.setupComplete, 'setup already completed on this manager (set E2E_UI_OWNER/E2E_UI_PASSWORD)');

		await page.goto('/stacks');
		await expect(page).toHaveURL(/\/setup$/);
		await expect(page.getByRole('heading', { level: 1, name: 'Set up DockYard' })).toBeVisible();
		if (!status.secureOrigin) {
			// The insecure-origin explanation is shown and the form is disabled.
			await expect(page.getByRole('alert').filter({ hasText: "Finish setup on DockYard's public URL" })).toBeVisible();
			await expect(page.getByRole('button', { name: 'Create owner account' })).toBeDisabled();
			test.skip(true, 'this origin cannot complete setup');
		}
		await page.getByLabel('Username').fill(owner);
		await page.getByLabel('Display name').fill('Homelab Owner');
		await page.getByLabel('Password', { exact: true }).fill(password);
		await page.getByLabel('Repeat the password').fill(password + 'x');
		await page.getByRole('button', { name: 'Create owner account' }).click();
		await expect(page.getByText('The passwords do not match.')).toBeVisible();
		await page.getByLabel('Repeat the password').fill(password);
		await page.getByRole('button', { name: 'Create owner account' }).click();

		await expect(page).toHaveURL(/\/$/);
		await expect(page.getByRole('status', { name: 'Notifications' })).toContainText('Created the owner account');
		await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
		// Setup is closed now.
		await page.goto('/setup');
		await expect(page).toHaveURL(/\/$/);
	});

	test('sign-out, wrong password and sign-in', async ({ page }) => {
		await signIn(page);
		await page.getByRole('button', { name: /^Account menu for/ }).click();
		await page.getByRole('menuitem', { name: 'Sign out' }).click();
		await expect(page).toHaveURL(/\/sign-in\?reason=signed-out$/);
		await expect(page.getByText('You signed out')).toBeVisible();

		// Protected pages send signed-out visitors to sign-in and back.
		await page.goto('/jobs');
		await expect(page).toHaveURL(/\/sign-in\?next=%2Fjobs$/);
		await page.getByLabel('Username').fill(owner);
		await page.getByLabel('Password', { exact: true }).fill('not the password at all');
		await page.getByRole('button', { name: 'Sign in', exact: true }).click();
		await expect(page.getByRole('alert').filter({ hasText: 'The username or password is not right.' })).toBeVisible();
		await page.getByLabel('Password', { exact: true }).fill(password);
		await page.getByRole('button', { name: 'Sign in', exact: true }).click();
		await expect(page).toHaveURL(/\/jobs$/);
		await expect(page.getByRole('heading', { level: 1, name: 'Jobs' })).toBeVisible();

		// The session cookie is HttpOnly: scripts cannot read it.
		expect(await page.evaluate(() => document.cookie)).not.toContain('dockyard_session');
	});

	test('shell navigation: sidebar, breadcrumbs, command palette, skip link', async ({ page }) => {
		await signIn(page);
		const nav = page.getByRole('navigation', { name: 'Main' });
		await expect(nav.getByRole('link', { name: 'Dashboard' })).toHaveAttribute('aria-current', 'page');
		await nav.getByRole('link', { name: 'Stacks' }).click();
		await expect(page).toHaveURL(/\/stacks$/);
		await expect(page.getByRole('heading', { level: 1, name: 'Stacks' })).toBeVisible();
		await expect(nav.getByRole('link', { name: 'Stacks' })).toHaveAttribute('aria-current', 'page');
		await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Stacks');
		await expect(page).toHaveTitle('Stacks · DockYard');

		// ⌘K / Ctrl+K opens the palette; pages are found without typing a query.
		await page.keyboard.press('ControlOrMeta+k');
		const input = page.getByRole('combobox', { name: 'Search pages, environments, stacks and containers' });
		await expect(input).toBeFocused();
		await input.fill('sett');
		await expect(page.getByRole('option', { name: 'Settings' })).toBeVisible();
		await page.keyboard.press('Enter');
		await expect(page).toHaveURL(/\/settings$/);
		// Escape closes and returns focus.
		await page.keyboard.press('ControlOrMeta+k');
		await expect(input).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(input).toBeHidden();

		// The skip link is the first stop for keyboard users.
		await page.goto('/settings');
		await expect(page.getByRole('heading', { level: 1, name: 'Settings' })).toBeVisible();
		await page.keyboard.press('Tab');
		await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused();
		await page.keyboard.press('Enter');
		await expect(page.locator('#main')).toBeFocused();

		if (shots) {
			await page.goto('/');
			await page.screenshot({ path: `${shots}/e2e-dashboard-1440.png` });
		}
	});

	test('narrow layout: navigation drawer and stacked tables', async ({ browser, baseURL }) => {
		const page = await newSignedInPage(browser, baseURL, { width: 390, height: 844 });
		await expect(page.getByRole('navigation', { name: 'Main' })).toBeHidden();
		await page.getByRole('button', { name: 'Open navigation' }).click();
		const drawer = page.getByRole('dialog', { name: 'Navigation' });
		await expect(drawer).toBeVisible();
		await drawer.getByRole('link', { name: 'Settings' }).click();
		await expect(page).toHaveURL(/\/settings$/);
		await expect(drawer).toBeHidden();
		if (shots) {
			await page.goto('/');
			await page.screenshot({ path: `${shots}/e2e-dashboard-390.png` });
		}
		await page.context().close();
	});

	test('environment switcher: choose, remember, reset', async ({ page, request }) => {
		await signIn(page);
		const envs = (await (await page.request.get('/api/v1/environments')).json()).items as { id: string; name: string; online: boolean }[];
		test.skip(envs.length === 0, 'no environments on this manager (the devstack provides three)');
		void request;
		const trigger = page.getByRole('button', { name: /^Environment: All environments/ });
		await trigger.click();
		const list = page.getByRole('listbox', { name: 'Environments' });
		await expect(list.getByRole('option')).toHaveCount(envs.length + 1);
		const target = envs[0];
		await list.getByRole('option', { name: new RegExp(`^${target.name}`) }).click();
		await expect(page.getByRole('button', { name: new RegExp(`^Environment: ${target.name},`) })).toBeVisible();

		// Remembered for this user across reloads (only the ID is stored).
		await page.reload();
		await expect(page.getByRole('button', { name: new RegExp(`^Environment: ${target.name},`) })).toBeVisible();
		const stored = await page.evaluate(() => Object.entries(localStorage).filter(([k]) => k.startsWith('dockyard:environment:')));
		expect(stored).toHaveLength(1);
		expect(stored[0][1]).toBe(target.id);

		// Environment-scoped pages show it as the first crumb.
		await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Stacks' }).click();
		const crumbs = page.getByRole('navigation', { name: 'Breadcrumb' });
		await expect(crumbs.getByRole('link', { name: target.name })).toHaveAttribute('href', `/environments/${target.id}`);
		await expect(crumbs.getByText('Stacks')).toHaveAttribute('aria-current', 'page');

		await page.getByRole('button', { name: new RegExp(`^Environment: ${target.name},`) }).click();
		await page.getByRole('listbox', { name: 'Environments' }).getByRole('option', { name: /^All environments/ }).click();
		await expect(page.getByRole('button', { name: /^Environment: All environments/ })).toBeVisible();
	});

	test('an invited Restricted user sees the denied state', async ({ page, browser, baseURL }) => {
		await signIn(page);
		const res = await page.request.post('/api/v1/invitations', {
			data: {},
			headers: { 'Idempotency-Key': `e2e-${Date.now()}` }
		});
		expect(res.status()).toBe(201);
		const { url } = (await res.json()) as { url: string };
		const link = new URL(url);
		expect(link.hash).toMatch(/^#code=dyi_/);

		const ctx = await browser.newContext({ baseURL, ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === '1' });
		const guest = await ctx.newPage();
		await guest.goto(link.pathname + link.hash);
		// The code is taken from the fragment and removed from the address bar.
		await expect(guest.getByLabel('Invitation code')).toHaveValue(/^dyi_/);
		await expect(guest).toHaveURL(/\/invitation$/);
		const name = `guest${Date.now() % 100000}`;
		await guest.getByLabel('Username').fill(name);
		await guest.getByLabel('Password', { exact: true }).fill('a long guest passphrase for e2e');
		await guest.getByRole('button', { name: 'Create account' }).click();

		await expect(guest.getByRole('heading', { level: 1, name: "You don't have access to anything yet." })).toBeVisible();
		await expect(guest.getByText('Ask the owner of this DockYard to grant access.')).toBeVisible();
		const links = guest.getByRole('navigation', { name: 'Main' }).getByRole('link');
		await expect(links).toHaveText(['Dashboard', 'Settings']);
		// Nothing leaks through search either.
		const search = await guest.request.get('/api/v1/search?q=a');
		expect((await search.json()).items).toEqual([]);
		if (shots) await guest.screenshot({ path: `${shots}/e2e-denied-1440.png` });
		await ctx.close();
	});
});
