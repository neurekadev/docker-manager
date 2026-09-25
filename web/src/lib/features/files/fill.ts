// Full-height work surfaces (file manager, logs, terminal): the element
// takes the viewport height below its own top (whatever header or tabs
// sit above it, standalone or inside the stack layout), never less than
// `min` px, and follows window resizes.
import type { Action } from 'svelte/action';

export interface FillOptions {
	/** Space kept free below the element (the page's bottom padding). */
	bottom?: number;
	min?: number;
}

export const fillViewport: Action<HTMLElement, FillOptions | undefined> = (node, opts) => {
	let o = opts ?? {};
	const update = () => {
		const top = node.getBoundingClientRect().top + window.scrollY;
		const h = Math.max(o.min ?? 480, window.innerHeight - top - (o.bottom ?? 24));
		node.style.height = `${Math.floor(h)}px`;
	};
	update();
	const raf = requestAnimationFrame(update);
	window.addEventListener('resize', update);
	return {
		update(next) {
			o = next ?? {};
			update();
		},
		destroy() {
			cancelAnimationFrame(raf);
			window.removeEventListener('resize', update);
		}
	};
};
