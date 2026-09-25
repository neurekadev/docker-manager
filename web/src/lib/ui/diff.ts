// Line diff for DiffView (#22: revision compare, #7). Myers' O(ND)
// algorithm over lines, then hunks with a few lines of context. Pure and
// dependency-free (Compose files are small; CodeMirror's merge view would
// add a lazy chunk for no gain here).

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

/** Splits text into lines; a trailing newline does not add an empty line. */
export function splitLines(text: string): string[] {
	if (text === '') return [];
	const lines = text.replace(/\r\n/g, '\n').split('\n');
	if (lines[lines.length - 1] === '') lines.pop();
	return lines;
}

/** The edit script turning a into b, line by line (Myers). */
export function diffLines(a: string[], b: string[]): DiffLine[] {
	const n = a.length;
	const m = b.length;
	const max = n + m;
	const offset = max + 1;
	const v = new Int32Array(2 * max + 3);
	const trace: Int32Array[] = [];
	let done = false;
	for (let d = 0; d <= max && !done; d++) {
		trace.push(v.slice());
		for (let k = -d; k <= d; k += 2) {
			let x =
				k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1])
					? v[offset + k + 1]
					: v[offset + k - 1] + 1;
			let y = x - k;
			while (x < n && y < m && a[x] === b[y]) {
				x++;
				y++;
			}
			v[offset + k] = x;
			if (x >= n && y >= m) {
				done = true;
				break;
			}
		}
	}
	// Backtrack through the saved V arrays.
	const out: DiffLine[] = [];
	let x = n;
	let y = m;
	for (let d = trace.length - 1; d >= 0; d--) {
		const vd = trace[d];
		const k = x - y;
		const prevK =
			k === -d || (k !== d && vd[offset + k - 1] < vd[offset + k + 1]) ? k + 1 : k - 1;
		const prevX = vd[offset + prevK];
		const prevY = prevX - prevK;
		while (x > prevX && y > prevY) {
			out.push({ kind: 'same', text: a[x - 1], oldNo: x, newNo: y });
			x--;
			y--;
		}
		if (d > 0) {
			if (x === prevX) out.push({ kind: 'add', text: b[y - 1], newNo: y });
			else out.push({ kind: 'del', text: a[x - 1], oldNo: x });
		}
		x = prevX;
		y = prevY;
	}
	return out.reverse();
}

/** Diff of two texts grouped into hunks with `context` unchanged lines. */
export function diffText(before: string, after: string, context = 3): DiffResult {
	const lines = diffLines(splitLines(before), splitLines(after));
	const added = lines.filter((l) => l.kind === 'add').length;
	const removed = lines.filter((l) => l.kind === 'del').length;
	const keep = new Uint8Array(lines.length);
	lines.forEach((l, i) => {
		if (l.kind === 'same') return;
		for (let j = Math.max(0, i - context); j <= Math.min(lines.length - 1, i + context); j++)
			keep[j] = 1;
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
