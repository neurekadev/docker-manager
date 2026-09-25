// Docker resources, builds and credentials in the UI (#22 track B4; #6,
// #19, #32, #33, #35).
//
// Runs against a manager whose owner exists: locally the seeded devstack
// (docs/development.md, "UI devstack"), in CI behind each TLS proxy of
// e2e/compose.yaml. The Docker parts need a connected environment and are
// skipped without one (the proxy-only CI stack has none); the #32 parts
// need DockYard's own containers and stacks volume, which the devstack
// seeds on "homelab" (agent guard with self-inspection) and a real
// co-located install has.
//
//   go run ./test/devstack -addr 127.0.0.1:8080
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/resources.spec.ts
//
// E2E_SCREENSHOTS_DIR saves review screenshots (1440x900 and 390x844).
// E2E_PULL_IMAGE names the image the pull test pulls; the default is a
// unique busybox tag the devstack's fake Engine accepts (set a real one,
// e.g. busybox:1.36, against a real Engine).
import { expect, test, type Page } from '@playwright/test';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const shots = process.env.E2E_SCREENSHOTS_DIR ?? '';

interface Env {
	id: string;
	name: string;
	online: boolean;
}
interface Protection {
	role: string;
	reason: string;
	self: boolean;
}
interface Named {
	name: string;
	environmentId: string;
	protection?: Protection;
}

async function signIn(page: Page) {
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
	await expect(page.getByRole('main')).toBeVisible();
}

/** GET through the signed-in page (same-origin, session cookie). */
async function apiGet<T>(page: Page, path: string): Promise<T> {
	return page.evaluate(async (p) => {
		const r = await fetch(p, { headers: { Accept: 'application/json' } });
		if (!r.ok) throw new Error(`${p}: ${r.status}`);
		return r.json();
	}, path);
}

async function onlineEnvironments(page: Page): Promise<Env[]> {
	const envs = await apiGet<{ items: Env[] }>(page, '/api/v1/environments?limit=200');
	return envs.items.filter((e) => e.online);
}

/** The first object of a list route (in any online environment) matching pred. */
async function findIn<T extends Named>(page: Page, kind: string, pred: (x: T) => boolean): Promise<T | undefined> {
	for (const env of await onlineEnvironments(page)) {
		const list = await apiGet<{ items: T[] }>(page, `/api/v1/environments/${env.id}/${kind}?limit=200`);
		const hit = list.items.find(pred);
		if (hit) return hit;
	}
	return undefined;
}

test.beforeEach(async ({ request }) => {
	const status = await (await request.get('/api/v1/setup/status')).json();
	test.skip(!status.setupComplete, 'first-run setup is not complete on this manager (ui.spec.ts does it)');
});

