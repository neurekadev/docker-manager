// Stacking of open dialogs (#22). Bits UI mounts a dialog's portal anchor
// in <body> when the component mounts, not when it opens, so two open
// dialogs with the same z-index paint in mount order: the step-up check
// (mounted with the app shell) ended up behind the page dialog that asked
// for it. A dialog that opens takes the layer above every open one.

/** Layers above --z-dialog; the highest stays below --z-menu. */
export const MAX_DIALOG_LAYER = 9;

const openLayers = new Set<number>();

/** Claims the layer above every open dialog; release it on close. */
export function claimDialogLayer(): { layer: number; release: () => void } {
	let layer = 0;
	for (const l of openLayers) layer = Math.max(layer, l + 1);
	openLayers.add(layer);
	return {
		layer: Math.min(layer, MAX_DIALOG_LAYER),
		release: () => openLayers.delete(layer)
	};
}
