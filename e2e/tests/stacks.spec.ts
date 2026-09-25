// Stack pages end to end (#22 track B2, #7, #20, #35): the list, the stack
// detail (header, KPIs, services, tabs), deploy with job progress, the
// revision diff, edit details, the migration preflight, create and delete,
// and discovery/import, at desktop and narrow widths.
//
// Locally against the seeded devstack (docs/development.md, "UI devstack"),
// which has the stacks Silo and Media on homelab, an adoptable project and
// simulated Compose deploys:
//
//   npm --prefix web run build
//   go run ./test/devstack -addr 127.0.0.1:8080
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/stacks.spec.ts
//
// Behind the TLS proxies in CI the manager has no environments: the
// seeded-data steps skip and the empty states are checked instead.
// E2E_SCREENSHOTS_DIR saves review screenshots (1440x900 and 390x844).
import { expect, test, type Browser, type Page } from '@playwright/test';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const shots = process.env.E2E_SCREENSHOTS_DIR ?? '';

async function signIn(page: Page) {
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page.getByRole('main')).toBeVisible();
	await expect(page).not.toHaveURL(/\/sign-in/);
}

async function signedIn(browser: Browser, baseURL: string | undefined, viewport = { width: 1440, height: 900 }) {
	const ctx = await browser.newContext({
		baseURL,
		viewport,
		ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === '1'
	});
	const page = await ctx.newPage();
	await signIn(page);
	return page;
}

async function shot(page: Page, name: string) {
	if (shots) await page.screenshot({ path: `${shots}/stacks-${name}.png` });
}

interface StackRef {
	id: string;
	name: string;
}

async function findStack(page: Page, name: string): Promise<StackRef | undefined> {
	const r = await page.request.get('/api/v1/stacks?limit=200');
	if (!r.ok()) return undefined;
	const items: StackRef[] = (await r.json()).items;
	return items.find((s) => s.name === name);
}

