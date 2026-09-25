// Table model and pure helpers (#22): sorting, selection and the
// virtualization window. Components: Table.svelte.
import type { Snippet } from 'svelte';

export type SortDirection = 'asc' | 'desc';

export interface SortState {
	column: string;
	direction: SortDirection;
}

export interface Column<T> {
	id: string;
	header: string;
	/** Cell content; default: String(row[id]). */
	cell?: Snippet<[T]>;
	/** Makes the column sortable (client-side unless Table.manualSort). */
	sortValue?: (row: T) => string | number | null | undefined;
	align?: 'start' | 'end';
	/** CSS width, e.g. "120px" or "20%". */
	width?: string;
	/** Tabular numbers, right-aligned. */
	numeric?: boolean;
	mono?: boolean;
	/** Visually hide the header text (it still labels the column). */
	hideHeader?: boolean;
	/**
	 * Role in stacked mode (<768 px): title and status on the first line,
	 * meta as label/value pairs, actions at the end, hidden omitted.
	 * Default: meta.
	 */
	stack?: 'title' | 'status' | 'meta' | 'actions' | 'hidden';
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

/** Compares sort values: null/undefined last in both directions. */
export function compareValues(
	a: string | number | null | undefined,
	b: string | number | null | undefined,
	direction: SortDirection
): number {
	const an = a === null || a === undefined || a === '';
	const bn = b === null || b === undefined || b === '';
	if (an || bn) return an === bn ? 0 : an ? 1 : -1;
	const c =
		typeof a === 'number' && typeof b === 'number'
			? a - b
			: collator.compare(String(a), String(b));
	return direction === 'asc' ? c : -c;
}

/** Returns rows sorted by the sort state (stable; a new array). */
export function sortRows<T>(
	rows: readonly T[],
	columns: readonly Column<T>[],
	sort: SortState | null
): T[] {
	const col = sort ? columns.find((c) => c.id === sort.column) : undefined;
	if (!sort || !col?.sortValue) return [...rows];
	const get = col.sortValue;
	return rows
		.map((row, i) => ({ row, i, v: get(row) }))
		.sort((x, y) => compareValues(x.v, y.v, sort.direction) || x.i - y.i)
		.map((x) => x.row);
}

/** The next sort state after activating a column's header. */
export function nextSort(current: SortState | null, column: string): SortState {
	if (current?.column !== column) return { column, direction: 'asc' };
	return { column, direction: current.direction === 'asc' ? 'desc' : 'asc' };
}

/** Header checkbox state for a selection over the visible keys. */
export function selectionState(
	visible: readonly string[],
	selected: readonly string[]
): boolean | 'mixed' {
	if (visible.length === 0) return false;
	const set = new Set(selected);
	const n = visible.filter((k) => set.has(k)).length;
	return n === 0 ? false : n === visible.length ? true : 'mixed';
}

/** Toggles all visible keys (keeps selected keys that are not visible). */
export function toggleAll(visible: readonly string[], selected: readonly string[]): string[] {
	const state = selectionState(visible, selected);
	const vis = new Set(visible);
	if (state === true) return selected.filter((k) => !vis.has(k));
	return [...new Set([...selected, ...visible])];
}

export interface VirtualWindow {
	start: number;
	end: number;
	padTop: number;
	padBottom: number;
}

/**
 * The rows to render for a scroll position (fixed row height): [start, end)
 * plus spacer heights above and below. overscan rows are added on both
 * sides so fast scrolling does not show blank space.
 */
export function virtualWindow(
	scrollTop: number,
	viewport: number,
	rowHeight: number,
	count: number,
	overscan = 8
): VirtualWindow {
	if (count === 0 || rowHeight <= 0) return { start: 0, end: 0, padTop: 0, padBottom: 0 };
	const first = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
	const visible = Math.ceil(Math.max(viewport, rowHeight) / rowHeight);
	const end = Math.min(count, first + visible + 2 * overscan);
	return { start: first, end, padTop: first * rowHeight, padBottom: (count - end) * rowHeight };
}
