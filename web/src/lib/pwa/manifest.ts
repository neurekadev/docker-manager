// Web app manifest (#11). Written to /manifest.webmanifest at build time by
// @vite-pwa/sveltekit (vite.config.ts) and linked from src/app.html.
//
// Colours are the design tokens (#22): the shell surface as theme colour
// (browser chrome matches the top bar) and the canvas as splash background.
// The icons are the #22 app icon (the shell's cube mark on the shell tile),
// generated from static/icons/icon.svg and scripts/icon-maskable.svg
// (docs/web.md, "Icons").
import type { ManifestOptions } from 'vite-plugin-pwa';

/** --surface-shell and --surface-canvas (src/lib/design/tokens.css). */
export const THEME_COLOR = '#0e141d';
export const BACKGROUND_COLOR = '#0b1016';

export const webManifest = {
	id: '/',
	name: 'DockYard',
	short_name: 'DockYard',
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
