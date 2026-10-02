// Web app manifest (#11). Written to /manifest.webmanifest at build time by
// @vite-pwa/sveltekit (vite.config.ts) and linked from src/app.html.
//
// Colours are the design tokens (#22): the shell surface as theme colour
// (browser chrome matches the top bar) and the canvas as splash background.
// The icons are the Docker Manager logo (the whale carrying containers),
// generated from scripts/logo.png (docs/internal/web.md, "Icons").
import type { ManifestOptions } from 'vite-plugin-pwa';

/** --surface-shell and --surface-canvas (src/lib/design/tokens.css). */
export const THEME_COLOR = '#0d131b';
export const BACKGROUND_COLOR = '#0a0f15';

export const webManifest = {
	id: '/',
	name: 'Docker Manager',
	short_name: 'Docker Manager',
	description: 'Self-hosted Docker Compose and container management.',
	lang: 'en',
	dir: 'ltr',
	start_url: '/',
	scope: '/',
	display: 'standalone',
	orientation: 'any',
	theme_color: THEME_COLOR,
	background_color: BACKGROUND_COLOR,
	icons: [
		{ src: '/icons/pwa-64x64.png', sizes: '64x64', type: 'image/png' },
		{ src: '/icons/pwa-192x192.png', sizes: '192x192', type: 'image/png' },
		{ src: '/icons/pwa-512x512.png', sizes: '512x512', type: 'image/png', purpose: 'any' },
		{
			src: '/icons/maskable-icon-512x512.png',
			sizes: '512x512',
			type: 'image/png',
			purpose: 'maskable'
		}
	]
} satisfies Partial<ManifestOptions>;
