// Line diff for DiffView (#22: revision compare, #7). Myers' O(ND)
// algorithm in linear space (middle-snake bisection, as diff-match-patch
// does) over interned lines, then hunks with a few lines of context. Pure
// and dependency-free (CodeMirror's merge view would add a lazy chunk for
// no gain here). Memory stays O(N+M) and each bisection gives up after
// MAX_BISECT_D steps, so a large rewrite yields a correct, possibly
// non-minimal script instead of freezing the tab.

export type DiffKind = 'same' | 'add' | 'del';

export interface DiffLine {
	kind: DiffKind;
	text: string;
	/** 1-based line number in the old text (absent for additions). */
	oldNo?: number;
	/** 1-based line number in the new text (absent for deletions). */
	newNo?: number;
}

export interface DiffHunk {
	lines: DiffLine[];
	/** Unchanged lines hidden before this hunk. */
	skippedBefore: number;
}

export interface DiffResult {
	hunks: DiffHunk[];
	/** Unchanged lines hidden after the last hunk. */
	skippedAfter: number;
	added: number;
	removed: number;
	/** Both texts are identical. */
	same: boolean;
}

/** Edit steps one bisection explores before it splits no further. */
const MAX_BISECT_D = 4096;

/** Splits text into lines; a trailing newline does not add an empty line. */
export function splitLines(text: string): string[] {
	if (text === '') return [];
	const lines = text.replace(/\r\n/g, '\n').split('\n');
	if (lines[lines.length - 1] === '') lines.pop();
	return lines;
}

/**
 * The point where a shortest edit script of a[aLo:aHi] → b[bLo:bHi]
 * crosses its middle (both ranges non-empty, without a common first or
 * last line), or null when the search exceeds MAX_BISECT_D.
 */
function bisect(
	a: Int32Array,
	aLo: number,
	aHi: number,
	b: Int32Array,
	bLo: number,
	bHi: number
): [number, number] | null {
	const n = aHi - aLo;
	const m = bHi - bLo;
	const maxD = Math.min(Math.ceil((n + m) / 2), MAX_BISECT_D);
	const off = maxD;
	const len = 2 * maxD + 2;
	const v1 = new Int32Array(len).fill(-1);
	const v2 = new Int32Array(len).fill(-1);
	v1[off + 1] = 0;
	v2[off + 1] = 0;
	const delta = n - m;
	// With an odd delta the forward path meets the reverse one, else the
	// reverse meets the forward one.
	const front = delta % 2 !== 0;
	// Diagonals that ran off the right or bottom edge are skipped.
	let k1start = 0;
	let k1end = 0;
	let k2start = 0;
	let k2end = 0;
	for (let d = 0; d < maxD; d++) {
		for (let k1 = -d + k1start; k1 <= d - k1end; k1 += 2) {
			const i1 = off + k1;
			let x1 =
				k1 === -d || (k1 !== d && v1[i1 - 1] < v1[i1 + 1]) ? v1[i1 + 1] : v1[i1 - 1] + 1;
			let y1 = x1 - k1;
			while (x1 < n && y1 < m && a[aLo + x1] === b[bLo + y1]) {
				x1++;
				y1++;
			}
			v1[i1] = x1;
			if (x1 > n) k1end += 2;
			else if (y1 > m) k1start += 2;
			else if (front) {
				const i2 = off + delta - k1;
				if (i2 >= 0 && i2 < len && v2[i2] !== -1 && x1 >= n - v2[i2])
					return [aLo + x1, bLo + y1];
			}
		}
		for (let k2 = -d + k2start; k2 <= d - k2end; k2 += 2) {
			const i2 = off + k2;
			let x2 =
				k2 === -d || (k2 !== d && v2[i2 - 1] < v2[i2 + 1]) ? v2[i2 + 1] : v2[i2 - 1] + 1;
			let y2 = x2 - k2;
			while (x2 < n && y2 < m && a[aHi - x2 - 1] === b[bHi - y2 - 1]) {
				x2++;
				y2++;
			}
			v2[i2] = x2;
			if (x2 > n) k2end += 2;
			else if (y2 > m) k2start += 2;
			else if (!front) {
				const i1 = off + delta - k2;
				if (i1 >= 0 && i1 < len && v1[i1] !== -1) {
					const x1 = v1[i1];
					if (x1 >= n - x2) return [aLo + x1, bLo + off + x1 - i1];
				}
			}
		}
	}
	return null;
}

