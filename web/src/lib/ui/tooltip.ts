// Placement of the app-wide tooltip (#22, TooltipLayer.svelte): above the
// anchor when it fits, else below, centred and kept inside the viewport.
// Pure; unit-tested in ui.spec.ts.

export interface Box {
	top: number;
	left: number;
	width: number;
	height: number;
}

export interface Placement {
	top: number;
	left: number;
	side: 'top' | 'bottom';
}

/**
 * Where a tooltip of `size` goes for an anchor rectangle: `gap` pixels
 * above it (below when the space above is too small) and at least `pad`
 * pixels from the viewport's edges.
 */
export function placeTooltip(
	anchor: Box,
	size: { width: number; height: number },
	viewport: { width: number; height: number },
	gap = 6,
	pad = 8
): Placement {
	const above = anchor.top - gap - size.height;
	const below = anchor.top + anchor.height + gap;
	const side = above >= pad || below + size.height > viewport.height - pad ? 'top' : 'bottom';
	const top = side === 'top' ? Math.max(pad, above) : below;
	const centred = anchor.left + anchor.width / 2 - size.width / 2;
	const left = Math.min(Math.max(pad, centred), Math.max(pad, viewport.width - pad - size.width));
	return { top, left, side };
}

/** The element whose title a pointer or focus target shows (the nearest one with a title). */
export function tooltipAnchor(target: EventTarget | null): HTMLElement | null {
	if (!(target instanceof Element)) return null;
	const el = target.closest<HTMLElement>('[title], [data-dy-title]');
	// SVG titles (charts, icons) keep their own semantics.
	if (!el || !(el instanceof HTMLElement)) return null;
	const text = el.getAttribute('title') ?? el.dataset.dyTitle ?? '';
	return text.trim() ? el : null;
}