test.describe('DockYard protects itself in the UI (#32)', () => {
	test("stopping and removing the connected agent fail with the reason", async ({ page }) => {
		await signIn(page);
		const agent = await findIn<Named>(page, 'containers', (c) => c.protection?.role === 'agent' && c.protection.self);
		test.skip(!agent, "no connected agent runs in a container here (the devstack seeds one on homelab)");
		await page.goto(`/containers/${agent!.environmentId}/${encodeURIComponent(agent!.name)}`);
		await expect(page.getByRole('heading', { level: 1, name: agent!.name })).toBeVisible();
		await expect(page.getByText('DockYard system').first()).toBeVisible();
		await expect(page.getByText("This environment's DockYard agent")).toBeVisible();

		// Stop: confirmed, sent, refused by the server with its reason.
		await page.getByRole('button', { name: 'Stop', exact: true }).click();
		const stop = page.getByRole('alertdialog', { name: `Stop ${agent!.name}?` });
		await stop.getByRole('button', { name: 'Stop container' }).click();
		await expect(stop.getByRole('alert')).toContainText("DockYard's own agent can't be stopped from DockYard.");
		await expect(stop.getByRole('alert')).toContainText('cuts DockYard off from this host');
		await stop.getByRole('button', { name: 'Cancel' }).click();
		await expect(stop).toBeHidden();
		await expect(page.getByText('Running', { exact: true }).first()).toBeVisible();

		// Remove: the server's removal preview refuses it; nothing to confirm.
		await page.getByRole('button', { name: 'More actions' }).click();
		await page.getByRole('menuitem', { name: 'Remove…' }).click();
		const remove = page.getByRole('alertdialog', { name: `${agent!.name} can't be removed` });
		await expect(remove).toContainText("DockYard's own resource");
		await expect(remove).toContainText('use Docker on the host');
		await expect(remove.getByRole('button', { name: /^Remove/ })).toHaveCount(0);
		if (shots) await page.screenshot({ path: `${shots}/b4-agent-remove-refused-1440.png` });
		await remove.getByRole('button', { name: 'Close' }).click();

		// The API refuses too (the UI never relies on hiding): a direct DELETE answers 409 protected.
		const res = await page.evaluate(async (path) => {
			const r = await fetch(path, { method: 'DELETE', headers: { Accept: 'application/json', 'Idempotency-Key': crypto.randomUUID() } });
			return { status: r.status, body: await r.json() };
		}, `/api/v1/environments/${agent!.environmentId}/containers/${encodeURIComponent(agent!.name)}?force=true`);
		expect(res.status).toBe(409);
		expect(res.body.code).toBe('protected');
	});

	test('deleting the stacks volume fails with the reason', async ({ page }) => {
		await signIn(page);
		const stacks = await findIn<Named>(page, 'volumes', (v) => v.protection?.role === 'stacks');
		test.skip(!stacks, 'no stacks volume is identified here');
		await page.goto('/volumes');
		const row = page.getByRole('row', { name: new RegExp(stacks!.name) });
		await expect(row.getByText('DockYard system')).toBeVisible();
		await row.getByRole('button', { name: `Actions for ${stacks!.name}` }).click();
		await page.getByRole('menuitem', { name: 'Remove…' }).click();
		const dialog = page.getByRole('alertdialog', { name: `${stacks!.name} can't be removed` });
		await expect(dialog).toContainText('the DockYard stacks volume');
		await expect(dialog).not.toContainText('#28');
		await dialog.getByRole('button', { name: 'Close' }).click();

		// The detail page says the same up front and offers no file manager or migration.
		await page.goto(`/volumes/${stacks!.environmentId}/${stacks!.name}`);
		await expect(page.getByText('The DockYard stacks volume').first()).toBeVisible();
		await expect(page.getByRole('link', { name: 'Files' })).toHaveCount(0);
		await expect(page.getByRole('link', { name: 'Migrate' })).toHaveCount(0);

		const res = await page.evaluate(async (path) => {
			const r = await fetch(path, { method: 'DELETE', headers: { Accept: 'application/json', 'Idempotency-Key': crypto.randomUUID() } });
			return { status: r.status, body: await r.json() };
		}, `/api/v1/environments/${stacks!.environmentId}/volumes/${stacks!.name}`);
		expect(res.status).toBe(409);
		expect(res.body.code).toBe('protected');
	});
});

