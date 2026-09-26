// PWA icon set, second run (see pwa-assets.config.mjs): the maskable icon
// and the Apple touch icon from the full-bleed scripts/icon-maskable.png
// (the logo inside the maskable safe circle on the --surface-shell tile;
// iOS rounds the corners itself).
export default {
	headLinkOptions: { preset: '2023' },
	preset: {
		transparent: { sizes: [] },
		maskable: { sizes: [512], padding: 0, resizeOptions: { background: '#0e141d' } },
		apple: { sizes: [180], padding: 0, resizeOptions: { background: '#0e141d' } }
	},
	images: ['scripts/icon-maskable.png']
};
