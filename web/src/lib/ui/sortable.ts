// Pure index logic of drag to reorder (#22; Sortable, DragHandle): moving
// an item, the drop position under the pointer, how the other rows shift
// while one is dragged, the keyboard moves and the announcement. Tested in
// sortable.spec.ts.

/** A copy of `items` with the item at `from` moved to `to`. */
export function moveItem<T>(items: readonly T[], from: number, to: number): T[] {
	const out = [...items];
	if (from < 0 || from >= out.length || to < 0 || to >= out.length || from === to) return out;
	const [item] = out.splice(from, 1);
	out.splice(to, 0, item);
	return out;
}

/**
 * Where the dragged item lands: past every item above whose middle (`mids`,
 * in list order, measured before the drag) its top edge rose above, or
 * every item below whose middle its bottom edge sank below. `top` and
 * `bottom` are the held item's edges where it is now.
 */
export function dropIndex(
	mids: readonly number[],
	from: number,
	top: number,
	bottom: number
): number {
	let to = from;
	for (let i = 0; i < mids.length; i++) {
		if (i < from && mids[i] > top) to--;
		else if (i > from && mids[i] < bottom) to++;
	}
	return to;
}

/**
 * Which way item `i` moves while the item at `from` is held over `to`:
 * -1 up (it fills the gap), 1 down (it makes room), 0 not at all (also
 * for the dragged item itself).
 */
export function shiftOf(i: number, from: number, to: number): -1 | 0 | 1 {
	if (from < to && i > from && i <= to) return -1;
	if (to < from && i >= to && i < from) return 1;
	return 0;
}

/**
 * The keyboard move of a drag handle: ArrowUp/ArrowDown one place (with or
 * without Alt), Home/End to the first or last place. Null for other keys or
 * when the item cannot move that way.
 */
export function keyTarget(key: string, index: number, length: number): number | null {
	const to =
		key === 'ArrowUp'
			? index - 1
			: key === 'ArrowDown'
				? index + 1
				: key === 'Home'
					? 0
					: key === 'End'
						? length - 1
						: null;
	if (to === null || to < 0 || to >= length || to === index) return null;
	return to;
}

/** The polite announcement after a move: "Moved Link 2 to position 1 of 3." */
export function movedMessage(name: string, to: number, length: number): string {
	return `Moved ${name} to position ${to + 1} of ${length}.`;
}