test.describe('Docker resources (#6)', () => {
	test('containers: filters, detail tabs and keyboard access to row actions', async ({ page }) => {
		await signIn(page);
		test.skip((await onlineEnvironments(page)).length === 0, 'needs a connected environment');
		await page.goto('/containers');
		await expect(page.getByRole('heading', { level: 1, name: 'Containers' })).toBeVisible();
		const table = page.getByRole('table', { name: 'Containers' });
		await expect(table.getByRole('row').nth(1)).toBeVisible();
		const total = await table.getByRole('row').count();

		await page.getByLabel('State').selectOption('exited');
		for (const status of await table.getByRole('row').locator('td:nth-child(2)').allInnerTexts())
			expect(status).toContain('Exited');
		await page.getByLabel('State').selectOption('');
		await page.getByRole('searchbox', { name: 'Search containers' }).fill('zzz-no-such-container');
		await expect(page.getByText('No containers match these filters.')).toBeVisible();
		await page.getByRole('button', { name: 'Clear filters' }).click();
		await expect(table.getByRole('row')).toHaveCount(total);

		// Row actions are reachable by keyboard.
		const first = table.getByRole('row').nth(1);
		const menuButton = first.getByRole('button', { name: /^Actions for / });
		await menuButton.focus();
		await page.keyboard.press('Enter');
		await expect(page.getByRole('menuitem', { name: 'Open' })).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(menuButton).toBeFocused();

		await first.getByRole('link').first().click();
		await expect(page.getByRole('navigation', { name: 'Container sections' }).getByRole('link', { name: 'Overview' })).toHaveAttribute(
			'aria-current',
			'page'
		);
		await expect(page.getByRole('heading', { name: 'Configuration' })).toBeVisible();
		if (shots) {
			await page.screenshot({ path: `${shots}/b4-container-1440.png`, fullPage: true });
			await page.goto('/containers');
			await page.screenshot({ path: `${shots}/b4-containers-1440.png`, fullPage: true });
		}
	});

	test('volumes and networks: create and remove through jobs', async ({ page }) => {
		await signIn(page);
		const envs = await onlineEnvironments(page);
		test.skip(envs.length === 0, 'needs a connected environment');
		const suffix = Date.now().toString(36);

		for (const kind of ['volume', 'network'] as const) {
			const name = `b4-e2e-${kind}-${suffix}`;
			await page.goto(`/${kind}s`);
			await page.getByRole('button', { name: `Create ${kind}` }).first().click();
			const dialog = page.getByRole('dialog', { name: `Create a ${kind}` });
			if (envs.length > 1) await dialog.getByLabel('Environment').selectOption(envs[0].id);
			await dialog.getByLabel('Name').fill(name);
			await dialog.getByRole('button', { name: `Create ${kind}` }).click();
			await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(`Created ${name}`);
			const row = page.getByRole('row', { name: new RegExp(name) });
			await expect(row).toBeVisible();

			await row.getByRole('button', { name: `Actions for ${name}` }).click();
			await page.getByRole('menuitem', { name: 'Remove…' }).click();
			const confirm = page.getByRole('alertdialog', { name: `Remove ${name}?` });
			await expect(confirm).toContainText(kind === 'volume' ? 'deleted permanently' : 'The network is deleted');
			await confirm.getByLabel(`Type ${name} to confirm`).fill(name);
			await confirm.getByRole('button', { name: kind === 'volume' ? 'Remove volume and its data' : 'Remove network' }).click();
			await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(`Removed ${name}`);
			await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0);
		}
	});

	test('pull an image, create a container from it, remove it (#19 match preview, #25 no implicit pull)', async ({ page }) => {
		await signIn(page);
		const envs = await onlineEnvironments(page);
		test.skip(envs.length === 0, 'needs a connected environment');
		const env = envs[0];
		const run = Date.now().toString(36);
		// A tag no earlier run pulled (the devstack's fake Engine pulls any tag).
		const image = process.env.E2E_PULL_IMAGE ?? `busybox:e2e-${run}`;
		const name = `b4-e2e-${run}`;

		// The create form knows the image is missing and offers the pull.
		await page.goto(`/containers/new?environment=${env.id}&image=${image}`);
		await expect(page.getByText(`${image} is not on ${env.name}`)).toBeVisible();
		await page.getByRole('button', { name: 'Pull image' }).click();
		const pull = page.getByRole('dialog', { name: 'Pull an image' });
		await expect(pull.getByRole('textbox', { name: 'Image' })).toHaveValue(image);
		// The match preview names the connection (or anonymous access) before anything runs.
		await expect(pull.getByText(/^Uses |^Pulls anonymously/)).toBeVisible();
		await pull.getByRole('button', { name: 'Pull image' }).click();
		await expect(pull.getByText('Succeeded', { exact: true }).first()).toBeVisible({ timeout: 20_000 });
		await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(`Pulled ${image}`);
		await pull.getByRole('button', { name: 'Done' }).click();
		await expect(page.getByText(`${image} is not on ${env.name}`)).toBeHidden();

		await page.getByRole('textbox', { name: 'Name' }).fill(name);
		await page.getByLabel('Variables').fill('GREETING=hello');
		await page.getByRole('button', { name: 'Add port' }).click();
		await page.getByLabel('Container port').fill('8080');
		await page.getByRole('button', { name: 'Create container' }).click();
		await expect(page).toHaveURL(new RegExp(`/containers/${env.id}/${name}$`), { timeout: 20_000 });
		await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(`Created ${name}`);
		// Environment variables: names only.
		await expect(page.getByText('GREETING')).toBeVisible();
		await expect(page.getByText('hello')).toHaveCount(0);

		await page.getByRole('button', { name: 'More actions' }).click();
		await page.getByRole('menuitem', { name: 'Remove…' }).click();
		const confirm = page.getByRole('alertdialog', { name: `Remove ${name}?` });
		await confirm.getByLabel(`Type ${name} to confirm`).fill(name);
		await confirm.getByRole('button', { name: /Remove container|Stop and remove/ }).click();
		await expect(page).toHaveURL(/\/containers$/, { timeout: 20_000 });
		await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0);
	});

	test('volume migration shows the preflight before anything is copied (#35)', async ({ page }) => {
		await signIn(page);
		const envs = await onlineEnvironments(page);
		test.skip(envs.length < 2, 'needs two connected environments');
		const vol = await findIn<Named & { inUse: boolean }>(page, 'volumes', (v) => !v.protection && v.inUse);
		test.skip(!vol, 'needs an unprotected volume in use');
		await page.goto(`/volumes/${vol!.environmentId}/${vol!.name}/migrate`);
		await expect(page.getByRole('heading', { name: 'Destination' })).toBeVisible();
		await expect(page.getByText(`Containers are using ${vol!.name}`)).toBeVisible();
		await page.getByRole('button', { name: 'Next' }).click();
		await expect(page.getByRole('heading', { name: 'Check' })).toBeVisible();
		// Nothing was copied: the source is intact and the start waits for the preflight.
		const blocked = page.getByText("The migration can't start yet");
		if (await blocked.isVisible()) await expect(page.getByRole('button', { name: 'Migrate volume' })).toBeDisabled();
		else await expect(page.getByText('Expected time')).toBeVisible();
	});

	test('narrow layout: stacked rows and full-screen dialogs', async ({ browser, baseURL }) => {
		const ctx = await browser.newContext({ baseURL, viewport: { width: 390, height: 844 }, ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === '1' });
		const page = await ctx.newPage();
		await signIn(page);
		test.skip((await onlineEnvironments(page)).length === 0, 'needs a connected environment');
		for (const path of ['/containers', '/images', '/volumes', '/networks']) {
			await page.goto(path);
			await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
			// No horizontal scrolling on a phone.
			const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
			expect(overflow, path).toBeLessThanOrEqual(1);
			if (shots) await page.screenshot({ path: `${shots}/b4${path.replace('/', '-')}-390.png`, fullPage: true });
		}
		await ctx.close();
	});
});

