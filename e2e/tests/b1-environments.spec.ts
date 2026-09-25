// #22 track B1 end to end: dashboard, environments (list, add/revoke an
// enrollment token, detail with metrics, system, agents, edit), jobs and
// job detail, schedules, notices.
//
// Locally against the seeded devstack (homelab and nas online, edge
// offline, three jobs incl. a partial prune, a weekly Europe/Berlin update
// check) — every step runs:
//
//   npm --prefix web run build && go run ./test/devstack -addr 127.0.0.1:8080
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/b1-environments.spec.ts
//
// In CI (behind the TLS proxies, a fresh manager without agents) the steps
// that need environments, jobs or schedules check the empty states instead.
// E2E_SCREENSHOTS_DIR saves review screenshots (1440x900 and 390x844).
import { expect, test, type Page } from '@playwright/test';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const shots = process.env.E2E_SCREENSHOTS_DIR ?? '';

interface Env {
	id: string;
	name: string;
	online: boolean;
}

async function shot(page: Page, name: string) {
	if (shots) await page.screenshot({ path: `${shots}/b1-${name}.png`, fullPage: false });
}

async function signIn(page: Page) {
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
	await expect(page.getByRole('main')).toBeVisible();
}

async function environments(page: Page): Promise<Env[]> {
	const res = await page.request.get('/api/v1/environments?limit=200');
	return (await res.json()).items as Env[];
}