test.describe.serial('Stacks', () => {
	test.beforeEach(async ({ request }) => {
		const status = await (await request.get('/api/v1/setup/status')).json();
		test.skip(!status.setupComplete, 'first-run setup is not complete (ui.spec.ts creates the owner)');
	});

	test('the stack list, or its empty state inviting to create or import', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL);
		await page.goto('/stacks');
		await expect(page.getByRole('heading', { level: 1, name: 'Stacks' })).toBeVisible();
		await expect(page.getByRole('link', { name: 'Create stack' }).first()).toBeVisible();
		await expect(page.getByRole('link', { name: 'Import project' }).first()).toBeVisible();
		const silo = await findStack(page, 'silo');
		if (!silo) {
			await expect(page.getByText('Create a stack or import an existing Compose project.')).toBeVisible();
			// Without environments the create page says what to do first.
			await page.getByRole('link', { name: 'Create stack' }).first().click();
			await expect(page.getByText(/No environments yet|Environment/).first()).toBeVisible();
			return;
		}
		const table = page.getByRole('table', { name: 'Stacks' });
		await expect(table.getByRole('link', { name: /Silo/ })).toBeVisible();
		await expect(table.getByRole('columnheader', { name: 'Environment' })).toBeVisible();
		await page.getByLabel('Filter stacks').fill('jellyfin');
		await expect(table.getByRole('link', { name: /Silo/ })).toHaveCount(0);
		await expect(table.getByRole('link', { name: /Media/ })).toBeVisible();
		await page.getByLabel('Filter stacks').fill('');
		await shot(page, 'list-1440');
	});

	test('stack detail: header, KPIs, services and the tabs', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL);
		const silo = await findStack(page, 'silo');
		test.skip(!silo, 'no seeded stack Silo (run the devstack)');
		await page.goto('/stacks');
		await page.getByRole('table', { name: 'Stacks' }).getByRole('link', { name: /Silo/ }).click();
		await expect(page).toHaveURL(new RegExp(`/stacks/${silo!.id}$`));
		await expect(page.getByRole('heading', { level: 1, name: 'Silo' })).toBeVisible();
		await expect(page.getByText('Personal cloud and media platform')).toBeVisible();
		await expect(page.getByText('homelab · silo')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Copy host path' })).toBeVisible();
		for (const kpi of ['Stack status', 'Services', 'CPU usage', 'Memory usage', 'Uptime', 'Last deploy'])
			await expect(page.getByRole('group', { name: kpi })).toBeVisible();
		const services = page.getByRole('table', { name: 'Services of Silo' });
		await expect(services.getByRole('row')).toHaveCount(6);
		await expect(services.getByRole('link', { name: '8080:80' })).toHaveAttribute('href', 'http://192.168.1.10:8080');
		await expect(services.getByRole('link', { name: 'Open a terminal in silo-web' })).toHaveAttribute(
			'href',
			`/stacks/${silo!.id}/terminal?service=silo-web`
		);
		// silo-worker publishes no port: no open action.
		await expect(services.getByRole('link', { name: 'Open silo-worker' })).toHaveCount(0);
		const tabs = page.getByRole('navigation', { name: 'Silo sections' });
		for (const t of ['Overview', 'Files', 'Logs', 'Terminal', 'Revisions', 'Policies', 'Activity'])
			await expect(tabs.getByRole('link', { name: t })).toBeVisible();
		await expect(tabs.getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page');
		await shot(page, 'detail-1440');

		// Keyboard: the header actions are reachable and labelled.
		await page.getByRole('button', { name: 'More stack actions' }).focus();
		await page.keyboard.press('Enter');
		await expect(page.getByRole('menuitem', { name: 'Edit details' })).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(page.getByRole('menu')).toHaveCount(0);
	});

	test('revisions: the undeployed changes against the deployed revision', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL);
		const silo = await findStack(page, 'silo');
		test.skip(!silo, 'no seeded stack Silo (run the devstack)');
		await page.goto(`/stacks/${silo!.id}`);
		await expect(page.getByRole('heading', { level: 1, name: 'Silo' })).toBeVisible();
		const chip = page.getByRole('link', { name: 'Undeployed changes' });
		test.skip((await chip.count()) === 0, 'Silo has no undeployed changes on this manager');
		await chip.click();
		await expect(page).toHaveURL(/\/revisions$/);
		await page.getByRole('button', { name: 'Show changes' }).click();
		const diff = page.getByRole('region', { name: 'Changes in compose.yaml' });
		await expect(diff).toBeVisible();
		await expect(diff.getByText('Added:').first()).toBeAttached();
		await expect(page.getByRole('table', { name: 'Revisions of Silo' }).getByText('On disk', { exact: true })).toBeVisible();
		await shot(page, 'revisions-1440');
	});

	test('deploy runs as a job with progress and repeats the action in a toast', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL);
		const silo = await findStack(page, 'silo');
		test.skip(!silo, 'no seeded stack Silo (run the devstack)');
		await page.goto(`/stacks/${silo!.id}`);
		await page.getByRole('button', { name: 'Deploy', exact: true }).click();
		const job = page.getByRole('region', { name: 'Jobs started here' });
		await expect(job.getByText('Deploy Silo')).toBeVisible();
		await expect(job.getByText('Succeeded', { exact: true })).toBeVisible({ timeout: 20_000 });
		await expect(page.getByText('Deployed Silo')).toBeVisible();
		await expect(page.getByRole('link', { name: 'Undeployed changes' })).toHaveCount(0);
		await job.getByRole('button', { name: 'Dismiss Deploy Silo' }).click();
		await expect(job).toHaveCount(0);
	});

	test('edit details changes DockYard metadata only', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL);
		const silo = await findStack(page, 'silo');
		test.skip(!silo, 'no seeded stack Silo (run the devstack)');
		await page.goto(`/stacks/${silo!.id}`);
		await page.getByRole('button', { name: 'More stack actions' }).click();
		await page.getByRole('menuitem', { name: 'Edit details' }).click();
		const dialog = page.getByRole('dialog', { name: 'Edit details of Silo' });
		await expect(dialog.getByText('Compose files are never changed.')).toBeVisible();
		await dialog.getByLabel('Description', { exact: true }).fill('Personal cloud, photos and files');
		await dialog.getByRole('button', { name: 'Save details' }).click();
		await expect(page.getByText('Saved details of Silo')).toBeVisible();
		await expect(page.getByText('Personal cloud, photos and files')).toBeVisible();
		// Put it back.
		await page.getByRole('button', { name: 'More stack actions' }).click();
		await page.getByRole('menuitem', { name: 'Edit details' }).click();
		await dialog.getByLabel('Description', { exact: true }).fill('Personal cloud and media platform');
		await dialog.getByRole('button', { name: 'Save details' }).click();
		await expect(page.getByText('Personal cloud and media platform')).toBeVisible();
	});

	test('migration wizard: the preflight before anything stops', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL);
		const silo = await findStack(page, 'silo');
		test.skip(!silo, 'no seeded stack Silo (run the devstack)');
		await page.goto(`/stacks/${silo!.id}`);
		await page.getByRole('button', { name: 'More stack actions' }).click();
		await page.getByRole('menuitem', { name: 'Migrate' }).click();
		await expect(page).toHaveURL(/\/migrate$/);
		await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByText('Migrate')).toBeVisible();
		await page.getByRole('radio', { name: 'nas' }).check();
		await page.getByRole('button', { name: 'Run preflight' }).click();
		await expect(page.getByRole('heading', { name: 'Preflight' })).toBeVisible();
		await expect(page.getByText('Data to copy')).toBeVisible();
		await expect(page.getByRole('table', { name: 'Images on the destination' })).toBeVisible();
		await expect(page.getByRole('table', { name: 'Volumes' })).toBeVisible();
		await expect(page.getByText('Unencrypted transfer')).toBeVisible();
		await shot(page, 'preflight-1440');
		// Nothing stopped: Silo still runs.
		await expect(page.getByText('Running').first()).toBeVisible();
		await page.getByRole('button', { name: 'Back' }).click();
		await expect(page.getByRole('radio', { name: 'nas' })).toBeChecked();
	});

	test('create a stack, validate it, then delete it', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL);
		test.skip(!(await findStack(page, 'silo')), 'no seeded environment (run the devstack)');
		const name = `e2e-${Date.now().toString(36)}`;
		await page.goto('/stacks/new');
		await page.getByLabel('Environment', { exact: true }).selectOption({ label: 'homelab' });
		await page.getByLabel('Name', { exact: true }).fill('Bad Name');
		await page.getByLabel('Name', { exact: true }).blur();
		await expect(page.getByText(/lower-case letters/).first()).toBeVisible();
		await page.getByLabel('Name', { exact: true }).fill(name);
		await page.getByRole('button', { name: 'Validate' }).click();
		await expect(page.getByText(/Valid: 1\s+service/)).toBeVisible();
		await page.getByRole('button', { name: 'Create stack' }).click();
		await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
		await expect(page.getByText(`Created ${name}`)).toBeVisible();

		await page.getByRole('button', { name: 'More stack actions' }).click();
		await page.getByRole('menuitem', { name: 'Delete' }).click();
		const dialog = page.getByRole('alertdialog', { name: `Delete ${name}?` });
		await expect(dialog.getByText('Keeps its volumes and the project directory on the host.')).toBeVisible();
		await expect(dialog.getByRole('button', { name: 'Delete stack' })).toBeDisabled();
		await dialog.getByRole('textbox').fill(name);
		await dialog.getByRole('button', { name: 'Delete stack' }).click();
		await expect(page).toHaveURL(/\/stacks$/, { timeout: 20_000 });
		await expect(page.getByText(`Deleted ${name}`)).toBeVisible();
	});

	test('discovery lists unmanaged projects and how they can be imported', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL);
		test.skip(!(await findStack(page, 'silo')), 'no seeded environment (run the devstack)');
		await page.goto('/stacks');
		await page.getByRole('link', { name: 'Import project' }).first().click();
		await expect(page.getByRole('heading', { level: 1, name: 'Import a Compose project' })).toBeVisible();
		await page.getByLabel('Environment', { exact: true }).selectOption({ label: 'homelab' });
		await expect(page.getByText('Can be adopted in place')).toBeVisible();
		await expect(page.getByRole('button', { name: 'Adopt in place' })).toBeVisible();
		await expect(page.getByText('Already managed')).toBeVisible();
		await page.getByLabel('Environment', { exact: true }).selectOption({ label: 'nas' });
		await expect(page.getByText('Needs its Compose source')).toBeVisible();
		await expect(page.getByText('/srv/compose/paperless')).toBeVisible();
		await shot(page, 'discovered-1440');
	});

	test('narrow layout: stacked rows and the current tab in view', async ({ browser, baseURL }) => {
		const page = await signedIn(browser, baseURL, { width: 390, height: 844 });
		const silo = await findStack(page, 'silo');
		test.skip(!silo, 'no seeded stack Silo (run the devstack)');
		await page.goto(`/stacks/${silo!.id}/activity`);
		const tab = page.getByRole('navigation', { name: 'Silo sections' }).getByRole('link', { name: 'Activity' });
		await expect(tab).toBeInViewport();
		await page.goto(`/stacks/${silo!.id}`);
		await expect(page.getByRole('heading', { level: 1, name: 'Silo' })).toBeVisible();
		await expect(page.getByRole('columnheader')).toHaveCount(0);
		await shot(page, 'detail-390');
	});
});