test.describe('Access (#17)', () => {
	test('a user without grants gets the denied state, not an empty list', async ({ page }) => {
		const guest = process.env.E2E_UI_GUEST ?? '';
		const guestPassword = process.env.E2E_UI_GUEST_PASSWORD ?? '';
		test.skip(!guest || !guestPassword, 'set E2E_UI_GUEST / E2E_UI_GUEST_PASSWORD (the devstack has guest)');
		await page.goto('/sign-in');
		await page.getByLabel('Username').fill(guest);
		await page.getByLabel('Password', { exact: true }).fill(guestPassword);
		await page.getByRole('button', { name: 'Sign in', exact: true }).click();
		await expect(page).not.toHaveURL(/\/sign-in/);
		for (const path of ['/containers', '/images', '/volumes', '/networks', '/builds', '/registries']) {
			await page.goto(path);
			await expect(page.getByTestId('denied-state'), path).toBeVisible();
		}
	});
});

test.describe('Builds (#33)', () => {
	test('save, list and delete a build definition; build arguments carry the history warning', async ({ page }) => {
		await signIn(page);
		const envs = await apiGet<{ items: Env[] }>(page, '/api/v1/environments?limit=200');
		test.skip(envs.items.length === 0 || !(await apiGet<{ owner: boolean }>(page, '/api/v1/me/permissions')).owner, 'needs an environment and the owner');
		const name = `b4-e2e-${Date.now().toString(36)}`;
		await page.goto('/builds/definitions');
		await page.getByRole('button', { name: 'New definition' }).first().click();
		const dialog = page.getByRole('dialog', { name: 'New build definition' });
		await dialog.getByLabel('Name', { exact: true }).fill(name);
		await dialog.getByLabel('Repository URL').fill('git@github.com:acme/app.git');
		await expect(dialog.getByText('SSH Git URLs are not supported in v1.')).toBeVisible();
		await dialog.getByLabel('Repository URL').fill('https://github.com/acme/app.git');
		await dialog.getByLabel('Image names').fill('registry.example.com/acme/app:e2e');
		await dialog.getByLabel('Build arguments').fill('NODE_VERSION=22');
		await expect(dialog.getByText('Build arguments are visible in the image history')).toBeVisible();
		await dialog.getByRole('button', { name: 'Save definition' }).click();
		await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(`Saved the build definition ${name}`);
		const row = page.getByRole('row', { name: new RegExp(name) });
		await expect(row).toContainText('github.com/acme/app');
		await row.getByRole('button', { name: `Actions for ${name}` }).click();
		await page.getByRole('menuitem', { name: 'Delete…' }).click();
		await page.getByRole('alertdialog', { name: `Delete ${name}?` }).getByRole('button', { name: 'Delete definition' }).click();
		await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(`Deleted ${name}`);
		await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0);
	});
});

