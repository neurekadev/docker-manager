// The terminal screen end to end (#8, #22 track B3): connect to a
// container with the default /bin/sh, type and read output through xterm,
// resize (stty size follows the window), disconnect; a command the image
// does not have ends with close 4422 and the brief's message.
//
// Needs a real Docker Engine (exec), so it runs in CI's extended suite, not
// against the Docker-free devstack (it has no exec). Set:
//
//   E2E_TERMINAL_USER, E2E_TERMINAL_PASSWORD  a user holding container.exec
//   E2E_TERMINAL_ENV        environment ID
//   E2E_TERMINAL_CONTAINER  a running container with /bin/sh (e.g. busybox)
//
// Without them the spec is skipped (like tests/terminal.spec.ts, which
// checks the same stream at the protocol level).
import { expect, test, type Page } from '@playwright/test';

const user = process.env.E2E_TERMINAL_USER ?? '';
const password = process.env.E2E_TERMINAL_PASSWORD ?? '';
const envId = process.env.E2E_TERMINAL_ENV ?? '';
const container = process.env.E2E_TERMINAL_CONTAINER ?? '';
const configured = user !== '' && password !== '' && envId !== '' && container !== '';

async function signIn(page: Page) {
	await page.goto('/sign-in');
	await page.getByLabel('Username').fill(user);
	await page.getByLabel('Password', { exact: true }).fill(password);
	await page.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(page).not.toHaveURL(/\/sign-in/);
}

test.describe('terminal screen', () => {
	test.skip(
		!configured,
		'set E2E_TERMINAL_USER, E2E_TERMINAL_PASSWORD, E2E_TERMINAL_ENV and E2E_TERMINAL_CONTAINER'
	);

	test('connect, type, resize and disconnect', async ({ page }) => {
		await signIn(page);
		await page.goto(
			`/containers/${encodeURIComponent(envId)}/${encodeURIComponent(container)}/terminal`
		);
		await expect(page.getByLabel('Command')).toHaveValue('/bin/sh');
		await page.getByRole('button', { name: 'Connect' }).click();
		await expect(page.getByText(/^Connected to /)).toBeVisible({ timeout: 15_000 });
		const screen = page.getByRole('region', { name: /output$/ });
		await screen.click();
		await page.keyboard.type('echo dockyard-ui-$((6*7))\n');
		await expect(screen).toContainText('dockyard-ui-42', { timeout: 10_000 });

		// The grid follows the window (resize messages): the TTY has a size.
		await page.setViewportSize({ width: 900, height: 600 });
		await page.keyboard.type('stty size\n');
		await expect(screen).toContainText(/\n?\d+ \d+/, { timeout: 10_000 });

		await page.getByRole('button', { name: 'Disconnect' }).click();
		await expect(page.getByRole('button', { name: 'Connect again' })).toBeVisible();
		await expect(page.getByText('Ended')).toBeVisible();
	});

	test('a command the image does not have ends with 4422 and says so', async ({ page }) => {
		await signIn(page);
		await page.goto(
			`/containers/${encodeURIComponent(envId)}/${encodeURIComponent(container)}/terminal`
		);
		await page.getByLabel('Command').fill('/definitely-not-a-shell');
		await page.getByRole('button', { name: 'Connect' }).click();
		await expect(
			page.getByText('This image has no /definitely-not-a-shell — try another command.')
		).toBeVisible({
			timeout: 15_000
		});
		await expect(page.getByRole('button', { name: 'Connect again' })).toBeEnabled();
	});
});
