// Live synchronization in the real screens (#23 with the #22 UI): open
// views converge without a reload on changes made through DockYard in
// another session, on changes made directly through Docker on either host,
// after a network outage, and on a permission revocation (the view clears).
// tests/live.spec.ts covers the stream contract through the TLS proxies;
// tests/ui-files.spec.ts the external file edit in the file manager.
//
// Locally against the seeded devstack with its control listener (it
// changes the fake Engines directly, like `docker stop` on the host):
//   go run ./test/devstack -addr 127.0.0.1:8080 -control 127.0.0.1:8081
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner E2E_DEVSTACK_CONTROL=http://127.0.0.1:8081 \
//     npx playwright test tests/live-ui.spec.ts
// Needs the seeded environments (homelab, nas) and the Restricted account
// (E2E_UI_GUEST / E2E_UI_GUEST_PASSWORD, devstack defaults); skipped
// without them. The steps change shared state, so they run in order.
import { expect, test, type Browser, type Page } from '@playwright/test';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const guest = process.env.E2E_UI_GUEST ?? 'guest';
const guestPassword = process.env.E2E_UI_GUEST_PASSWORD ?? 'dockyard-devstack-guest';
const control = process.env.E2E_DEVSTACK_CONTROL ?? '';

type Env = { id: string; name: string; online: boolean };

async function signIn(page: Page, user = owner, pass = password) {
	const status = await (await page.request.get('/api/v1/setup/status')).json();
	test.skip(!status.setupComplete, 'setup is still open on this manager');
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(user);
	await page.getByLabel('Password', { exact: true }).fill(pass);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
}

async function session(
	browser: Browser,
	baseURL: string | undefined,
	user?: string,
	pass?: string
) {
	const ctx = await browser.newContext({
		baseURL,
		viewport: { width: 1440, height: 900 },
		ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === '1'
	});
	const page = await ctx.newPage();
	await signIn(page, user, pass);
	return page;
}

async function environments(page: Page): Promise<Env[]> {
	return (await (await page.request.get('/api/v1/environments')).json()).items as Env[];
}

/**
 * Marks the document so a later check proves the page never reloaded
 * (a reload or full navigation drops the marker).
 */
async function markNoReload(page: Page) {
	await page.evaluate(() => ((window as unknown as { __noReload: number }).__noReload = 1));
}

async function assertNoReload(page: Page) {
	expect(
		await page.evaluate(() => (window as unknown as { __noReload?: number }).__noReload)
	).toBe(1);
}

/** The status badge in the page header. */
function headerStatus(page: Page) {
	return page.locator('header.page-header').first();
}

async function engineAction(env: string, container: string, action: 'stop' | 'start') {
	const res = await fetch(`${control}/engines/${env}/containers/${container}/${action}`, {
		method: 'POST'
	});
	expect(res.status, await res.text()).toBe(204);
}

