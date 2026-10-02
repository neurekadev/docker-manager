// PWA icon set (#22 app icon, #11 manifest), all from scripts/logo.png (the
// Docker Manager logo on a transparent square) with one run of the one-off
// generator CLI (not a dependency; commit its PNG output):
//
//   cd web
//   npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets.config.mjs
//   mv scripts/favicon.ico static/favicon.ico
//   mv scripts/pwa-*.png scripts/maskable-icon-512x512.png scripts/apple-touch-icon-180x180.png static/icons/
//
// The "any" icons and the favicon are the logo edge to edge. The maskable
// and Apple touch icons keep a transparent background so the launcher and
// iOS supply the tile behind the logo (#214, #220); their padding keeps the
// logo inside the maskable 80 % safe circle.
export default {
	headLinkOptions: { preset: '2023' },
	preset: {
		transparent: { sizes: [64, 192, 512], favicons: [[48, 'favicon.ico']] },
		maskable: { sizes: [512], padding: 0.368, resizeOptions: { background: 'transparent' } },
		apple: { sizes: [180], padding: 0.355, resizeOptions: { background: 'transparent' } }
	},
	images: ['scripts/logo.png']
};
