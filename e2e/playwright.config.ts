// Playwright configuration (#27, #29). Targets the E2E stack from
// e2e/compose.yaml: the manager (built from source with the e2e test
// routes) behind each example reverse proxy from deploy/, one project per
// proxy origin. Every spec runs through every proxy.
//
// E2E_BASE_URL            test a single origin instead (project "custom")
// E2E_IGNORE_HTTPS_ERRORS set to 1 for local runs without trusting the E2E
//                         CA (WebAuthn then fails: browsers disable it on
//                         certificate errors). CI trusts the CA instead.
import { defineConfig, devices } from '@playwright/test';

/** Proxy origins of e2e/compose.yaml. */
const proxyOrigins: Record<string, string> = {
	caddy: 'https://localhost:8443',
	traefik: 'https://localhost:8444',
	nginx: 'https://localhost:8445'
};

const origins: Record<string, string> = process.env.E2E_BASE_URL
	? { custom: process.env.E2E_BASE_URL }
	: proxyOrigins;

export default defineConfig({
	testDir: './tests',
	fullyParallel: true,
	forbidOnly: !!process.env.CI,
	retries: 0,
	// The proxy idle tests hold streams open for over a minute; run them
	// side by side.
	workers: process.env.CI ? 6 : undefined,
	timeout: 30_000,
	reporter: process.env.CI
		? [
				['list'],
				['html', { open: 'never' }],
				['junit', { outputFile: 'test-results/junit.xml' }]
			]
		: [['list'], ['html', { open: 'never' }]],
	use: {
		ignoreHTTPSErrors: process.env.E2E_IGNORE_HTTPS_ERRORS === '1',
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure'
	},
	// The WebAuthn helper drives Chromium's DevTools protocol, so the suite
	// is Chromium-only for now; browser support is #12.
	projects: Object.entries(origins).map(([name, baseURL]) => ({
		name,
		use: { ...devices['Desktop Chrome'], baseURL }
	}))
});
