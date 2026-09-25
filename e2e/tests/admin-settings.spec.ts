// Settings, audit, API tokens and profile (#22 track B5: #16 sign-in
// policy and factors, #31 API tokens, #30 audit viewer, #13 schedule
// defaults, #34 diagnostics) end to end, as the owner.
//
// Locally against the seeded devstack (docs/development.md, "UI devstack"):
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/admin-settings.spec.ts
// In CI behind each TLS proxy (a manager whose owner ui.spec.ts created).
import { expect, test, type Page } from '@playwright/test';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const shots = process.env.E2E_SCREENSHOTS_DIR ?? '';

async function signIn(page: Page) {
	const status = await (await page.request.get('/api/v1/setup/status')).json();
	test.skip(!status.setupComplete, 'setup is still open on this manager (run ui.spec.ts first)');
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
}

async function confirmIdentity(page: Page) {
	const dialog = page.getByRole('dialog', { name: "Confirm it's you" });
	if (await dialog.isVisible().catch(() => false)) {
		await dialog.getByLabel('Password').fill(password);
		await dialog.getByRole('button', { name: 'Confirm with password' }).click();
	}
}

async function shot(page: Page, name: string) {
	if (!shots) return;
	await page.screenshot({
		path: `${shots}/b5-${name}-${page.viewportSize()?.width}.png`,
		fullPage: true
	});
}

test.describe('settings', () => {
	test('overview links every area the owner may open', async ({ page }) => {
		await signIn(page);
		await page
			.getByRole('navigation', { name: 'Main' })
			.getByRole('link', { name: 'Settings' })
			.click();
		await expect(page.getByRole('heading', { level: 1, name: 'Settings' })).toBeVisible();
		const tabs = page.getByRole('navigation', { name: 'Settings sections' });
		for (const name of [
			'Profile and security',
			'API tokens',
			'Sign-in policy',
			'Schedule defaults',
			'Audit log',
			'Diagnostics'
		])
			await expect(tabs.getByRole('link', { name })).toBeVisible();
	});

	test('this DockYard: rename it, deployment settings read-only (#4)', async ({ page, baseURL }) => {
		await signIn(page);
		await page.goto('/settings');
		const card = page.getByRole('region', { name: 'About this DockYard' });
		await expect(card).toContainText(new URL(baseURL ?? page.url()).origin);
		await expect(card).toContainText('Stream heartbeat');
		const name = `E2E instance ${Date.now() % 100000}`;
		await card.getByRole('button', { name: 'Rename' }).click();
		await card.getByLabel('Name').fill('   ');
		await card.getByRole('button', { name: 'Rename DockYard' }).click();
		await expect(card).toContainText('Enter a name.');
		await card.getByLabel('Name').fill(`  ${name}  `);
		await card.getByRole('button', { name: 'Rename DockYard' }).click();
		await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(`Renamed DockYard to ${name}`);
		await expect(card).toContainText(name);
		const settings = await (await page.request.get('/api/v1/settings')).json();
		expect(settings.name).toBe(name);
		await shot(page, 'overview');
	});

	test('API token: grants from the tree, shown once, then revoked', async ({ page }) => {
		await signIn(page);
		const name = `E2E script ${Date.now() % 100000}`;
		await page.goto('/settings/tokens/new');
		await page.getByLabel('Name', { exact: true }).fill(name);
		const matrix = page.getByRole('region', { name: 'Actions for All resources' });
		await matrix.getByRole('checkbox', { name: 'Grant View stacks' }).check();
		await page.getByRole('button', { name: /^Create token with 1 grant/ }).click();
		await confirmIdentity(page);
		await expect(page.getByText('DockYard shows this API token only once')).toBeVisible();
		const value = await page.getByLabel('API token', { exact: true }).textContent();
		expect(value).toMatch(/^dy_/);
		// The token works as a bearer credential for what it was granted.
		const res = await page.request.get('/api/v1/stacks', {
			headers: { Authorization: `Bearer ${value!.trim()}` }
		});
		expect(res.status()).toBe(200);
		await page.getByRole('checkbox', { name: /I stored the API token/ }).check();
		await page.getByRole('button', { name: 'Done' }).click();
		await expect(page).toHaveURL(/\/settings\/tokens$/);
		const row = page.getByRole('row', { name: new RegExp(name) });
		await expect(row).toContainText('Active');
		await shot(page, 'tokens');
		await row.getByRole('button', { name: 'Revoke' }).click();
		await page.getByRole('alertdialog').getByRole('button', { name: 'Revoke token' }).click();
		await expect(page.getByText(`Revoked the API token ${name}`)).toBeVisible();
		const after = await page.request.get('/api/v1/stacks', {
			headers: { Authorization: `Bearer ${value!.trim()}` }
		});
		expect(after.status()).toBe(401);
	});

	test('audit log: filter, open a record with its details', async ({ page }) => {
		await signIn(page);
		await page.goto('/settings/audit');
		const table = page.getByRole('table', { name: 'Audit records' });
		await expect(table).toBeVisible();
		await page.getByLabel('Category').selectOption('identity');
		await expect(table.getByRole('row').nth(1)).toContainText('Identity');
		await table.getByRole('button').first().click();
		const drawer = page.getByRole('dialog');
		await expect(drawer.getByText('Chain position')).toBeVisible();
		await expect(page.getByRole('link', { name: 'Export CSV' })).toHaveAttribute(
			'href',
			/\/api\/v1\/audit\/exports\?format=csv&category=identity/
		);
		await shot(page, 'audit');
	});

	test('sign-in policy lists the consequences before saving', async ({ page }) => {
		await signIn(page);
		await page.goto('/settings/sign-in');
		const lifetime = page.getByLabel('Invitations expire after (hours)');
		await expect(lifetime).not.toHaveValue('');
		// Pick a value that differs from the saved one so the test can run again.
		await lifetime.fill((await lifetime.inputValue()) === '100' ? '101' : '100');
		await lifetime.blur();
		await page.getByRole('button', { name: 'Save sign-in policy' }).click();
		const dialog = page.getByRole('alertdialog', { name: 'Save the sign-in policy?' });
		await expect(dialog).toContainText('Invitation lifetime (hours):');
		await dialog.getByRole('button', { name: 'Save sign-in policy' }).click();
		await confirmIdentity(page);
		await expect(page.getByText('Saved the sign-in policy')).toBeVisible();
	});

	test('profile: recovery codes are shown once', async ({ page }) => {
		await signIn(page);
		await page.goto('/settings/security');
		await expect(
			page.getByRole('heading', { level: 1, name: 'Profile and security' })
		).toBeVisible();
		await page.getByRole('button', { name: 'Generate new codes' }).click();
		await page
			.getByRole('alertdialog')
			.getByRole('button', { name: 'Generate new codes' })
			.click();
		await confirmIdentity(page);
		const dialog = page.getByRole('dialog', { name: 'Your new recovery codes' });
		await expect(dialog.getByLabel('recovery codes', { exact: true })).toBeVisible();
		await dialog.getByRole('checkbox', { name: /I stored the recovery codes/ }).check();
		await dialog.getByRole('button', { name: 'Done' }).click();
		await expect(page.getByText('10 left')).toBeVisible();
	});
});
