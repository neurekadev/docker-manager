// PWA icon set (#22 app icon, #11 manifest). Two sources, two runs of the
// one-off generator CLI (not a dependency; commit its PNG output):
//
//   cd web
//   npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets.config.mjs
//   npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets-maskable.config.mjs
//   mv static/icons/favicon.ico static/favicon.ico
//   mv scripts/maskable-icon-512x512.png scripts/apple-touch-icon-180x180.png static/icons/
//
// This run: the "any" icons (the rounded --surface-shell tile with the cube
// mark, transparent corners) and the favicon, from static/icons/icon.svg.
export default {
	headLinkOptions: { preset: '2023' },
	preset: {
		transparent: { sizes: [64, 192, 512], favicons: [[48, 'favicon.ico']] },
		maskable: { sizes: [] },
		apple: { sizes: [] }
	},
	images: ['static/icons/icon.svg']
};
