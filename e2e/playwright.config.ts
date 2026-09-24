// Playwright configuration (#29). Targets the TLS proxy fixture from
// e2e/compose.yaml (manager built from source behind Caddy).
//
// E2E_BASE_URL            origin to test (default https://localhost:8443)
// E2E_IGNORE_HTTPS_ERRORS set to 1 for local runs without trusting Caddy's
//                         root CA (WebAuthn then fails: browsers disable it
//                         on certificate errors). CI trusts the root instead.
import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.E2E_BASE_URL ?? 'https://localhost:8443';

export default defineConfig({
	testDir: './tests',
	fullyParallel: true,
	forbidOnly: !!process.env.CI,
	retries: 0,
	timeout: 30_000,
	reporter: process.env.CI
		? [['list'], ['html', { open: 'never' }], ['junit', { outputFile: 'test-results/junit.xml' }]]
		: [['list'], ['html', { open: 'never' }]],
	use: {
		baseURL,
		ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === '1',
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure'
	},
	projects: [
		{
			// The WebAuthn helper drives Chromium's DevTools protocol, so the
			// suite is Chromium-only for now; browser support is #12.
			name: 'chromium',
			use: { ...devices['Desktop Chrome'] }
		}
	]
});
