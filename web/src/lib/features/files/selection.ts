// Selection model of the file list (#15): desktop semantics as pure
// functions over the displayed order of row keys (paths).
//
//   click            select only this row (anchor and cursor move to it)
//   Ctrl/Cmd-click   toggle this row (anchor moves to it)
//   Shift-click      select the range anchor..row (replaces the selection)
//   Ctrl+Shift-click add the range anchor..row to the selection
//   arrows           move the cursor and select it; with Shift extend the
//                    range from the anchor; with Ctrl only move the cursor
//   Space            toggle the cursor row; Ctrl/Cmd+A select all
//
// The cursor is the keyboard focus row (aria-activedescendant); the anchor
// is where range selections start.

export interface Selection {
	/** Selected keys in selection order. */
	selected: string[];
	anchor: string | null;
	cursor: string | null;
}

export const EMPTY: Selection = { selected: [], anchor: null, cursor: null };

export interface ClickModifiers {
	/** Ctrl (Windows/Linux) or Cmd (macOS). */
	toggle?: boolean;
	/** Shift. */
	range?: boolean;
}

function rangeKeys(keys: readonly string[], from: string, to: string): string[] {
	let a = keys.indexOf(from);
	const b = keys.indexOf(to);
	if (b < 0) return [];
	if (a < 0) a = b;
	const [lo, hi] = a <= b ? [a, b] : [b, a];
	return keys.slice(lo, hi + 1);
}

function union(a: readonly string[], b: readonly string[]): string[] {
	const seen = new Set(a);
	return [...a, ...b.filter((k) => !seen.has(k))];
}

/** A pointer click on `key`. */
export function click(
	s: Selection,
	keys: readonly string[],
	key: string,
	mods: ClickModifiers = {}
): Selection {
	if (!keys.includes(key)) return s;
	if (mods.range) {
		const anchor = s.anchor && keys.includes(s.anchor) ? s.anchor : key;
		const range = rangeKeys(keys, anchor, key);
		return { selected: mods.toggle ? union(s.selected, range) : range, anchor, cursor: key };
	}
	if (mods.toggle) {
		const on = s.selected.includes(key);
		return {
			selected: on ? s.selected.filter((k) => k !== key) : [...s.selected, key],
			anchor: key,
			cursor: key
		};
	}
	return { selected: [key], anchor: key, cursor: key };
}

/** Toggles one row (its checkbox) without touching the rest. */
export function toggle(s: Selection, key: string): Selection {
	const on = s.selected.includes(key);
	return {
		selected: on ? s.selected.filter((k) => k !== key) : [...s.selected, key],
		anchor: key,
		cursor: key
	};
}

export type Move = number | 'first' | 'last';
export type MoveMode = 'select' | 'extend' | 'cursor';

/**
 * Moves the cursor by `delta` rows (or to the first/last row). `select`
 * selects the new cursor row only, `extend` selects anchor..cursor,
 * `cursor` leaves the selection alone (Ctrl+arrows).
 */
export function move(
	s: Selection,
	keys: readonly string[],
	delta: Move,
	mode: MoveMode = 'select'
): Selection {
	if (keys.length === 0) return s;
	const cur = s.cursor ? keys.indexOf(s.cursor) : -1;
	let next: number;
	if (delta === 'first') next = 0;
	else if (delta === 'last') next = keys.length - 1;
	else if (cur < 0) next = delta > 0 ? 0 : keys.length - 1;
	else next = Math.min(keys.length - 1, Math.max(0, cur + delta));
	const key = keys[next];
	if (mode === 'cursor') return { ...s, cursor: key };
	if (mode === 'extend') {
		const anchor = s.anchor && keys.includes(s.anchor) ? s.anchor : (s.cursor ?? key);
		return { selected: rangeKeys(keys, anchor, key), anchor, cursor: key };
	}
	return { selected: [key], anchor: key, cursor: key };
}

/** Space: toggles the cursor row. */
export function toggleCursor(s: Selection): Selection {
	return s.cursor ? toggle(s, s.cursor) : s;
}

export function selectAll(s: Selection, keys: readonly string[]): Selection {
	return {
		selected: [...keys],
		anchor: s.anchor ?? keys[0] ?? null,
		cursor: s.cursor ?? keys[0] ?? null
	};
}

export function clear(s: Selection): Selection {
	return { selected: [], anchor: s.cursor, cursor: s.cursor };
}

/**
 * Keeps only keys that still exist (after a refresh or a live update);
 * the cursor falls back to the nearest remaining row.
 */
export function retain(s: Selection, keys: readonly string[]): Selection {
	const present = new Set(keys);
	const selected = s.selected.filter((k) => present.has(k));
	const cursor = s.cursor && present.has(s.cursor) ? s.cursor : null;
	const anchor = s.anchor && present.has(s.anchor) ? s.anchor : cursor;
	if (selected.length === s.selected.length && cursor === s.cursor && anchor === s.anchor)
		return s;
	return { selected, anchor, cursor };
}

/**
 * The keys an action applies to, in display order. Actions apply to the
 * selection only, never silently to an unselected cursor row.
 */
export function targets(s: Selection, keys: readonly string[]): string[] {
	const on = new Set(s.selected);
	return keys.filter((k) => on.has(k));
}
