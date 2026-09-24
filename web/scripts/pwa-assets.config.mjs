// Placeholder PWA icon set (#11), generated from static/icons/icon.svg:
//
//   cd web && npx --yes @vite-pwa/assets-generator@2.0.0 --config scripts/pwa-assets.config.mjs
//
// The generator is a one-off CLI (not a dependency); commit its PNG output.
// Replaced by the design system's artwork in #22.
export default {
	headLinkOptions: { preset: '2023' },
	preset: {
		transparent: { sizes: [64, 192, 512], favicons: [[48, 'favicon.ico']] },
		maskable: { sizes: [512], padding: 0.1, resizeOptions: { background: '#404040' } },
		apple: { sizes: [180], padding: 0, resizeOptions: { background: '#404040' } }
	},
	images: ['static/icons/icon.svg']
};
