// Web app manifest (#11). Written to /manifest.webmanifest at build time by
// @vite-pwa/sveltekit (vite.config.ts) and linked from src/app.html.
//
// PROVISIONAL until the design system (#22): the colours are neutral
// placeholders and the icons are simple placeholder artwork generated from
// static/icons/icon.svg (see docs/web.md). Do not treat either as branding.
import type { ManifestOptions } from 'vite-plugin-pwa';

/** Neutral placeholder colours; #22 replaces them with design tokens. */
export const PLACEHOLDER_THEME_COLOR = '#404040';
export const PLACEHOLDER_BACKGROUND_COLOR = '#ffffff';

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
	theme_color: PLACEHOLDER_THEME_COLOR,
	background_color: PLACEHOLDER_BACKGROUND_COLOR,
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
