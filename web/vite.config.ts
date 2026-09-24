import { defineConfig } from 'vitest/config';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { SvelteKitPWA } from '@vite-pwa/sveltekit';
import { webManifest } from './src/lib/pwa/manifest.ts';
import { bundleReport } from './scripts/bundle-report-plugin.ts';

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			// src/service-worker.ts is registered by $lib/pwa/register.svelte.ts
			// so that updates wait for the user (no automatic takeover).
			serviceWorker: { register: false },
			// Single-page app embedded in the Go manager (web/embed.go). Output goes
			// to build/app; build/fallback is a committed placeholder that must survive.
			adapter: adapter({
				pages: 'build/app',
				assets: 'build/app',
				fallback: 'index.html',
				strict: true
			})
		}),
		// Build-time only (docs/adr/0002-frontend-libraries.md): writes
		// manifest.webmanifest and injects the precache list into the
		// SvelteKit-built service worker. No Workbox runtime ships.
		SvelteKitPWA({
			strategies: 'injectManifest',
			srcDir: 'src',
			filename: 'service-worker.ts',
			injectRegister: false,
			manifest: webManifest,
			manifestFilename: 'manifest.webmanifest',
			includeManifestIcons: false,
			kit: { adapterFallback: 'index.html', spa: true },
			injectManifest: {
				// Only versioned static files: content-hashed build output, the
				// manifest and its icons. The SPA shell (index.html) is added by
				// `kit.spa`. Nothing under /api or /agent, no prerendered data.
				globPatterns: [
					'client/_app/immutable/**/*.{js,css,woff2}',
					'client/manifest.webmanifest',
					'client/icons/*.{png,svg}',
					'client/favicon.ico'
				],
				maximumFileSizeToCacheInBytes: 3 * 1024 * 1024
			},
			devOptions: { enabled: false }
		}),
		bundleReport()
	],
	server: {
		// `npm run dev` proxies the API to a locally running manager.
		proxy: {
			'/api': 'http://127.0.0.1:8080'
		}
	},
	test: {
		expect: { requireAssertions: true },
		include: ['src/**/*.{test,spec}.{js,ts}'],
		environment: 'node'
	}
});