/** The edit script turning a into b, line by line (Myers). */
export function diffLines(a: string[], b: string[]): DiffLine[] {
	// Equal lines share one number, so comparisons never touch the text.
	const ids = new Map<string, number>();
	const intern = (s: string) => {
		let id = ids.get(s);
		if (id === undefined) ids.set(s, (id = ids.size));
		return id;
	};
	const ai = Int32Array.from(a, intern);
	const bi = Int32Array.from(b, intern);
	const del = new Uint8Array(a.length);
	const add = new Uint8Array(b.length);
	// Ranges still to diff, four numbers each: aLo, aHi, bLo, bHi.
	const todo = [0, a.length, 0, b.length];
	while (todo.length) {
		let bHi = todo.pop()!;
		let bLo = todo.pop()!;
		let aHi = todo.pop()!;
		let aLo = todo.pop()!;
		while (aLo < aHi && bLo < bHi && ai[aLo] === bi[bLo]) {
			aLo++;
			bLo++;
		}
		while (aLo < aHi && bLo < bHi && ai[aHi - 1] === bi[bHi - 1]) {
			aHi--;
			bHi--;
		}
		const split = aLo < aHi && bLo < bHi ? bisect(ai, aLo, aHi, bi, bLo, bHi) : null;
		if (split) {
			const [x, y] = split;
			todo.push(x, aHi, y, bHi, aLo, x, bLo, y);
			continue;
		}
		// One side is empty, or the search gave up: replace the range.
		del.fill(1, aLo, aHi);
		add.fill(1, bLo, bHi);
	}
	// Unmarked lines are the common subsequence: walk both sides together,
	// deletions before additions within a change.
	const out: DiffLine[] = [];
	let x = 0;
	let y = 0;
	while (x < a.length || y < b.length) {
		if (x < a.length && (del[x] || y >= b.length)) {
			out.push({ kind: 'del', text: a[x], oldNo: x + 1 });
			x++;
		} else if (y < b.length && (add[y] || x >= a.length)) {
			out.push({ kind: 'add', text: b[y], newNo: y + 1 });
			y++;
		} else {
			out.push({ kind: 'same', text: a[x], oldNo: x + 1, newNo: y + 1 });
			x++;
			y++;
		}
	}
	return out;
}

/** An edit script grouped into hunks with `context` unchanged lines. */
export function hunkLines(lines: DiffLine[], context = 3): DiffResult {
	let added = 0;
	let removed = 0;
	const keep = new Uint8Array(lines.length);
	// Marks each line once: a context range starts where the last one ended.
	let keptTo = 0;
	lines.forEach((l, i) => {
		if (l.kind === 'same') return;
		if (l.kind === 'add') added++;
		else removed++;
		const to = Math.min(lines.length, i + context + 1);
		keep.fill(1, Math.max(keptTo, i - context), to);
		keptTo = Math.max(keptTo, to);
	});
	const hunks: DiffHunk[] = [];
	let skipped = 0;
	let cur: DiffHunk | null = null;
	lines.forEach((l, i) => {
		if (!keep[i]) {
			skipped++;
			cur = null;
			return;
		}
		if (!cur) {
			cur = { lines: [], skippedBefore: skipped };
			hunks.push(cur);
			skipped = 0;
		}
		cur.lines.push(l);
	});
	return { hunks, skippedAfter: skipped, added, removed, same: added === 0 && removed === 0 };
}

/** Diff of two texts grouped into hunks with `context` unchanged lines. */
export function diffText(before: string, after: string, context = 3): DiffResult {
	return hunkLines(diffLines(splitLines(before), splitLines(after)), context);
}