test.describe.serial('live views (#23)', () => {
	test('two sessions converge on a change made through DockYard', async ({
		browser,
		baseURL
	}) => {
		const a = await session(browser, baseURL);
		const homelab = (await environments(a)).find((e) => e.name === 'homelab' && e.online);
		test.skip(!homelab, 'needs the devstack');
		const b = await session(browser, baseURL);
		const url = `/containers/${homelab!.id}/homeassistant`;
		await a.goto(url);
		await b.goto(url);
		for (const p of [a, b]) await expect(headerStatus(p).getByText('Running')).toBeVisible();
		await markNoReload(b);

		// Session A stops the container; session B sees it without a reload.
		await a.getByRole('button', { name: 'Stop', exact: true }).click();
		await a.getByRole('alertdialog').getByRole('button', { name: 'Stop container' }).click();
		await expect(headerStatus(b).getByText(/Exited|Stopped/)).toBeVisible({ timeout: 15_000 });
		await expect(headerStatus(a).getByText(/Exited|Stopped/)).toBeVisible({ timeout: 15_000 });

		// And back.
		await a.getByRole('button', { name: 'Start', exact: true }).click();
		await expect(headerStatus(b).getByText('Running')).toBeVisible({ timeout: 15_000 });
		await assertNoReload(b);
		await a.context().close();
		await b.context().close();
	});

	test('changes made directly through Docker on either host appear without a reload', async ({
		browser,
		baseURL
	}) => {
		test.skip(!control, 'needs the devstack control listener (E2E_DEVSTACK_CONTROL)');
		const page = await session(browser, baseURL);
		const envs = await environments(page);
		const homelab = envs.find((e) => e.name === 'homelab' && e.online);
		const nas = envs.find((e) => e.name === 'nas' && e.online);
		test.skip(!homelab || !nas, 'needs homelab and nas online');
		await page.goto('/containers');
		const table = page.getByRole('table').first();
		const row = (name: string) => table.getByRole('row', { name: new RegExp(`\\b${name}\\b`) });
		await expect(row('pihole').getByText('Running')).toBeVisible();
		await expect(row('syncthing').getByText('Running')).toBeVisible();
		await markNoReload(page);

		// `docker stop` on homelab, then on nas, outside DockYard.
		await engineAction('homelab', 'pihole', 'stop');
		await expect(row('pihole').getByText('Exited')).toBeVisible({ timeout: 15_000 });
		await engineAction('nas', 'syncthing', 'stop');
		await expect(row('syncthing').getByText('Exited')).toBeVisible({ timeout: 15_000 });

		// An open detail page follows too.
		await page.goto(`/containers/${nas!.id}/syncthing`);
		await expect(headerStatus(page).getByText('Exited')).toBeVisible();
		await markNoReload(page);
		await engineAction('nas', 'syncthing', 'start');
		await expect(headerStatus(page).getByText('Running')).toBeVisible({ timeout: 15_000 });
		await engineAction('homelab', 'pihole', 'start');
		await assertNoReload(page);
		await page.context().close();
	});

	test('after a network outage the banner shows, then the view catches up', async ({
		browser,
		baseURL
	}) => {
		test.skip(!control, 'needs the devstack control listener (E2E_DEVSTACK_CONTROL)');
		const page = await session(browser, baseURL);
		const homelab = (await environments(page)).find((e) => e.name === 'homelab' && e.online);
		test.skip(!homelab, 'needs the devstack');
		await page.goto(`/containers/${homelab!.id}/pihole`);
		await expect(headerStatus(page).getByText('Running')).toBeVisible();
		await markNoReload(page);

		await page.context().setOffline(true);
		// The indicator reports the retry at once; the banner follows after
		// the stream has been down for 5 s (never on a short blip).
		await expect(page.getByRole('status', { name: 'Live updates' })).toContainText(
			/Reconnecting|paused/,
			{ timeout: 15_000 }
		);
		await expect(page.getByText('Live updates are disconnected')).toBeVisible({
			timeout: 20_000
		});
		// A change happens on the host while this tab is offline (missed
		// notifications).
		await engineAction('homelab', 'pihole', 'stop');

		await page.context().setOffline(false);
		await expect(page.getByText('Live updates are disconnected')).toBeHidden({
			timeout: 30_000
		});
		await expect(headerStatus(page).getByText('Exited')).toBeVisible({ timeout: 30_000 });
		await assertNoReload(page);
		await engineAction('homelab', 'pihole', 'start');
		await page.context().close();
	});

	test('a permission revocation clears the open view', async ({ browser, baseURL }) => {
		const admin = await session(browser, baseURL);
		const users = (await (await admin.request.get('/api/v1/users')).json()).items as {
			id: string;
			username: string;
		}[];
		const g = users.find((u) => u.username === guest);
		const silo = (
			(await (await admin.request.get('/api/v1/stacks')).json()).items as {
				id: string;
				name: string;
			}[]
		).find((s) => s.name === 'silo');
		test.skip(!g || !silo, 'needs the devstack guest account and the Silo stack');

		// Permission changes need a recent step-up.
		const stepUp = await admin.request.post('/api/v1/auth/step-ups', {
			data: { password }
		});
		expect(stepUp.ok(), await stepUp.text()).toBe(true);
		const put = async (rules: unknown[]) => {
			const cur = await admin.request.get(`/api/v1/users/${g!.id}/permissions`);
			const res = await admin.request.put(`/api/v1/users/${g!.id}/permissions`, {
				headers: { 'If-Match': cur.headers()['etag'] ?? '' },
				data: { rules }
			});
			expect(res.ok(), await res.text()).toBe(true);
		};
		await put([
			{
				capability: 'stack.read',
				effect: 'allow',
				scope: { kind: 'resource', resourceType: 'stack', resourceId: silo!.id }
			}
		]);
		try {
			const viewer = await session(browser, baseURL, guest, guestPassword);
			await viewer.goto(`/stacks/${silo!.id}`);
			await expect(viewer.getByRole('heading', { level: 1, name: 'Silo' })).toBeVisible();
			await expect(
				viewer
					.getByRole('navigation', { name: 'Main' })
					.getByRole('link', { name: 'Stacks' })
			).toBeVisible();
			await markNoReload(viewer);

			// The owner takes the grant away: the open page clears its data at
			// once and shows that the stack is gone for this user.
			await put([]);
			await expect(viewer.getByRole('heading', { level: 1, name: 'Silo' })).toBeHidden({
				timeout: 15_000
			});
			await expect(
				viewer.getByText(/does not exist or you can.t see it/).first()
			).toBeVisible({ timeout: 15_000 });
			await expect(
				viewer
					.getByRole('navigation', { name: 'Main' })
					.getByRole('link', { name: 'Stacks' })
			).toBeHidden();
			await assertNoReload(viewer);
			await viewer.context().close();
		} finally {
			await put([]);
			await admin.context().close();
		}
	});
});
