// Automation and backup screens (#22 track B5: #20 updates, #14
// maintenance, #10 backups, #24 import) end to end.
//
// Locally against the seeded devstack (docs/development.md, "UI devstack"):
//   go run ./test/devstack -addr 127.0.0.1:8080
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/admin-automation.spec.ts
// The import test needs a -setup devstack started after a seeded one
// (E2E_IMPORT_DIR = its backups/manager directory, E2E_IMPORT_KEY = the
// printed Recovery Key). In CI (a manager without agents or backups behind
// each TLS proxy) the steps that need seeded data skip themselves.
// E2E_SCREENSHOTS_DIR saves review screenshots (1440x900 and 390x844).
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

async function shot(page: Page, name: string) {
	if (!shots) return;
	await page.screenshot({
		path: `${shots}/b5-${name}-${page.viewportSize()?.width}.png`,
		fullPage: true
	});
}

async function onlineEnvironments(page: Page): Promise<{ id: string; name: string }[]> {
	const envs = (await (await page.request.get('/api/v1/environments')).json()).items as {
		id: string;
		name: string;
		online: boolean;
	}[];
	return envs.filter((e) => e.online);
}

test.describe('updates (#20)', () => {
	test('policies list, detail with digests and quarantine guidance, preview', async ({
		page
	}) => {
		await signIn(page);
		await page
			.getByRole('navigation', { name: 'Main' })
			.getByRole('link', { name: 'Updates' })
			.click();
		await expect(page.getByRole('heading', { level: 1, name: 'Updates' })).toBeVisible();
		const policies = (await (await page.request.get('/api/v1/update-policies')).json())
			.items as { name: string }[];
		if (policies.length === 0) {
			await expect(page.getByText('No update policies yet.')).toBeVisible();
			await page.getByRole('link', { name: 'Create update policy' }).first().click();
			await expect(
				page.getByRole('heading', {
					level: 1,
					name: 'New update policy'
				})
			).toBeVisible();
			await expect(page.getByRole('button', { name: 'Create update policy' })).toBeDisabled();
			return;
		}
		await page.getByRole('link', { name: policies[0].name }).click();
		await expect(page.getByRole('heading', { level: 1, name: policies[0].name })).toBeVisible();
		const table = page.getByRole('table', { name: /Update candidates of/ });
		await expect(table).toBeVisible();
		if (await page.getByText('Quarantined digests').isVisible()) {
			await expect(page.getByText('Failed updates are not rolled back')).toBeVisible();
			await expect(
				page.getByText(/in your own (Compose file|definition)/).first()
			).toBeVisible();
		}
		await shot(page, 'update-policy');
		await page.getByRole('button', { name: 'Preview update' }).click();
		const dialog = page.getByRole('dialog', { name: 'Update preview' });
		await expect(dialog).toBeVisible();
		// Either the plan or why it cannot be computed (the devstack has no registry).
		await expect(
			dialog
				.getByText(/Recreated|Nothing to update|could not be computed|Undeployed changes/)
				.first()
		).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(dialog).toBeHidden();
	});

	test('the form keeps checks and updates off until turned on', async ({ page }) => {
		await signIn(page);
		await page.goto('/updates/new');
		await expect(page.getByRole('switch', { name: 'Check automatically' })).toHaveAttribute(
			'aria-checked',
			'false'
		);
		await expect(page.getByRole('switch', { name: 'Update automatically' })).toHaveAttribute(
			'aria-checked',
			'false'
		);
		await page.getByRole('switch', { name: 'Update automatically' }).click();
		await expect(page.getByText('Containers restart without asking')).toBeVisible();
		await shot(page, 'update-new');
	});
});

