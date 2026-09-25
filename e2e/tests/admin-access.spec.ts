// Access administration (#22 track B5: #16 users and invitations, #17
// groups and the permission editor) end to end, as the owner.
//
// Locally against the seeded devstack (docs/development.md, "UI devstack"):
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/admin-access.spec.ts
// In CI behind each TLS proxy (a manager whose owner ui.spec.ts created).
// E2E_SCREENSHOTS_DIR saves review screenshots.
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
	// A fresh sign-in counts as recent; the dialog appears only when it expired.
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

test.describe.serial('access (#16, #17)', () => {
	const group = `E2E operators ${Date.now() % 100000}`;

	test('invite a user: the link is shown once', async ({ page }) => {
		await signIn(page);
		await page
			.getByRole('navigation', { name: 'Main' })
			.getByRole('link', { name: 'Access' })
			.click();
		await expect(page.getByRole('heading', { level: 1, name: 'Access' })).toBeVisible();
		await expect(page.getByRole('table', { name: 'Users' })).toBeVisible();
		await page.getByRole('button', { name: 'Invite user' }).click();
		const dialog = page.getByRole('dialog', { name: 'Invite a user' });
		await dialog.getByLabel('Email').fill('new.person@example.com');
		await dialog.getByRole('button', { name: 'Create invitation' }).click();
		await confirmIdentity(page);
		await expect(
			dialog.getByText('DockYard shows this invitation link only once')
		).toBeVisible();
		await expect(dialog.getByLabel('invitation link', { exact: true })).toContainText(
			'#code=dyi_'
		);
		await dialog.getByRole('checkbox', { name: /I stored the invitation link/ }).check();
		await dialog.getByRole('button', { name: 'Done' }).click();
		await page.getByRole('link', { name: 'Invitations' }).click();
		const row = page.getByRole('row', { name: /new\.person@example\.com/ }).first();
		await expect(row).toContainText('Pending');
		await row.getByRole('button', { name: 'Revoke' }).click();
		await page
			.getByRole('alertdialog')
			.getByRole('button', { name: 'Revoke invitation' })
			.click();
		await expect(page.getByText('Revoked the invitation')).toBeVisible();
	});

	test('create a group, grant with the permission tree, review and save', async ({ page }) => {
		await signIn(page);
		await page.goto('/access/groups');
		await page.getByRole('button', { name: 'Create group' }).click();
		await page.getByRole('dialog', { name: 'Create a group' }).getByLabel('Name').fill(group);
		await page
			.getByRole('dialog', { name: 'Create a group' })
			.getByRole('button', { name: 'Create group' })
			.click();
		await confirmIdentity(page);
		await expect(page.getByRole('heading', { level: 1, name: group })).toBeVisible();
		await expect(page.getByText('No access').first()).toBeVisible();

		// All resources is selected; allow Restart for every container.
		const matrix = page.getByRole('region', {
			name: 'Actions for All resources'
		});
		await expect(matrix.getByText(/including ones added later/)).toBeVisible();
		await matrix
			.getByRole('radiogroup', { name: 'Restart for All resources' })
			.getByRole('radio', { name: /^Allow/ })
			.check();
		// High-risk actions are marked.
		await expect(
			matrix.getByRole('radiogroup', {
				name: 'Open terminal for All resources'
			})
		).toContainText('High risk');
		// Bulk: deny two actions after a confirmation preview.
		await matrix.getByRole('checkbox', { name: 'Select Start', exact: true }).check();
		await matrix.getByRole('checkbox', { name: 'Select Stop', exact: true }).check();
		await matrix.getByRole('button', { name: 'Deny', exact: true }).click();
		const bulk = page.getByRole('alertdialog', { name: /Deny 2 actions/ });
		await expect(bulk).toContainText('No rule → Deny');
		await bulk.getByRole('button', { name: 'Deny 2 actions' }).click();

		// An environment scope, found with the tree search.
		const envs = (await (await page.request.get('/api/v1/environments')).json()).items as {
			id: string;
			name: string;
		}[];
		if (envs.length) {
			await page
				.getByRole('navigation', { name: 'Resources' })
				.getByRole('button', { name: envs[0].name, exact: true })
				.click();
			await expect(
				page.getByRole('region', {
					name: `Actions for ${envs[0].name}`
				})
			).toBeVisible();
		}
		await shot(page, 'group-editor');

		await page.getByRole('button', { name: 'Save permissions' }).click();
		const confirm = page.getByRole('alertdialog', {
			name: `Save the permissions of ${group}?`
		});
		await expect(confirm).toContainText('No rule → Allow');
		await confirm.getByRole('button', { name: 'Save permissions' }).click();
		await confirmIdentity(page);
		await expect(page.getByText(`Saved the permissions of ${group}`)).toBeVisible();
		await expect(page.getByText('Grants access').first()).toBeVisible();
	});

	test('a user: overrides, effective access and a preview', async ({ page }) => {
		await signIn(page);
		const users = (await (await page.request.get('/api/v1/users')).json()).items as {
			id: string;
			owner: boolean;
			username: string;
		}[];
		const member = users.find((u) => !u.owner);
		test.skip(!member, 'no member account (the devstack seeds guest)');
		await page.goto(`/access/users/${member!.id}`);
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
		const matrix = page.getByRole('region', {
			name: 'Actions for All resources'
		});
		const restart = matrix.getByRole('radiogroup', {
			name: 'Restart for All resources'
		});
		await expect(restart).toContainText(/Inherits (Allow|Deny)/);
		await restart.getByRole('radio', { name: /^Allow/ }).check();
		await page.getByRole('button', { name: 'Preview with unsaved changes' }).click();
		await expect(page.getByRole('table', { name: /Previewed access of/ })).toContainText(
			'Restart'
		);
		await shot(page, 'user-editor');
		await page.getByRole('button', { name: 'Discard' }).click();
		await expect(page.getByRole('region', { name: 'Unsaved permission changes' })).toBeHidden();
	});

	test('delete the group again', async ({ page }) => {
		await signIn(page);
		await page.goto('/access/groups');
		await page.getByRole('link', { name: group }).click();
		await page.getByRole('button', { name: 'Group actions' }).click();
		await page.getByRole('menuitem', { name: 'Delete group' }).click();
		const dialog = page.getByRole('alertdialog', {
			name: `Delete group ${group}`
		});
		await dialog.getByRole('textbox').fill(group);
		await dialog.getByRole('button', { name: 'Delete group' }).click();
		await confirmIdentity(page);
		await expect(page).toHaveURL(/\/access\/groups$/);
		await expect(page.getByText(`Deleted group ${group}`)).toBeVisible();
	});
});
