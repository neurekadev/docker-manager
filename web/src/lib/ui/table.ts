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
	/**
	 * Caps the column's content width (e.g. "280px"); longer content wraps,
	 * or is cut with an ellipsis when `truncate` is set.
	 */
	maxWidth?: string;
	/**
	 * One line with an ellipsis instead of wrapping (names, images, paths).
	 * The full text is the cell's tooltip: `title(row)`, or the plain value
	 * for columns without a `cell` snippet. Pair it with `maxWidth`.
	 */
	truncate?: boolean;
	/** The cell's tooltip (the full value of a truncated cell). */
	title?: (row: T) => string | undefined;
	/**
	 * Pins the column to the table's right edge while the table scrolls
	 * sideways (row actions stay in reach). Use it for the last column.
	 */
	pin?: 'end';
	/** Tabular numbers, right-aligned. */
	numeric?: boolean;
	mono?: boolean;
	/** Visually hide the header text (it still labels the column). */
	hideHeader?: boolean;
	/**
	 * Role in stacked mode (<768 px): title and status on the first line
	 * (the status moves below a title too long to share the line; a
	 * column of badges or a time can be a status too), meta as label/value
	 * pairs that wrap long values, actions at the end, hidden omitted.
	 * `head`: at the end of the first line, fixed (a compact row action
	 * such as the "⋯" menu or a few icon buttons, so they do not take a
	 * line of their own). Default: meta.
	 */
	stack?: 'title' | 'status' | 'meta' | 'actions' | 'head' | 'hidden';
}

/** The tooltip of a cell: the column's title, else the plain value of a truncated column. */
export function cellTitle<T>(col: Column<T>, row: T): string | undefined {
	if (col.title) return col.title(row) || undefined;
	if (!col.truncate || col.cell) return undefined;
	const v = (row as Record<string, unknown>)[col.id];
	return v === null || v === undefined || v === '' ? undefined : String(v);
}

/** Inline style of a cell's content box (the column's maxWidth). */
export function cellStyle<T>(col: Column<T>): string | undefined {
	return col.maxWidth ? `max-width: ${col.maxWidth}` : undefined;
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