test.describe('maintenance (#14)', () => {
	test('create a policy, preview it and run it in the background', async ({ page }) => {
		await signIn(page);
		const envs = await onlineEnvironments(page);
		test.skip(envs.length === 0, 'no online environment on this manager');
		const name = `E2E cleanup ${Date.now() % 100000}`;
		await page.goto('/maintenance/new');
		await page.getByLabel('Name', { exact: true }).fill(name);
		await page.getByLabel('Environment', { exact: true }).selectOption(envs[0].id);
		// Every rule starts off; volume rules need their own opt-in first.
		await expect(page.getByRole('switch', { name: 'Named volumes' })).toBeDisabled();
		await page.getByRole('switch', { name: 'Stopped containers' }).click();
		await page.getByRole('button', { name: 'Create maintenance policy' }).click();
		await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
		await expect(page.getByText(`Created maintenance policy ${name}`)).toBeVisible();

		await page.getByRole('button', { name: 'Preview' }).click();
		await expect(page.getByText(/would be removed/)).toBeVisible();
		await shot(page, 'maintenance-preview');

		await page.getByRole('button', { name: 'Run now' }).first().click();
		const confirm = page.getByRole('alertdialog', {
			name: `Run ${name} now?`
		});
		await expect(confirm).toBeVisible();
		await expect(confirm.getByText('A completed removal cannot be undone.')).toBeVisible();
		await confirm.getByRole('switch', { name: 'Run in background' }).click();
		await confirm.getByRole('button', { name: `Run ${name}` }).click();
		await expect(page.getByText(`Pruning ${name} in the background`)).toBeVisible();
		await expect(
			page.getByText(new RegExp(`(Pruned ${name}|${name} was not pruned)`))
		).toBeVisible({ timeout: 20_000 });

		// Deleting the policy (204 No Content) returns to the list.
		await page.getByRole('button', { name: 'More actions' }).click();
		await page.getByRole('menuitem', { name: 'Delete policy' }).click();
		const del = page.getByRole('alertdialog', { name: `Delete maintenance policy ${name}` });
		await del.getByRole('textbox').fill(name);
		await del.getByRole('button', { name: 'Delete policy' }).click();
		await expect(page).toHaveURL(/\/maintenance$/);
		await expect(page.getByText(`Deleted maintenance policy ${name}`)).toBeVisible();
	});

	test('defaults show every rule off with volume opt-ins', async ({ page }) => {
		await signIn(page);
		await page.goto('/maintenance/defaults');
		await expect(
			page.getByRole('heading', {
				level: 1,
				name: 'Maintenance defaults'
			})
		).toBeVisible();
		await expect(page.getByRole('switch', { name: 'Anonymous volumes' })).toHaveAttribute(
			'aria-checked',
			'false'
		);
		await expect(
			page.getByRole('checkbox', { name: /deletes the data in them/ }).first()
		).not.toBeChecked();
	});
});

