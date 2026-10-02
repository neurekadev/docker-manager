// PWA icon set, second run (see pwa-assets.config.mjs): the maskable icon
// from the full-bleed scripts/icon-maskable.png (the logo inside the
// maskable safe circle on the --surface-shell tile).
export default {
	headLinkOptions: { preset: '2023' },
	preset: {
		transparent: { sizes: [] },
		maskable: { sizes: [512], padding: 0, resizeOptions: { background: '#0d131b' } },
		apple: { sizes: [] }
	},
	images: ['scripts/icon-maskable.png']
};
