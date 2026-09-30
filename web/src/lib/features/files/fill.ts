// Full-height work surfaces (file manager, logs, terminal): the element
// takes the viewport height below its own top (whatever header or tabs
// sit above it, standalone or inside the stack layout), never less than
// `min` px, without a page scroll. It follows window resizes and content
// above it that appears, disappears or changes size later (a job tray, a
// notice): the earlier siblings of the element and of each ancestor up to
// <main> are observed.
import type { Action } from 'svelte/action';

export interface FillOptions {
	/** Space kept free below the element; default: <main>'s bottom padding. */
	bottom?: number;
	min?: number;
}

/** The height that fills the viewport below `top` (px, document coordinates). */
export function fillHeight(viewport: number, top: number, bottom: number, min: number): number {
	return Math.floor(Math.max(min, viewport - top - bottom));
}

export const fillViewport: Action<HTMLElement, FillOptions | undefined> = (node, opts) => {
	let o = opts ?? {};
	const bottom = () => {
		if (o.bottom !== undefined) return o.bottom;
		const main = node.closest('main');
		return main ? parseFloat(getComputedStyle(main).paddingBottom) || 0 : 24;
	};
	const update = () => {
		const top = node.getBoundingClientRect().top + window.scrollY;
		const h = `${fillHeight(window.innerHeight, top, bottom(), o.min ?? 480)}px`;
		if (node.style.height !== h) node.style.height = h;
	};
	let frame = 0;
	const schedule = () => {
		if (!frame)
			frame = requestAnimationFrame(() => {
				frame = 0;
				update();
			});
	};

	// What sits above the element: every earlier sibling of it and of its
	// ancestors (size changes), and those ancestors' children (added or
	// removed siblings, which re-arm the watch).
	const resized = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(schedule);
	const changed =
		typeof MutationObserver === 'undefined'
			? null
			: new MutationObserver(() => {
					watch();
					schedule();
				});
	function watch() {
		resized?.disconnect();
		changed?.disconnect();
		const stop = node.closest('main');
		for (let el: Element = node; el.parentElement && el !== stop; el = el.parentElement) {
			changed?.observe(el.parentElement, { childList: true });
			for (let s = el.previousElementSibling; s; s = s.previousElementSibling)
				resized?.observe(s);
		}
	}

	update();
	watch();
	schedule();
	window.addEventListener('resize', schedule);
	return {
		update(next) {
			o = next ?? {};
			update();
		},
		destroy() {
			cancelAnimationFrame(frame);
			resized?.disconnect();
			changed?.disconnect();
			window.removeEventListener('resize', schedule);
		}
	};
};