test.describe.serial('B1: dashboard, environments, jobs, schedules', () => {
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

	test.beforeEach(async ({ page }) => {
		await signIn(page);
	});

	test('dashboard: environments with status, usage and live updates', async ({ page }) => {
		await page.goto('/');
		await expect(page.getByRole('heading', { level: 1, name: 'Dashboard' })).toBeVisible();
		const summary = page.getByRole('region', { name: 'Summary' });
		await expect(summary.getByRole('group', { name: 'Environments online' })).toBeVisible();
		await expect(summary.getByRole('group', { name: 'Failed jobs (24 h)' })).toBeVisible();
		const envs = await environments(page);
		if (!envs.length) {
			await expect(page.getByText('No environments yet.')).toBeVisible();
			await expect(
				page.getByRole('main').getByRole('link', { name: 'Add environment' }).first()
			).toBeVisible();
			return;
		}
		for (const e of envs) {
			const card = page.getByRole('article', { name: e.name });
			await expect(card).toBeVisible();
			await expect(card).toContainText(e.online ? 'Online' : 'Offline');
		}
		const online = envs.find((e) => e.online);
		if (online) {
			const card = page.getByRole('article', { name: online.name });
			// Usage is there from the first load (#22 bug: "—" and "0 B / 0 B").
			await expect(card.getByText(/^\d+(\.\d)?%$/).first()).toBeVisible();
			await expect(card).toContainText(/Memory\s*[\d.]+ [KMGT]?B\s*\/\s*[\d.]+ [KMGT]?B/);
			await expect(card.locator('canvas').first()).toBeVisible(); // sparkline drawn
			await expect(summary.getByRole('group', { name: 'Memory in use' })).not.toContainText(
				'0 B / 0 B'
			);
			// The open dashboard updates live: a new metrics sample (every 10 s)
			// reaches the view through the live stream without a reload.
			const cpu = card.getByText(/^\d+(\.\d)?%$/).first();
			// Two successive changes: the first could come from the stream's
			// initial refresh, the second only from a metrics event (the
			// overview's safety-net poll is 60 s).
			for (let i = 0; i < 2; i++) {
				const before = await cpu.textContent();
				await expect(async () => expect(await cpu.textContent()).not.toBe(before)).toPass({
					timeout: 30_000
				});
			}
		}
		const offline = envs.find((e) => !e.online);
		if (offline)
			await expect(page.getByRole('article', { name: offline.name })).toContainText(
				'Values are the last known.'
			);
		await shot(page, 'dashboard-1440');
	});

	test('add environment: one-time token, install commands, revoke', async ({ page }) => {
		await page.goto('/environments');
		await expect(page.getByRole('heading', { level: 1, name: 'Environments' })).toBeVisible();
		await page.getByRole('link', { name: 'Add environment' }).first().click();
		await expect(page).toHaveURL(/\/environments\/add$/);
		await page.getByLabel('Environment name').fill('e2e-host');
		await page.getByRole('button', { name: 'Create enrollment token' }).click();
		await expect(page.getByText('Waiting for the agent to connect.')).toBeVisible();
		const commands = page.getByRole('tablist', { name: 'Install commands' });
		await expect(
			commands.getByRole('tab', { name: 'On another Docker host', exact: true })
		).toHaveAttribute('aria-selected', 'true');
		await expect(page.getByLabel('Agent on another Docker host: command')).toContainText(
			"DOCKYARD_ENVIRONMENT_NAME='e2e-host'"
		);
		await expect(page.getByLabel('enrollment token', { exact: true })).toContainText(/^dye_/);
		// Keyboard: the Compose variant.
		await commands.getByRole('tab', { name: 'On another Docker host', exact: true }).focus();
		await page.keyboard.press('ArrowRight');
		await expect(page.getByText(/DOCKYARD_ENROLLMENT_TOKEN=dye_/)).toBeVisible();
		await shot(page, 'add-environment-1440');
		// The token is no API data kept by the browser.
		const stored = await page.evaluate(
			() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage })
		);
		expect(stored).not.toContain('dye_');

		await page.getByRole('button', { name: 'Revoke token' }).click();
		await expect(page).toHaveURL(/\/environments$/);
		await expect(page.getByRole('status', { name: 'Notifications' })).toContainText(
			'Revoked enrollment token'
		);
		const list = await (await page.request.get('/api/v1/agent-enrollments?limit=200')).json();
		expect(
			list.items.find((e: { environmentName?: string }) => e.environmentName === 'e2e-host')
				?.state
		).toBe('revoked');
	});

	test('environment detail: metrics with gaps, system, agents, edit', async ({ page }) => {
		const envs = await environments(page);
		test.skip(!envs.length, 'no enrolled environments on this manager');
		const env = envs.find((e) => e.online) ?? envs[0];
		await page.goto('/environments');
		await page
			.getByRole('table', { name: 'Active environments' })
			.getByRole('link', { name: env.name })
			.click();
		await expect(page.getByRole('heading', { level: 1, name: env.name })).toBeVisible();
		for (const title of ['CPU', 'Memory', 'Network', 'Load'])
			await expect(page.getByRole('figure', { name: title })).toBeVisible();
		await page.getByLabel('Range').selectOption('24h');
		await expect(page.getByText(/one point every/)).toBeVisible();
		await shot(page, 'environment-1440');

		await page.getByRole('tab', { name: 'System' }).click();
		await expect(page).toHaveURL(/\?tab=system$/);
		await expect(page.getByRole('heading', { name: 'Docker Engine' })).toBeVisible();
		await expect(page.getByRole('heading', { name: 'Agent' })).toBeVisible();

		await page.getByRole('tab', { name: 'Agents' }).click();
		await expect(page.getByRole('table', { name: `Agents of ${env.name}` })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Rotate credential' })).toBeVisible();

		// Edit: rename and back (If-Match revisions).
		await page.getByRole('button', { name: 'Edit' }).click();
		const dialog = page.getByRole('dialog', { name: `Edit ${env.name}` });
		await dialog.getByRole('textbox', { name: /^Name/ }).fill(`${env.name}-renamed`);
		await dialog.getByRole('button', { name: 'Save changes' }).click();
		await expect(
			page.getByRole('heading', { level: 1, name: `${env.name}-renamed` })
		).toBeVisible();
		await page.getByRole('button', { name: 'Edit' }).click();
		await page.getByRole('dialog').getByRole('textbox', { name: /^Name/ }).fill(env.name);
		await page.getByRole('dialog').getByRole('button', { name: 'Save changes' }).click();
		await expect(page.getByRole('heading', { level: 1, name: env.name })).toBeVisible();

		// Archive shows the removal preview; cancel keeps everything.
		await page.getByRole('button', { name: 'More actions' }).click();
		await page.getByRole('menuitem', { name: 'Archive environment' }).click();
		const archive = page.getByRole('alertdialog', { name: `Archive ${env.name}` });
		await expect(archive).toContainText('Nothing on the host changes');
		await expect(archive.getByRole('button', { name: 'Archive environment' })).toBeDisabled();
		await archive.getByRole('button', { name: 'Cancel' }).click();
		await expect(archive).toBeHidden();
	});

	test('offline environment: last known state and the offline interval as a gap', async ({
		page
	}) => {
		const offline = (await environments(page)).find((e) => !e.online);
		test.skip(!offline, 'no offline environment on this manager');
		await page.goto(`/environments/${offline!.id}`);
		await expect(page.getByText(`${offline!.name} is offline`)).toBeVisible();
		const cpu = page.getByRole('figure', { name: 'CPU' });
		await expect(cpu.getByRole('list', { name: 'CPU: time without samples' })).toContainText(
			/No samples (since|\d)/
		);
		await expect(page.getByRole('button', { name: /^Notices/ })).toBeVisible();
		await page.getByRole('button', { name: /^Notices/ }).click();
		await expect(
			page
				.getByRole('dialog', { name: 'Notices' })
				.or(page.getByText(`${offline!.name} is offline`).last())
		).toBeVisible();
	});

	test('jobs: filters in the URL, detail with items, recovery and targets', async ({ page }) => {
		await page.goto('/jobs');
		await expect(page.getByRole('heading', { level: 1, name: 'Jobs' })).toBeVisible();
		const jobs = (await (await page.request.get('/api/v1/jobs?limit=50')).json()).items as {
			id: string;
			state: string;
		}[];
		if (!jobs.length) {
			await expect(page.getByText('No jobs yet.')).toBeVisible();
			return;
		}
		await expect(page.getByRole('table', { name: 'Jobs' })).toBeVisible();
		await page.getByLabel('State').selectOption('problems');
		await expect(page).toHaveURL(/state=problems/);
		const problems = jobs.filter((j) => ['failed', 'partial', 'interrupted'].includes(j.state));
		const rows = page.getByRole('table', { name: 'Jobs' }).getByRole('row');
		await expect(rows).toHaveCount(problems.length ? problems.length + 1 : 2);
		await page.getByLabel('Origin').selectOption('scheduled');
		await expect(page).toHaveURL(/origin=scheduled/);
		await page.getByRole('button', { name: 'Clear filters' }).first().click();
		await expect(page).not.toHaveURL(/state=/);
		await shot(page, 'jobs-1440');

		const partial = jobs.find((j) => j.state === 'partial');
		if (partial) {
			await page.goto(`/jobs/${partial.id}`);
			await expect(page.getByText('Partly failed').first()).toBeVisible();
			await expect(page.getByRole('list', { name: 'Items' })).toBeVisible();
			await expect(page.getByText(/^\d+ of \d+ items failed/)).toBeVisible();
			await expect(page.getByRole('heading', { name: 'Timeline' })).toBeVisible();
			await expect(page.getByRole('button', { name: 'Cancel job' })).toHaveCount(0); // finished
		}
	});

	test('schedules: next runs with DST notes and history', async ({ page }) => {
		await page.goto('/schedules');
		await expect(page.getByRole('heading', { level: 1, name: 'Schedules' })).toBeVisible();
		const list = (await (await page.request.get('/api/v1/schedules')).json()).items as {
			policyName: string;
			timeZone: string;
		}[];
		if (!list.length) {
			await expect(page.getByText('No scheduled policies yet.')).toBeVisible();
			return;
		}
		await expect(page.getByRole('table', { name: 'Schedules' })).toBeVisible();
		await page.getByRole('button', { name: 'Details' }).first().click();
		const drawer = page.getByRole('dialog');
		await expect(drawer.getByRole('heading', { name: 'Next runs' })).toBeVisible();
		await expect(drawer.getByRole('heading', { name: 'Recent runs' })).toBeVisible();
		if (list.some((s) => s.timeZone === 'Europe/Berlin')) {
			await page.keyboard.press('Escape');
			const row = page.getByRole('row').filter({ hasText: 'Europe/Berlin' }).first();
			await row.getByRole('button', { name: 'Details' }).click();
			// The seeded 02:30 Sunday check lands on the repeated October hour.
			await expect(
				page.getByRole('dialog').getByText('Clocks move back', { exact: true })
			).toBeVisible();
		}
		await shot(page, 'schedules-1440');
	});

	test('narrow layout: stacked cards and tables at 390 px', async ({ page }) => {
		await page.setViewportSize({ width: 390, height: 844 });
		await page.goto('/');
		await expect(page.getByRole('heading', { level: 1, name: 'Dashboard' })).toBeVisible();
		const overflow = await page.evaluate(
			() => document.documentElement.scrollWidth - window.innerWidth
		);
		expect(overflow).toBeLessThanOrEqual(1);
		await shot(page, 'dashboard-390');
		await page.goto('/jobs');
		await expect(page.getByRole('heading', { level: 1, name: 'Jobs' })).toBeVisible();
		expect(
			await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
		).toBeLessThanOrEqual(1);
		await shot(page, 'jobs-390');
	});
});
