// The file manager end to end (#15, #22 track B3): browsing with mouse,
// keyboard and touch; multi-select and the ".." drop target; create, edit
// and save, upload with conflict prompts, download, archive and extract,
// permissions, delete; a host-side edit appearing in the open listing and an
// unsaved edit surviving an external change (save refused until resolved);
// the same component in a volume.
//
// Needs a manager with a stack that has files and a local volume. Locally
// the Docker-free devstack provides them (docs/development.md, "UI
// devstack"); its stack project directories are real folders on disk:
//
//   npm --prefix web run build
//   go run ./test/devstack -addr 127.0.0.1:8080
//   cd e2e && E2E_BASE_URL=http://localhost:8080 E2E_UI_OWNER=admin \
//     E2E_UI_PASSWORD=dockyard-devstack-owner \
//     E2E_FILES_STACK_DIR=<devstack data>/hosts/homelab/volumes/dockyard_stacks/_data/silo \
//     npx playwright test tests/ui-files.spec.ts
//
// E2E_FILES_STACK (default "silo") names the stack, E2E_FILES_VOLUME (default
// "silo_db-data") a volume on its environment. Without the stack the spec is
// skipped (the proxy-only CI stack has no stacks); without
// E2E_FILES_STACK_DIR the host-side edit test is skipped.
import { expect, test, type Browser, type Page } from '@playwright/test';
import { appendFileSync, existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const owner = process.env.E2E_UI_OWNER ?? 'owner';
const password = process.env.E2E_UI_PASSWORD ?? 'dockyard-e2e-owner-passphrase';
const stackName = process.env.E2E_FILES_STACK ?? 'silo';
const volumeName = process.env.E2E_FILES_VOLUME ?? 'silo_db-data';
const stackDir = process.env.E2E_FILES_STACK_DIR ?? '';
const shots = process.env.E2E_SCREENSHOTS_DIR ?? '';
const run = `e2e${Date.now() % 1_000_000}`;

interface Target {
	stackId: string;
	environmentId: string;
}

async function signIn(page: Page) {
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(owner);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
}

async function findStack(page: Page): Promise<Target | null> {
	const res = await page.request.get('/api/v1/stacks?limit=200');
	if (!res.ok()) return null;
	const items = (await res.json()).items as { id: string; name: string; environmentId: string }[];
	const s = items.find((x) => x.name === stackName);
	return s ? { stackId: s.id, environmentId: s.environmentId } : null;
}

async function openFiles(page: Page, t: Target, path = '') {
	await page.goto(`/stacks/${t.stackId}/files${path ? `?path=${encodeURIComponent(path)}` : ''}`);
	await expect(page.getByRole('grid', { name: /^Files in / })).toBeVisible();
}

const grid = (page: Page) => page.getByRole('grid', { name: /^Files in / });
const row = (page: Page, name: string) =>
	grid(page).getByRole('row').filter({ has: page.getByText(name, { exact: true }) });

async function newSignedIn(browser: Browser, baseURL: string | undefined, viewport: { width: number; height: number }, touch = false) {
	const ctx = await browser.newContext({
		baseURL,
		viewport,
		hasTouch: touch,
		isMobile: touch,
		ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === '1'
	});
	const page = await ctx.newPage();
	await signIn(page);
	return page;
}

test.describe.serial('file manager', () => {
	let target: Target | null = null;

	test.beforeEach(async ({ page }) => {
		await signIn(page);
		target ??= await findStack(page);
		test.skip(!target, `no stack "${stackName}" on this manager (the devstack has one)`);
	});

	test.afterAll(async ({ browser, baseURL }) => {
		if (!target) return;
		// Remove what this run created (best effort).
		const page = await newSignedIn(browser, baseURL, { width: 1440, height: 900 });
		const list = await page.request.get(`/api/v1/stacks/${target.stackId}/files?path=.&hidden=true&limit=200`);
		const names = ((await list.json()).items as { name: string }[]).map((i) => i.name).filter((n) => n.includes(run));
		if (names.length)
			await page.request.post(`/api/v1/stacks/${target.stackId}/files/deletions`, {
				data: { paths: names },
				headers: { 'Idempotency-Key': `${run}-cleanup` }
			});
		await page.context().close();
	});

	test('browse with the mouse: folders, the ".." row, breadcrumbs, filter, hidden files', async ({ page }) => {
		await openFiles(page, target!);
		await expect(row(page, 'compose.yaml')).toBeVisible();
		await expect(grid(page).getByRole('row', { name: /Parent folder/ })).toHaveCount(0);
		await row(page, 'config').dblclick();
		await expect(page).toHaveURL(/\?path=config$/);
		const up = grid(page).getByRole('row', { name: /Parent folder/ });
		await expect(up).toBeVisible();
		await expect(row(page, 'silo.yaml')).toBeVisible();
		await up.dblclick();
		await expect(page).not.toHaveURL(/path=/);
		await page.getByRole('searchbox', { name: 'Filter by name' }).fill('compose');
		await expect(row(page, 'compose.yaml')).toBeVisible();
		await expect(row(page, 'nginx.conf')).toHaveCount(0);
		await page.getByRole('searchbox', { name: 'Filter by name' }).fill('');
		await expect(row(page, '.env')).toHaveCount(0);
		await page.getByRole('button', { name: 'Show hidden files' }).click();
		await expect(row(page, '.env')).toBeVisible();
		await page.goto(`/stacks/${target!.stackId}/files?path=config`);
		await page.getByRole('navigation', { name: 'Folder path' }).getByRole('link', { name: stackName }).click();
		await expect(row(page, 'compose.yaml')).toBeVisible();
		if (shots) await page.screenshot({ path: `${shots}/files-1440.png` });
	});

	test('keyboard: arrows, Enter, Backspace, Ctrl/Cmd+A, Escape, context menu', async ({ page }) => {
		await openFiles(page, target!);
		await grid(page).focus();
		await page.keyboard.press('ArrowDown');
		await expect(row(page, 'config')).toHaveAttribute('aria-selected', 'true');
		await page.keyboard.press('Enter');
		await expect(page).toHaveURL(/\?path=config$/);
		await expect(grid(page)).toBeFocused();
		await page.keyboard.press('Backspace');
		await expect(page).not.toHaveURL(/path=/);
		// Going up puts the cursor on the folder we came from (not selected).
		const configId = await row(page, 'config').getAttribute('id');
		await expect(grid(page)).toHaveAttribute('aria-activedescendant', configId!);
		await expect(row(page, 'config')).toHaveAttribute('aria-selected', 'false');
		await page.keyboard.press('ControlOrMeta+a');
		await expect(page.getByText(/^\d+ selected$/)).toBeVisible();
		await page.keyboard.press('Escape');
		await expect(page.getByText(/^\d+ selected$/)).toHaveCount(0);
		// The context menu (Shift+F10) acts on the selection.
		await page.keyboard.press('ArrowDown');
		await page.keyboard.press('Shift+F10');
		await expect(page.getByRole('menuitem', { name: 'Open folder' })).toBeVisible();
		await page.keyboard.press('Escape');
	});

	test('create, edit and save a file; a Compose source says it is not deployed', async ({ page }) => {
		await openFiles(page, target!);
		await page.getByRole('button', { name: 'New file' }).click();
		await page.getByLabel('File name').fill(`${run}-notes.yaml`);
		await page.getByRole('button', { name: 'Create file' }).click();
		await expect(page.getByRole('tab', { name: new RegExp(`${run}-notes\\.yaml`) })).toBeVisible();
		const editor = page.locator('.cm-content');
		await editor.click();
		await page.keyboard.type('key: value');
		await expect(page.getByText('Unsaved changes')).toBeVisible();
		await page.keyboard.press('ControlOrMeta+s');
		await expect(page.getByText(`Saved ${run}-notes.yaml`)).toBeVisible();
		const saved = await page.request.get(`/api/v1/stacks/${target!.stackId}/files/content?path=${run}-notes.yaml`);
		expect((await saved.json()).content).toBe('key: value');
		// Opening compose.yaml shows the revision notice.
		await row(page, 'compose.yaml').click();
		await expect(page.getByRole('region', { name: 'Editor' })).toContainText(
			'Compose source: saving records a new revision'
		);
		await expect(page.getByRole('region', { name: 'Editor' })).toContainText('it is not deployed');
	});

	test('copy and paste into the same folder asks per item; keep both', async ({ page }) => {
		await openFiles(page, target!);
		await row(page, `${run}-notes.yaml`).click({ modifiers: ['ControlOrMeta'] });
		await grid(page).focus();
		await page.keyboard.press('ControlOrMeta+c');
		await page.keyboard.press('ControlOrMeta+v');
		const dialog = page.getByRole('alertdialog');
		await expect(dialog).toContainText('already exists');
		await expect(dialog.getByRole('checkbox')).toHaveCount(0); // one conflict: no apply-to-all
		await dialog.getByRole('button', { name: /^Keep both/ }).click();
		await expect(row(page, `${run}-notes (1).yaml`)).toBeVisible({ timeout: 15_000 });
	});

	test('drag an entry onto a folder and onto ".." moves it', async ({ page }) => {
		await openFiles(page, target!);
		await row(page, `${run}-notes (1).yaml`).dragTo(row(page, 'config'));
		await expect(row(page, `${run}-notes (1).yaml`)).toHaveCount(0, { timeout: 15_000 });
		await openFiles(page, target!, 'config');
		await row(page, `${run}-notes (1).yaml`).dragTo(grid(page).getByRole('row', { name: /Parent folder/ }));
		await expect(row(page, `${run}-notes (1).yaml`)).toHaveCount(0, { timeout: 15_000 });
		await openFiles(page, target!);
		await expect(row(page, `${run}-notes (1).yaml`)).toBeVisible();
	});

	test('upload files with a conflict prompt, then download one', async ({ page }) => {
		const dir = mkdtempSync(join(tmpdir(), 'dy-upload-'));
		const a = join(dir, `${run}-upload.txt`);
		writeFileSync(a, 'first upload\n');
		try {
			await openFiles(page, target!);
			await page.getByRole('button', { name: 'Upload', exact: true }).click();
			const chooser = page.waitForEvent('filechooser');
			await page.getByRole('menuitem', { name: 'Upload files' }).click();
			await (await chooser).setFiles(a);
			await expect(page.getByText(/^Uploaded 1 file$/)).toBeVisible({ timeout: 15_000 });
			await expect(row(page, `${run}-upload.txt`)).toBeVisible();
			// The same name again: asked, not overwritten silently.
			writeFileSync(a, 'second upload\n');
			await page.getByRole('button', { name: 'Upload', exact: true }).click();
			const again = page.waitForEvent('filechooser');
			await page.getByRole('menuitem', { name: 'Upload files' }).click();
			await (await again).setFiles(a);
			const dialog = page.getByRole('alertdialog');
			await expect(dialog).toContainText(`${run}-upload.txt already exists`);
			await dialog.getByRole('button', { name: 'Replace' }).click();
			await expect
				.poll(
					async () =>
						(await (await page.request.get(`/api/v1/stacks/${target!.stackId}/files/content?path=${run}-upload.txt`)).json())
							.content,
					{ timeout: 15_000 }
				)
				.toBe('second upload\n');

			await row(page, `${run}-upload.txt`).click({ button: 'right' });
			const download = page.waitForEvent('download');
			await page.getByRole('menuitem', { name: 'Download' }).click();
			expect((await download).suggestedFilename()).toBe(`${run}-upload.txt`);
		} finally {
			rmSync(dir, { recursive: true, force: true });
		}
	});

	test('archive and extract', async ({ page }) => {
		await openFiles(page, target!);
		await row(page, 'config').click({ button: 'right' });
		await page.getByRole('menuitem', { name: 'Create archive…' }).click();
		await page.getByLabel('Archive name').fill(`${run}-config.zip`);
		await page.getByRole('button', { name: 'Create archive' }).click();
		await expect(row(page, `${run}-config.zip`)).toBeVisible({ timeout: 20_000 });
		await row(page, `${run}-config.zip`).click({ button: 'right' });
		await page.getByRole('menuitem', { name: 'Extract…' }).click();
		await page.getByLabel('Folder name').fill(`${run}-extracted`);
		await page.getByRole('button', { name: 'Extract' }).click();
		await expect(row(page, `${run}-extracted`)).toBeVisible({ timeout: 20_000 });
		await row(page, `${run}-extracted`).dblclick();
		await expect(row(page, 'config')).toBeVisible();
	});

	test('permissions with recursion preview, then delete with type-to-confirm', async ({ page }) => {
		await openFiles(page, target!);
		await row(page, `${run}-extracted`).click({ button: 'right' });
		await page.getByRole('menuitem', { name: 'Permissions…' }).click();
		const dialog = page.getByRole('dialog', { name: 'Change permissions' });
		await dialog.getByRole('switch', { name: 'Apply to everything inside the selected folders' }).click();
		await expect(dialog.getByText(/^Changes \d+ folders?/)).toBeVisible();
		await dialog.getByLabel(/^Mode/).first().fill('0750');
		await dialog.getByRole('button', { name: 'Change permissions' }).click();
		await expect(page.getByText(new RegExp(`Changed permissions of ${run}-extracted`))).toBeVisible({ timeout: 20_000 });

		await row(page, `${run}-extracted`).click();
		await grid(page).focus();
		await page.keyboard.press('Delete');
		const confirm = page.getByRole('alertdialog', { name: new RegExp(`Delete ${run}-extracted`) });
		await expect(confirm).toContainText('This cannot be undone');
		await expect(confirm.getByRole('button', { name: 'Delete' })).toBeDisabled();
		await confirm.getByLabel(`Type ${run}-extracted to confirm`).fill(`${run}-extracted`);
		await confirm.getByRole('button', { name: 'Delete' }).click();
		await expect(row(page, `${run}-extracted`)).toHaveCount(0, { timeout: 20_000 });
	});

	test('a host-side edit appears in the open listing; an unsaved edit survives and blocks saving', async ({ page }) => {
		test.skip(!stackDir || !existsSync(stackDir), 'set E2E_FILES_STACK_DIR to the stack directory as this runner sees it');
		await openFiles(page, target!);
		const name = `${run}-host.txt`;
		writeFileSync(join(stackDir, name), 'from the host\n');
		await expect(row(page, name)).toBeVisible({ timeout: 10_000 });

		await row(page, name).click();
		await page.locator('.cm-content').click();
		await page.keyboard.press('ControlOrMeta+End');
		await page.keyboard.type('my unsaved line');
		appendFileSync(join(stackDir, name), 'the host again\n');
		const banner = page.getByRole('alert').filter({ hasText: `${name} changed on disk. Your edits are kept.` });
		await expect(banner).toBeVisible({ timeout: 10_000 });
		await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeDisabled();
		await expect(page.locator('.cm-content')).toContainText('my unsaved line');
		await banner.getByRole('button', { name: 'Compare' }).click();
		await expect(page.getByRole('dialog', { name: `Compare ${name}` })).toContainText('the host again');
		await page.keyboard.press('Escape');
		await banner.getByRole('button', { name: 'Overwrite' }).click();
		await page.getByRole('alertdialog').getByRole('button', { name: 'Overwrite' }).click();
		await expect(banner).toHaveCount(0);
		const got = await page.request.get(`/api/v1/stacks/${target!.stackId}/files/content?path=${name}`);
		expect((await got.json()).content).toContain('my unsaved line');
		if (shots) await page.screenshot({ path: `${shots}/files-editor-1440.png` });
	});

	test('touch-size screen: taps, checkboxes and the row menu', async ({ browser, baseURL }) => {
		const page = await newSignedIn(browser, baseURL, { width: 390, height: 844 }, true);
		await openFiles(page, target!);
		await row(page, 'config').tap();
		await expect(page).toHaveURL(/\?path=config$/);
		await grid(page).getByRole('row', { name: /Parent folder/ }).tap();
		await expect(page).not.toHaveURL(/path=/);
		await page.getByRole('checkbox', { name: 'Select nginx.conf' }).tap();
		await page.getByRole('checkbox', { name: 'Select README.md' }).tap();
		await expect(page.getByText('2 selected')).toBeVisible();
		await page.getByRole('button', { name: 'Actions for nginx.conf' }).tap();
		await expect(page.getByRole('menuitem', { name: 'Download as ZIP' })).toBeVisible();
		await page.keyboard.press('Escape');
		// Opening a file switches to the editor pane; the list stays one tap away.
		await row(page, 'compose.yaml').tap();
		await expect(page.getByRole('button', { name: /^Editor \(1\)$/ })).toHaveAttribute('aria-pressed', 'true');
		await page.getByRole('button', { name: 'Files', exact: true }).tap();
		await expect(grid(page)).toBeVisible();
		if (shots) await page.screenshot({ path: `${shots}/files-390.png` });
		await page.context().close();
	});

	test('the same file manager in a volume: create, upload, edit, archive, extract, permissions, delete', async ({ page }) => {
		const vol = await page.request.get(`/api/v1/environments/${target!.environmentId}/volumes/${volumeName}`);
		test.skip(!vol.ok(), `no volume ${volumeName}`);
		const base = `/api/v1/environments/${target!.environmentId}/volumes/${volumeName}/files`;
		await page.goto(`/volumes/${target!.environmentId}/${volumeName}/files`);
		await expect(grid(page)).toBeVisible();
		await page.getByRole('button', { name: 'New folder' }).click();
		await page.getByLabel('Folder name').fill(`${run}-dir`);
		await page.getByRole('button', { name: 'Create folder' }).click();
		await expect(row(page, `${run}-dir`)).toBeVisible();
		await row(page, `${run}-dir`).dblclick();
		await expect(grid(page).getByRole('row', { name: /Parent folder/ })).toBeVisible();

		// Upload into the new folder, then edit and save the file.
		const tmp = mkdtempSync(join(tmpdir(), 'dy-vol-'));
		try {
			const f = join(tmp, 'settings.conf');
			writeFileSync(f, 'max_connections = 100\n');
			await page.getByRole('button', { name: 'Upload', exact: true }).click();
			const chooser = page.waitForEvent('filechooser');
			await page.getByRole('menuitem', { name: 'Upload files' }).click();
			await (await chooser).setFiles(f);
			await expect(row(page, 'settings.conf')).toBeVisible({ timeout: 15_000 });
		} finally {
			rmSync(tmp, { recursive: true, force: true });
		}
		await row(page, 'settings.conf').click();
		await page.locator('.cm-content').click();
		await page.keyboard.press('ControlOrMeta+End');
		await page.keyboard.type('shared_buffers = 256MB');
		await page.keyboard.press('ControlOrMeta+s');
		await expect(page.getByText('Saved settings.conf')).toBeVisible();
		const saved = await page.request.get(`${base}/content?path=${run}-dir/settings.conf`);
		expect((await saved.json()).content).toBe('max_connections = 100\nshared_buffers = 256MB');

		// Archive the folder, extract it next to it, change its mode.
		await grid(page).getByRole('row', { name: /Parent folder/ }).dblclick();
		await row(page, `${run}-dir`).click({ button: 'right' });
		await page.getByRole('menuitem', { name: 'Create archive…' }).click();
		await page.getByRole('radio', { name: /tar\.gz/ }).check();
		await expect(page.getByLabel('Archive name')).toHaveValue(`${run}-dir.tar.gz`);
		await page.getByRole('button', { name: 'Create archive' }).click();
		await expect(row(page, `${run}-dir.tar.gz`)).toBeVisible({ timeout: 20_000 });
		await row(page, `${run}-dir.tar.gz`).click({ button: 'right' });
		await page.getByRole('menuitem', { name: 'Extract…' }).click();
		await page.getByLabel('Folder name').fill(`${run}-copy`);
		await page.getByRole('button', { name: 'Extract' }).click();
		await expect(row(page, `${run}-copy`)).toBeVisible({ timeout: 20_000 });
		await row(page, `${run}-copy`).click({ button: 'right' });
		await page.getByRole('menuitem', { name: 'Permissions…' }).click();
		const perms = page.getByRole('dialog', { name: 'Change permissions' });
		await perms.getByRole('switch', { name: 'Apply to everything inside the selected folders' }).click();
		await perms.getByRole('checkbox', { name: 'Others read' }).uncheck();
		await perms.getByRole('button', { name: 'Change permissions' }).click();
		await expect(page.getByText(new RegExp(`Changed permissions of ${run}-copy`))).toBeVisible({ timeout: 20_000 });

		// Delete what this test made (recursive: type-to-confirm).
		for (const name of [`${run}-copy`, `${run}-dir`]) {
			await row(page, name).click();
			await grid(page).focus();
			await page.keyboard.press('Delete');
			const confirm = page.getByRole('alertdialog', { name: new RegExp(`Delete ${name}`) });
			await confirm.getByLabel(`Type ${name} to confirm`).fill(name);
			await confirm.getByRole('button', { name: 'Delete' }).click();
			await expect(row(page, name)).toHaveCount(0, { timeout: 20_000 });
		}
		await row(page, `${run}-dir.tar.gz`).click();
		await grid(page).focus();
		await page.keyboard.press('Delete');
		await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
		await expect(row(page, `${run}-dir.tar.gz`)).toHaveCount(0, { timeout: 20_000 });
	});

	test('change the owner (POSIX hosts)', async ({ page }) => {
		// Windows cannot change numeric owners: the devstack's agent there
		// reports it per item. Linux agents (production, CI) apply it.
		test.skip(process.platform === 'win32', 'chown needs a POSIX host for the agent');
		const vol = await page.request.get(`/api/v1/environments/${target!.environmentId}/volumes/${volumeName}`);
		test.skip(!vol.ok(), `no volume ${volumeName}`);
		await page.goto(`/volumes/${target!.environmentId}/${volumeName}/files`);
		await page.getByRole('button', { name: 'New folder' }).click();
		await page.getByLabel('Folder name').fill(`${run}-owned`);
		await page.getByRole('button', { name: 'Create folder' }).click();
		await row(page, `${run}-owned`).click({ button: 'right' });
		await page.getByRole('menuitem', { name: 'Permissions…' }).click();
		const perms = page.getByRole('dialog', { name: 'Change permissions' });
		await perms.getByRole('switch', { name: 'Change mode' }).click();
		await perms.getByRole('switch', { name: 'Change owner' }).click();
		// The current owner (prefilled): allowed for the agent's own user too.
		await expect(perms.getByLabel('Owner ID (UID)')).not.toHaveValue('');
		await perms.getByRole('button', { name: 'Change permissions' }).click();
		await expect(page.getByText(new RegExp(`Changed permissions of ${run}-owned`))).toBeVisible({ timeout: 20_000 });
		await row(page, `${run}-owned`).click();
		await grid(page).focus();
		await page.keyboard.press('Delete');
		await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
		await expect(row(page, `${run}-owned`)).toHaveCount(0, { timeout: 20_000 });
	});
});
