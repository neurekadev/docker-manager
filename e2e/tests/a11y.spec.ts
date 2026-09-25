// Accessibility of the whole UI (#22 accessibility floor): axe-core on
// every main route (no serious or critical WCAG 2.1 A/AA violation) and
// keyboard-only use of the shell, a table, a dialog and the file manager.
//
// Locally against the seeded devstack (docs/development.md, "UI devstack"):
//   go run ./test/devstack -addr 127.0.0.1:8080
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner npx playwright test tests/a11y.spec.ts
// In CI (a manager without agents behind each TLS proxy) the pages that
// need seeded objects are skipped; the rest still run.
import { AxeBuilder } from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';

// Justified exceptions (rule id → why). Keep this empty unless a rule is
// wrong for a specific, documented reason; never to hide a real defect.
const EXCEPTIONS: Record<string, string> = {};

async function signIn(page: Page) {
	const status = await (await page.request.get('/api/v1/setup/status')).json();
	test.skip(!status.setupComplete, 'setup is still open on this manager (run ui.spec.ts first)');
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
}

async function items<T>(page: Page, path: string): Promise<T[]> {
	const r = await page.request.get(`/api/v1${path}`);
	if (!r.ok()) return [];
	const body = await r.json();
	return (body.items ?? body) as T[];
}

type Id = { id: string; name?: string; online?: boolean };

/** Every main route with the seeded IDs the manager has (null: skip). */
async function mainRoutes(page: Page): Promise<[string, string | null][]> {
	const envs = await items<Id>(page, '/environments');
	const env = envs.find((e) => e.online) ?? null;
	const stack = (await items<Id>(page, '/stacks'))[0] ?? null;
	const job = (await items<Id>(page, '/jobs?limit=1'))[0] ?? null;
	const user = (await items<Id>(page, '/users'))[0] ?? null;
	const group = (await items<Id>(page, '/groups'))[0] ?? null;
	const bpol = (await items<Id>(page, '/backup-policies'))[0] ?? null;
	const brepo = (await items<Id>(page, '/backup-repositories'))[0] ?? null;
	const backup = (await items<Id>(page, '/backups'))[0] ?? null;
	const upol = (await items<Id>(page, '/update-policies'))[0] ?? null;
	const mpol = (await items<Id>(page, '/maintenance-policies'))[0] ?? null;
	const containers = env ? await items<Id>(page, `/environments/${env.id}/containers`) : [];
	const volumes = env ? await items<Id>(page, `/environments/${env.id}/volumes`) : [];
	const networks = env ? await items<Id>(page, `/environments/${env.id}/networks`) : [];
	const images = env ? await items<Id>(page, `/environments/${env.id}/images`) : [];
	const e = encodeURIComponent;
	const c = containers[0]?.name;
	const v = volumes[0]?.name;
	return [
		['dashboard', '/'],
		['environments', '/environments'],
		['environment', env && `/environments/${env.id}`],
		['add environment', '/environments/add'],
		['stacks', '/stacks'],
		['stack overview', stack && `/stacks/${stack.id}`],
		['stack files', stack && `/stacks/${stack.id}/files`],
		['stack logs', stack && `/stacks/${stack.id}/logs`],
		['stack terminal', stack && `/stacks/${stack.id}/terminal`],
		['stack revisions', stack && `/stacks/${stack.id}/revisions`],
		['stack policies', stack && `/stacks/${stack.id}/policies`],
		['stack activity', stack && `/stacks/${stack.id}/activity`],
		['new stack', '/stacks/new'],
		['discovered stacks', '/stacks/discovered'],
		['containers', '/containers'],
		['container', env && c ? `/containers/${env.id}/${e(c)}` : null],
		['container logs', env && c ? `/containers/${env.id}/${e(c)}/logs` : null],
		['new container', '/containers/new'],
		['images', '/images'],
		['image', env && images[0] ? `/images/${env.id}/${e(images[0].id)}` : null],
		['volumes', '/volumes'],
		['volume', env && v ? `/volumes/${env.id}/${e(v)}` : null],
		['volume files', env && v ? `/volumes/${env.id}/${e(v)}/files` : null],
		['networks', '/networks'],
		['network', env && networks[0] ? `/networks/${env.id}/${e(networks[0].name!)}` : null],
		['builds', '/builds'],
		['build definitions', '/builds/definitions'],
		['registries', '/registries'],
		['git credentials', '/registries/git'],
		['backups', '/backups'],
		['backup', backup && `/backups/${backup.id}`],
		['backup policies', '/backups/policies'],
		['backup policy', bpol && `/backups/policies/${bpol.id}`],
		['new backup policy', '/backups/policies/new'],
		['backup repositories', '/backups/repositories'],
		['backup repository', brepo && `/backups/repositories/${brepo.id}`],
		['updates', '/updates'],
		['update policy', upol && `/updates/${upol.id}`],
		['maintenance', '/maintenance'],
		['maintenance policy', mpol && `/maintenance/${mpol.id}`],
		['jobs', '/jobs'],
		['job', job && `/jobs/${job.id}`],
		['schedules', '/schedules'],
		['access', '/access'],
		['user', user && `/access/users/${user.id}`],
		['groups', '/access/groups'],
		['group', group && `/access/groups/${group.id}`],
		['invitations', '/access/invitations'],
		['settings', '/settings'],
		['profile and security', '/settings/security'],
		['API tokens', '/settings/tokens'],
		['new API token', '/settings/tokens/new'],
		['sign-in policy', '/settings/sign-in'],
		['audit log', '/settings/audit'],
		['diagnostics', '/settings/diagnostics'],
		['design gallery', '/design']
	];
}