test.describe('Registries (#19)', () => {
	test('the owner adds a connection after a step-up; the secret is never shown again', async ({ page }) => {
		await signIn(page);
		test.skip(!(await apiGet<{ owner: boolean }>(page, '/api/v1/me/permissions')).owner, `${owner} is not the owner`);
		const run = Date.now().toString(36);
		const name = `B4 e2e ${run}`;
		const host = `registry-${run}.e2e.invalid:5000`;
		const secret = `b4-e2e-secret-${Date.now()}`;
		await page.goto('/registries');
		await page.getByRole('button', { name: 'Add connection' }).first().click();
		const dialog = page.getByRole('dialog', { name: 'Add a registry connection' });
		await dialog.getByLabel('Name', { exact: true }).fill(name);
		await dialog.getByLabel('Registry host').fill(host);
		await dialog.getByLabel('Username', { exact: true }).fill('puller');
		await dialog.getByRole('textbox', { name: 'Access token' }).fill(secret);
		await dialog.getByLabel('Repositories').fill('team/*');
		await dialog.getByRole('button', { name: 'Add connection' }).click();

		// A fresh session needs the password again (step-up) unless it just signed in.
		const stepUp = page.getByRole('dialog', { name: "Confirm it's you" });
		const toast = page.getByRole('status', { name: 'Notifications' });
		await expect(stepUp.or(toast.getByText(`Added ${name}`))).toBeVisible();
		if (await stepUp.isVisible()) {
			await stepUp.getByLabel('Password', { exact: true }).fill(password);
			await stepUp.getByRole('button', { name: 'Confirm' }).click();
		}
		await expect(toast).toContainText(`Added ${name}`);
		const row = page.getByRole('row', { name: new RegExp(name) });
		await expect(row).toContainText(`${host}/team/*`);
		await expect(row).toContainText(/fp_/);
		expect(await page.content()).not.toContain(secret);

		// Which connection does an image use?
		await page.getByRole('link', { name: 'Which connection?' }).click();
		await page.getByLabel('Image').fill(`${host}/team/app:1.0`);
		await expect(page.getByText(`Uses ${name}`)).toBeVisible();

		// Delete it again (type-to-confirm).
		await page.getByRole('link', { name: 'Registry connections' }).click();
		await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: `Actions for ${name}` }).click();
		await page.getByRole('menuitem', { name: 'Delete…' }).click();
		const del = page.getByRole('alertdialog', { name: `Delete ${name}?` });
		await del.getByLabel(`Type ${name} to confirm`).fill(name);
		await del.getByRole('button', { name: 'Delete connection' }).click();
		await expect(toast).toContainText(`Deleted ${name}`);
		await expect(page.getByRole('row', { name: new RegExp(name) })).toHaveCount(0);
	});
});
