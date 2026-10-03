// Drag to reorder (#22): one Sortable per list, its items and their grip
// handles (DragHandle) attached by index. Pointer events, so mouse, pen and
// touch work alike (HTML5 drag and drop does not work on phones): only the
// handle starts a drag, the held row follows the pointer while the others
// make room, and dropping calls `onmove(from, to)`; Escape cancels. On the
// focused handle ArrowUp/ArrowDown (also with Alt) move one place,
// Home/End to the ends; every move is announced politely ("Moved Link 2 to
// position 1 of 3.") and focus stays on the moved row's handle.
//
//   const sort = new Sortable({ onmove: (from, to) => (rows = moveItem(rows, from, to)) });
//   {#each rows as row, i (row.key)}
//     <li {@attach sort.item(i)}>
//       <DragHandle sortable={sort} index={i} name="Link {i + 1}" />…
//
// Works for any vertical list, table rows included. Index logic: sortable.ts.
import { tick } from 'svelte';
import type { Attachment } from 'svelte/attachments';
import { announce } from './announce';
import { dropIndex, keyTarget, movedMessage, shiftOf } from './sortable';

export interface SortableOptions {
	/** Moves the item at `from` to `to`, e.g. `rows = moveItem(rows, from, to)`. */
	onmove: (from: number, to: number) => void;
}

interface Drag {
	from: number;
	to: number;
	name: string;
	pointerId: number;
	startY: number;
	/** The held item's edges before the drag. */
	top: number;
	bottom: number;
	mids: number[];
	/** How far the other items move to make room: the held item's height plus the gap. */
	step: number;
	min: number;
	max: number;
	els: HTMLElement[];
}

// Inline styles a drag sets on the items (reset when it ends).
const DRAG_STYLES = ['transform', 'transition', 'position', 'z-index', 'box-shadow', 'background'];

export class Sortable {
	/** The index of the item being dragged; null while none is. */
	dragging = $state<number | null>(null);

	#onmove: SortableOptions['onmove'];
	// Element registries, not view state.
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#items = new Map<number, HTMLElement>();
	// eslint-disable-next-line svelte/prefer-svelte-reactivity
	#handles = new Map<number, HTMLElement>();
	#drag: Drag | null = null;
	#bodyStyle = { userSelect: '', cursor: '' };

	constructor(options: SortableOptions) {
		this.#onmove = options.onmove;
	}

	/** Attach to each item (row) of the list: `{@attach sort.item(i)}`. */
	item =
		(index: number): Attachment<HTMLElement> =>
		(el) => {
			this.#items.set(index, el);
			return () => {
				if (this.#items.get(index) === el) this.#items.delete(index);
			};
		};

	/**
	 * Attach to an item's grip handle (DragHandle does): `name` is the item
	 * as the announcement names it ("Link 2").
	 */
	handle =
		(index: number, name: string): Attachment<HTMLElement> =>
		(el) => {
			this.#handles.set(index, el);
			const down = (e: PointerEvent) => this.#start(e, index, name, el);
			const key = (e: KeyboardEvent) => this.#key(e, index, name);
			el.addEventListener('pointerdown', down);
			el.addEventListener('keydown', key);
			return () => {
				el.removeEventListener('pointerdown', down);
				el.removeEventListener('keydown', key);
				if (this.#handles.get(index) === el) this.#handles.delete(index);
			};
		};

	get #length() {
		return this.#handles.size;
	}

	async #commit(from: number, to: number, name: string) {
		const length = this.#length;
		this.#onmove(from, to);
		announce(movedMessage(name, to, length));
		await tick();
		this.#handles.get(to)?.focus();
	}

	#key(e: KeyboardEvent, index: number, name: string) {
		if (this.#drag) {
			if (e.key === 'Escape') {
				e.preventDefault();
				this.#end(false);
			}
			return;
		}
		const to = keyTarget(e.key, index, this.#length);
		if (to === null) return;
		e.preventDefault();
		void this.#commit(index, to, name);
	}