/** Serious and critical violations of WCAG 2.1 A/AA on the current page. */
async function audit(page: Page, name: string): Promise<string[]> {
	const result = await new AxeBuilder({ page })
		.withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
		.analyze();
	return result.violations
		.filter((v) => (v.impact === 'serious' || v.impact === 'critical') && !EXCEPTIONS[v.id])
		.map(
			(v) =>
				`${name}: ${v.id} (${v.impact}) ${v.help}\n` +
				v.nodes
					.slice(0, 5)
					.map(
						(n) =>
							`    ${n.target.join(' ')}: ${n.failureSummary?.split('\n')[1] ?? ''}`
					)
					.join('\n')
		);
}

/**
 * Waits until the page shows its title and its loading placeholders are
 * gone (skeletons carry aria-busy). Returns the busy regions still left
 * after the wait (live panels that keep loading, e.g. a log follow).
 */
async function settled(page: Page): Promise<number> {
	await expect(page.locator('h1').first()).toBeVisible({ timeout: 15_000 });
	const busy = page.locator('[aria-busy="true"]');
	await expect(busy)
		.toHaveCount(0, { timeout: 8_000 })
		.catch(() => {});
	return busy.count();
}

test.describe('axe', () => {
	test('public pages have no serious or critical violations', async ({ page }) => {
		await page.goto('/sign-in');
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
		expect(await audit(page, 'sign-in')).toEqual([]);
	});

	test('every main route has no serious or critical violations', async ({ page }) => {
		test.setTimeout(300_000);
		await signIn(page);
		const failures: string[] = [];
		const skipped: string[] = [];
		for (const [name, path] of await mainRoutes(page)) {
			if (!path) {
				skipped.push(name);
				continue;
			}
			await page.goto(path);
			const busy = await settled(page);
			if (busy)
				test.info().annotations.push({ type: 'busy', description: `${name}: ${busy}` });
			failures.push(...(await audit(page, `${name} (${path})`)));
		}
		if (skipped.length)
			test.info().annotations.push({ type: 'skipped', description: skipped.join(', ') });
		expect(failures, failures.join('\n')).toEqual([]);
	});

	test('the narrow layout with the navigation drawer open', async ({ page }) => {
		await page.setViewportSize({ width: 390, height: 844 });
		await signIn(page);
		await page.goto('/stacks');
		await settled(page);
		expect(await audit(page, 'stacks 390')).toEqual([]);
		await page.getByRole('button', { name: 'Open navigation' }).click();
		await expect(page.getByRole('dialog')).toBeVisible();
		expect(await audit(page, 'drawer 390')).toEqual([]);
	});
});