test.describe('backups (#10)', () => {
	test('history, repositories and a backup with its contents', async ({ page }) => {
		await signIn(page);
		await page
			.getByRole('navigation', { name: 'Main' })
			.getByRole('link', { name: 'Backups' })
			.click();
		await expect(page.getByRole('heading', { level: 1, name: 'Backups' })).toBeVisible();
		const repos = (await (await page.request.get('/api/v1/backup-repositories')).json())
			.items as { name: string; state: string }[];
		if (repos.length === 0) {
			await expect(page.getByText('No backups yet.')).toBeVisible();
			await expect(page.getByRole('link', { name: 'Add backup repository' })).toBeVisible();
			return;
		}
		await shot(page, 'backups');
		await page.getByRole('link', { name: 'Repositories' }).click();
		const ready = repos.find((r) => r.state === 'ready');
		test.skip(!ready, 'no confirmed repository');
		await page.getByRole('link', { name: ready!.name }).click();
		await expect(page.getByRole('heading', { level: 1, name: ready!.name })).toBeVisible();
		await page.getByRole('button', { name: 'Test connection' }).click();
		await expect(page.getByText(/Connection (works|failed)/)).toBeVisible();

		const backups = (await (await page.request.get('/api/v1/backups')).json()).items as {
			id: string;
		}[];
		test.skip(backups.length === 0, 'no backups yet');
		await page.goto(`/backups/${backups[0].id}`);
		await expect(page.getByRole('table', { name: /Contents of/ })).toBeVisible();
		await expect(page.getByRole('link', { name: /^Download / }).first()).toBeVisible();
		await shot(page, 'backup');
	});

	test('policy wizard: scope, shutdown off, schedule off, retention floor and a first backup', async ({
		page
	}) => {
		await signIn(page);
		const repos = (await (await page.request.get('/api/v1/backup-repositories')).json())
			.items as { id: string; state: string }[];
		test.skip(!repos.some((r) => r.state === 'ready'), 'no confirmed repository');
		const name = `E2E backup ${Date.now() % 100000}`;
		await page.goto('/backups/policies/new');
		await page.getByLabel('Name', { exact: true }).fill(name);
		await page.getByLabel('Repository', { exact: true }).selectOption({ index: 1 });
		await page.getByRole('button', { name: 'Create policy' }).click();
		await expect(
			page.getByRole('heading', { level: 2, name: 'What to back up' })
		).toBeVisible();
		await expect(
			page.getByRole('switch', { name: 'Back up the manager state' })
		).toHaveAttribute('aria-checked', 'true');
		await page.getByRole('button', { name: 'Next' }).click();
		await expect(
			page.getByRole('switch', {
				name: 'Stop containers during backups'
			})
		).toHaveAttribute('aria-checked', 'false');
		await page.getByRole('button', { name: 'Next' }).click();
		await expect(page.getByRole('switch', { name: 'Back up automatically' })).toHaveAttribute(
			'aria-checked',
			'false'
		);
		await page.getByRole('button', { name: 'Next' }).click();
		await expect(page.getByLabel('Minimum recovery floor')).toHaveValue('3');
		await page.getByRole('button', { name: 'Preview retention' }).click();
		await expect(page.getByText(/forgotten|nothing would be forgotten/).first()).toBeVisible();
		await page.getByRole('button', { name: 'Save policy' }).click();
		await page.getByRole('button', { name: 'Run first backup' }).click();
		await expect(page.getByText(`Started a backup of ${name}`)).toBeVisible();
		await shot(page, 'policy-first-backup');
		await page.getByRole('button', { name: 'Done' }).click();
		await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
	});

	test('restore wizard of the manager state explains the fresh-manager import', async ({
		page
	}) => {
		await signIn(page);
		const backups = (
			await (await page.request.get('/api/v1/backups?kind=manager_state')).json()
		).items as { id: string }[];
		test.skip(backups.length === 0, 'no manager-state backup');
		await page.goto(`/backups/${backups[0].id}/restore`);
		await expect(
			page.getByRole('heading', {
				name: 'Restore the manager on a new DockYard'
			})
		).toBeVisible();
		await expect(page.getByText('Import from backup').first()).toBeVisible();
	});
});

test.describe('fresh-manager import (#24)', () => {
	const dir = process.env.E2E_IMPORT_DIR ?? '';
	const key = process.env.E2E_IMPORT_KEY ?? '';

	test('setup imports a backup set and asks to re-attach hosts', async ({ page }) => {
		const status = await (await page.request.get('/api/v1/setup/status')).json();
		test.skip(
			status.setupComplete || !dir || !key,
			'needs a -setup devstack with backups (E2E_IMPORT_DIR, E2E_IMPORT_KEY)'
		);
		await page.goto('/setup');
		await page.getByRole('link', { name: 'Import from backup' }).click();
		await expect(
			page.getByRole('heading', { level: 1, name: 'Import from backup' })
		).toBeVisible();
		await page.getByLabel('Directory', { exact: true }).fill(dir);
		await page.getByLabel('Recovery Key', { exact: true }).fill(key);
		await page.getByRole('button', { name: 'Check access' }).click();
		await expect(page.getByText('Access works')).toBeVisible();
		await page.getByRole('button', { name: 'Show backups' }).click();
		await expect(page.getByRole('group', { name: 'Backup set' })).toBeVisible();
		await page.getByRole('button', { name: 'Next' }).click();
		await page
			.getByRole('checkbox', {
				name: "Replace this manager's state with the backup and restart"
			})
			.check();
		await page.getByRole('button', { name: 'Import and restart' }).click();
		await expect(page.getByText('Imported', { exact: true })).toBeVisible({
			timeout: 60_000
		});
		await expect(page.getByText(/enroll its agent again/)).toBeVisible();
		await page.getByRole('button', { name: 'Sign in' }).click();
		await expect(page).toHaveURL(/\/sign-in/);
	});
});