	#start(e: PointerEvent, index: number, name: string, handle: HTMLElement) {
		if (e.button !== 0 || this.#drag || (handle as HTMLButtonElement).disabled) return;
		const n = this.#length;
		const els = Array.from({ length: n }, (_, i) => this.#items.get(i));
		if (n < 2 || els.some((el) => !el)) return;
		const items = els as HTMLElement[];
		e.preventDefault();
		const rects = items.map((el) => el.getBoundingClientRect());
		const gap = Math.max(0, rects[1].top - rects[0].bottom);
		this.#drag = {
			from: index,
			to: index,
			name,
			pointerId: e.pointerId,
			startY: e.clientY,
			top: rects[index].top,
			bottom: rects[index].bottom,
			mids: rects.map((r) => r.top + r.height / 2),
			step: rects[index].height + gap,
			min: rects[0].top - rects[index].top,
			max: rects[n - 1].bottom - rects[index].bottom,
			els: items
		};
		this.dragging = index;
		try {
			handle.setPointerCapture?.(e.pointerId);
		} catch {
			// Not capturable (already released): the window listeners still follow it.
		}
		items.forEach((el, i) => {
			if (i === index) {
				el.style.position = 'relative';
				el.style.zIndex = '1';
				el.style.boxShadow = 'var(--shadow-float)';
				el.style.background = 'var(--surface-raised)';
				el.dataset.dragging = '';
			} else {
				el.style.transition = 'transform var(--duration-fast) var(--ease-out)';
			}
		});
		this.#bodyStyle = {
			userSelect: document.body.style.userSelect,
			cursor: document.body.style.cursor
		};
		document.body.style.userSelect = 'none';
		document.body.style.cursor = 'grabbing';
		window.addEventListener('pointermove', this.#onPointerMove);
		window.addEventListener('pointerup', this.#onPointerUp);
		window.addEventListener('pointercancel', this.#onPointerCancel);
		window.addEventListener('keydown', this.#onEscape);
	}

	#onPointerMove = (e: PointerEvent) => {
		const d = this.#drag;
		if (!d || e.pointerId !== d.pointerId) return;
		e.preventDefault();
		const dy = Math.min(d.max, Math.max(d.min, e.clientY - d.startY));
		d.els[d.from].style.transform = `translateY(${dy}px)`;
		const to = dropIndex(d.mids, d.from, d.top + dy, d.bottom + dy);
		if (to === d.to) return;
		d.to = to;
		d.els.forEach((el, i) => {
			if (i === d.from) return;
			const s = shiftOf(i, d.from, to);
			el.style.transform = s ? `translateY(${s * d.step}px)` : '';
		});
	};

	#onPointerUp = (e: PointerEvent) => {
		if (this.#drag && e.pointerId === this.#drag.pointerId) this.#end(true);
	};

	#onPointerCancel = (e: PointerEvent) => {
		if (this.#drag && e.pointerId === this.#drag.pointerId) this.#end(false);
	};

	#onEscape = (e: KeyboardEvent) => {
		if (e.key !== 'Escape' || !this.#drag) return;
		e.preventDefault();
		this.#end(false);
	};

	#end(drop: boolean) {
		const d = this.#drag;
		if (!d) return;
		window.removeEventListener('pointermove', this.#onPointerMove);
		window.removeEventListener('pointerup', this.#onPointerUp);
		window.removeEventListener('pointercancel', this.#onPointerCancel);
		window.removeEventListener('keydown', this.#onEscape);
		// Clearing the transition with the transform: the rows snap into
		// place in the same frame the list re-renders in its new order.
		for (const el of d.els) {
			for (const p of DRAG_STYLES) el.style.removeProperty(p);
			delete el.dataset.dragging;
		}
		document.body.style.userSelect = this.#bodyStyle.userSelect;
		document.body.style.cursor = this.#bodyStyle.cursor;
		this.#drag = null;
		this.dragging = null;
		if (drop && d.to !== d.from) void this.#commit(d.from, d.to, d.name);
	}
}