test.describe('keyboard only', () => {
	test('shell: skip link, navigation, command palette with focus return', async ({ page }) => {
		await signIn(page);
		await page.goto('/');
		await settled(page);
		// The first Tab reaches the skip link; Enter moves focus to the page.
		await page.keyboard.press('Tab');
		const skip = page.getByRole('link', { name: 'Skip to content' });
		await expect(skip).toBeFocused();
		await expect(skip).toBeVisible();
		await page.keyboard.press('Enter');
		await expect(page).toHaveURL(/#main$/);
		// Tab through the sidebar to "Stacks" and open it.
		await page.goto('/');
		await settled(page);
		const stacksLink = page.getByRole('navigation', { name: 'Main' }).getByRole('link', {
			name: 'Stacks'
		});
		for (
			let i = 0;
			i < 30 && !(await stacksLink.evaluate((el) => el === document.activeElement));
			i++
		)
			await page.keyboard.press('Tab');
		await expect(stacksLink).toBeFocused();
		await page.keyboard.press('Enter');
		await expect(page).toHaveURL(/\/stacks$/);
		await expect(page.getByRole('heading', { level: 1, name: 'Stacks' })).toBeVisible();
		// The palette opens with Ctrl+K, traps focus, closes with Escape and
		// gives focus back to where it was.
		const search = page.getByRole('button', { name: /Search anything/ });
		await search.focus();
		await page.keyboard.press('Control+k');
		const palette = page.getByRole('dialog');
		await expect(palette).toBeVisible();
		await expect(palette.getByRole('combobox')).toBeFocused();
		expect(await audit(page, 'command palette')).toEqual([]);
		await page.keyboard.press('Escape');
		await expect(palette).toBeHidden();
		await expect(search).toBeFocused();
		// Enter on a result navigates.
		await page.keyboard.press('Control+k');
		await page.keyboard.type('settings');
		await expect(palette.getByRole('option').first()).toBeVisible();
		await page.keyboard.press('Enter');
		await expect(page).toHaveURL(/\/settings/);
	});

	test('table: sortable headers are buttons that toggle aria-sort', async ({ page }) => {
		await signIn(page);
		await page.goto('/stacks');
		await settled(page);
		const table = page.getByRole('table').first();
		await expect(table).toBeVisible();
		const header = table
			.getByRole('columnheader')
			.filter({ has: page.getByRole('button') })
			.first();
		test.skip((await header.count()) === 0, 'no sortable column on this manager');
		const button = header.getByRole('button');
		await button.focus();
		const before = await header.getAttribute('aria-sort');
		await page.keyboard.press('Enter');
		await expect(header).not.toHaveAttribute('aria-sort', before ?? 'none');
		await expect(button).toBeFocused();
		await page.keyboard.press('Space');
		await expect(button).toBeFocused();
	});

	test('dialog: focus moves in, stays trapped and returns to the trigger', async ({ page }) => {
		await signIn(page);
		await page.goto('/access');
		await settled(page);
		const trigger = page.getByRole('button', { name: 'Invite user' }).first();
		test.skip(!(await trigger.isVisible()), 'the invite action is not available');
		await trigger.focus();
		await page.keyboard.press('Enter');
		const dialog = page.getByRole('dialog');
		await expect(dialog).toBeVisible();
		expect(await audit(page, 'invite dialog')).toEqual([]);
		// Focus is inside the dialog and stays there however far we Tab.
		const inside = () => dialog.evaluate((d) => d.contains(document.activeElement));
		expect(await inside()).toBe(true);
		for (let i = 0; i < 25; i++) {
			await page.keyboard.press('Tab');
			expect(await inside()).toBe(true);
		}
		for (let i = 0; i < 5; i++) {
			await page.keyboard.press('Shift+Tab');
			expect(await inside()).toBe(true);
		}
		await page.keyboard.press('Escape');
		await expect(dialog).toBeHidden();
		await expect(trigger).toBeFocused();
	});

	test('file manager: arrows, Enter into a folder, Backspace out, open a file', async ({
		page
	}) => {
		await signIn(page);
		const stack = (await items<Id>(page, '/stacks')).find((s) => s.name === 'silo');
		test.skip(!stack, 'needs the seeded Silo stack (devstack)');
		await page.goto(`/stacks/${stack!.id}/files`);
		const grid = page.getByRole('grid');
		await expect(grid.getByRole('row', { name: /config/ })).toBeVisible();
		await grid.focus();
		await page.keyboard.press('Home');
		const active = async () => {
			const id = await grid.getAttribute('aria-activedescendant');
			return id ? ((await page.locator(`[id="${id}"]`).getAttribute('data-path')) ?? '') : '';
		};
		await expect.poll(active).not.toBe('');
		// Walk down to the "config" folder and open it with Enter.
		for (let i = 0; i < 12 && !(await active()).endsWith('config'); i++)
			await page.keyboard.press('ArrowDown');
		expect(await active()).toMatch(/config$/);
		await page.keyboard.press('Enter');
		await expect(grid.getByRole('row', { name: 'Parent folder' })).toBeVisible();
		await expect(grid).toHaveAccessibleName('Files in config');
		await expect(grid).toBeFocused();
		// Backspace goes back up.
		await page.keyboard.press('Backspace');
		await expect(grid.getByRole('row', { name: /compose\.yaml/ })).toBeVisible();
		// Open compose.yaml in the editor with the keyboard.
		for (let i = 0; i < 20 && !(await active()).endsWith('compose.yaml'); i++)
			await page.keyboard.press('ArrowDown');
		expect(await active()).toMatch(/compose\.yaml$/);
		await page.keyboard.press('Enter');
		await expect(page.getByRole('tab', { name: /compose\.yaml/ })).toBeVisible();
		// The context menu has a keyboard alternative (Shift+F10) and Escape
		// closes it.
		await grid.focus();
		await page.keyboard.press('Shift+F10');
		const menu = page.getByRole('menu');
		await expect(menu).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(menu).toBeHidden();
	});
});

test.describe('motion and contrast', () => {
	test('reduced motion turns the live-change pulse off', async ({ browser, baseURL }) => {
		const ctx = await browser.newContext({ baseURL, reducedMotion: 'reduce' });
		const page = await ctx.newPage();
		await signIn(page);
		await page.goto('/');
		await settled(page);
		const durations = await page.evaluate(() => {
			const out: string[] = [];
			for (const el of Array.from(document.querySelectorAll('*')).slice(0, 2000)) {
				const s = getComputedStyle(el);
				if (s.animationName !== 'none' && parseFloat(s.animationDuration) > 0.01)
					out.push(
						`${el.tagName}.${el.className}: ${s.animationName} ${s.animationDuration}`
					);
			}
			return out;
		});
		expect(durations).toEqual([]);
		await ctx.close();
	});
});
