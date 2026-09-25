// The log viewer end to end (#8, #22 track B3): a stack's service logs
// merged and coloured by service, service filter chips, follow (live SSE),
// timestamps, search, clear, download, the pop-out window and the logs
// drawer on the Files tab; one container's logs.
//
// Needs a stack whose containers write logs. Locally the Docker-free
// devstack provides one ("silo", logs every 4 s; docs/development.md):
//
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/ui-logs.spec.ts
//
// E2E_LOGS_STACK (default "silo") names the stack. Without it the spec is
// skipped (the proxy-only CI stack has no stacks).
import { expect, test, type Page } from '@playwright/test';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const stackName = process.env.E2E_LOGS_STACK ?? 'silo';
const shots = process.env.E2E_SCREENSHOTS_DIR ?? '';

interface StackRef {
	id: string;
	environmentId: string;
}

async function signIn(page: Page) {
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
}

async function findStack(page: Page): Promise<StackRef | null> {
	const res = await page.request.get('/api/v1/stacks?limit=200');
	if (!res.ok()) return null;
	const s = (
		(await res.json()).items as { id: string; name: string; environmentId: string }[]
	).find((x) => x.name === stackName);
	return s ? { id: s.id, environmentId: s.environmentId } : null;
}

const lines = (page: Page) => page.getByRole('log', { name: /lines$/ });

test.describe('log viewer', () => {
	let stack: StackRef | null = null;

	test.beforeEach(async ({ page }) => {
		await signIn(page);
		stack ??= await findStack(page);
		test.skip(!stack, `no stack "${stackName}" on this manager (the devstack has one)`);
	});

	test('stack logs: services merged, filter chips, search, follow, timestamps, clear, download', async ({
		page
	}) => {
		const services = (
			await (await page.request.get(`/api/v1/stacks/${stack!.id}/services`)).json()
		).services as { name: string }[];
		await page.goto(`/stacks/${stack!.id}/logs`);
		const viewer = page.getByRole('region', { name: /^Logs of / });
		await expect(viewer).toBeVisible();
		await expect(lines(page).locator('.line').first()).toBeVisible({ timeout: 15_000 });
		// Every service has a chip; its lines carry the service name.
		const chips = viewer.getByRole('group', { name: 'Services' });
		for (const s of services)
			await expect(chips.getByRole('button', { name: s.name })).toBeVisible();
		const first = services[0].name;
		await expect(lines(page)).toContainText(first);
		await chips.getByRole('button', { name: first, exact: true }).click();
		await expect(chips.getByRole('button', { name: first, exact: true })).toHaveAttribute(
			'aria-pressed',
			'false'
		);
		await expect(
			lines(page)
				.locator('.src')
				.filter({ hasText: new RegExp(`^${first}$`) })
		).toHaveCount(0);
		await chips.getByRole('button', { name: 'All services' }).click();

		// Search highlights and counts.
		await viewer.getByRole('searchbox', { name: 'Search logs' }).fill('a');
		await expect(viewer.getByText(/^\d+ match(es)?$/)).toBeVisible();
		await expect(lines(page).locator('mark').first()).toBeVisible();
		await viewer.getByRole('searchbox', { name: 'Search logs' }).fill('');

		// Timestamps off and on.
		await viewer.getByRole('switch', { name: 'Timestamps' }).click();
		await expect(lines(page).locator('.ts')).toHaveCount(0);
		await viewer.getByRole('switch', { name: 'Timestamps' }).click();

		// Clear view, then new lines keep arriving while following (live SSE).
		await viewer.getByRole('button', { name: 'Clear view' }).click();
		await expect(lines(page).locator('.line')).toHaveCount(0);
		await expect(lines(page).locator('.line').first()).toBeVisible({ timeout: 20_000 });
		await expect(viewer.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();

		// Follow off pauses the stream.
		await viewer.getByRole('switch', { name: 'Follow' }).click();
		await expect(viewer.getByText('Paused: turn on Follow to continue')).toBeVisible();
		await viewer.getByRole('switch', { name: 'Follow' }).click();

		const download = page.waitForEvent('download');
		await viewer.getByRole('button', { name: 'Download shown lines' }).click();
		expect((await download).suggestedFilename()).toMatch(/\.log$/);
		if (shots) await page.screenshot({ path: `${shots}/logs-1440.png` });
	});

	test('pop-out window and the drawer on the Files tab', async ({ page, context }) => {
		await page.goto(`/stacks/${stack!.id}/logs`);
		const popup = context.waitForEvent('page');
		await page.getByRole('button', { name: 'Open in a new window' }).click();
		const win = await popup;
		await expect(win).toHaveURL(/\/popout\/logs\?stack=/);
		await expect(win.getByRole('region', { name: /^Logs of / })).toBeVisible();
		await expect(win.getByRole('navigation', { name: 'Main' })).toHaveCount(0); // no app shell
		await expect(win.getByRole('log').locator('.line').first()).toBeVisible({
			timeout: 15_000
		});
		await win.close();

		await page.goto(`/stacks/${stack!.id}/files`);
		const list = page.getByRole('grid', { name: /^Files in / });
		const before = (await list.boundingBox())!.width;
		await page.getByRole('button', { name: 'Show logs' }).click();
		const drawer = page.getByRole('region', { name: 'Logs drawer' });
		await expect(drawer).toBeVisible();
		// The drawer takes height, never width.
		expect(Math.round((await list.boundingBox())!.width)).toBe(Math.round(before));
		const handle = drawer.getByRole('separator', { name: 'Resize the logs drawer' });
		const h0 = (await drawer.boundingBox())!.height;
		await handle.focus();
		await page.keyboard.press('ArrowUp');
		await page.keyboard.press('ArrowUp');
		expect((await drawer.boundingBox())!.height).toBeGreaterThan(h0);
		await drawer.getByRole('button', { name: 'Close the logs drawer' }).click();
		await expect(drawer).toHaveCount(0);
	});

	test('one container: logs route', async ({ page }) => {
		const svc = (await (await page.request.get(`/api/v1/stacks/${stack!.id}/services`)).json())
			.services as {
			containers: { name: string; state: string }[];
		}[];
		const c = svc.flatMap((s) => s.containers).find((x) => x.state === 'running');
		test.skip(!c, 'no running container');
		await page.goto(`/containers/${stack!.environmentId}/${c!.name}/logs`);
		await expect(page.getByRole('region', { name: `Logs of ${c!.name}` })).toBeVisible();
		await expect(lines(page).locator('.line').first()).toBeVisible({ timeout: 15_000 });
		// A single container has no service chips.
		await expect(page.getByRole('group', { name: 'Services' })).toHaveCount(0);
	});
});
