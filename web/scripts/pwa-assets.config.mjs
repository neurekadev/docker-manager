// PWA icon set (#22 app icon, #11 manifest). Two sources, two runs of the
// one-off generator CLI (not a dependency; commit its PNG output):
//
//   cd web
//   npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets.config.mjs
//   npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets-maskable.config.mjs
//   mv scripts/favicon.ico static/favicon.ico
//   mv scripts/pwa-*.png scripts/maskable-icon-512x512.png scripts/apple-touch-icon-180x180.png static/icons/
//
// This run: the "any" icons (the Docker Manager logo on a transparent
// square), the favicon and the Apple touch icon, from scripts/logo.png.
// The Apple touch icon keeps a transparent background so iOS supplies the
// tile behind it (#214); its padding puts the logo at the size it has in
// the maskable icon.
export default {
	headLinkOptions: { preset: '2023' },
	preset: {
		transparent: { sizes: [64, 192, 512], favicons: [[48, 'favicon.ico']] },
		maskable: { sizes: [] },
		apple: { sizes: [180], padding: 0.355, resizeOptions: { background: 'transparent' } }
	},
	images: ['scripts/logo.png']
};
