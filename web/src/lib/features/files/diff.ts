// Line diff for the external-change conflict's "Compare" (#15): what is on
// disk now against the unsaved buffer. Myers' O((N+M)D) algorithm with a
// bound on D; beyond it the comparison reports `tooLarge` instead of
// freezing the tab.

export type DiffOp = { type: 'equal' | 'add' | 'remove'; text: string };

export interface LineDiff {
	ops: DiffOp[];
	added: number;
	removed: number;
	/** The edit distance exceeded the bound: no ops were computed. */
	tooLarge: boolean;
}

export function splitLines(s: string): string[] {
	if (s === '') return [];
	const lines = s.replace(/\r\n?/g, '\n').split('\n');
	if (lines[lines.length - 1] === '') lines.pop();
	return lines;
}

/** Diff from `a` (disk) to `b` (buffer). */
export function diffLines(a: string, b: string, maxEdits = 4000): LineDiff {
	const x = splitLines(a);
	const y = splitLines(b);
	const n = x.length;
	const m = y.length;
	const max = Math.min(n + m, maxEdits);
	const offset = max + 1;
	const v = new Int32Array(2 * max + 3);
	const trace: Int32Array[] = [];
	let found = n === 0 && m === 0;
	for (let d = 0; d <= max && !found; d++) {
		trace.push(v.slice());
		for (let k = -d; k <= d; k += 2) {
			let px: number;
			if (k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1]))
				px = v[offset + k + 1];
			else px = v[offset + k - 1] + 1;
			let py = px - k;
			while (px < n && py < m && x[px] === y[py]) {
				px++;
				py++;
			}
			v[offset + k] = px;
			if (px >= n && py >= m) {
				found = true;
				break;
			}
		}
	}
	if (!found) return { ops: [], added: 0, removed: 0, tooLarge: true };
	// Backtrack from the end through the frontiers saved before each step
	// (trace[d] is the state before edit d).
	const ops: DiffOp[] = [];
	let px = n;
	let py = m;
	for (let d = trace.length - 1; d >= 0; d--) {
		const vd = trace[d];
		const k = px - py;
		let prevK: number;
		if (k === -d || (k !== d && vd[offset + k - 1] < vd[offset + k + 1])) prevK = k + 1;
		else prevK = k - 1;
		const prevX = d === 0 ? 0 : vd[offset + prevK];
		const prevY = prevX - prevK;
		while (px > prevX && py > prevY) {
			ops.push({ type: 'equal', text: x[px - 1] });
			px--;
			py--;
		}
		if (d > 0) {
			if (px === prevX) ops.push({ type: 'add', text: y[py - 1] });
			else ops.push({ type: 'remove', text: x[px - 1] });
		}
		px = prevX;
		py = prevY;
	}
	ops.reverse();
	let added = 0;
	let removed = 0;
	for (const o of ops) {
		if (o.type === 'add') added++;
		else if (o.type === 'remove') removed++;
	}
	return { ops, added, removed, tooLarge: false };
}

export interface DiffRow {
	type: 'equal' | 'add' | 'remove' | 'skip';
	text: string;
	/** Line numbers in the disk version and the buffer (null when absent). */
	a: number | null;
	b: number | null;
}

/** Rows for display: changed lines with `context` unchanged lines around them. */
export function diffRows(ops: readonly DiffOp[], context = 3): DiffRow[] {
	const rows: DiffRow[] = [];
	let a = 0;
	let b = 0;
	const numbered = ops.map((o) => {
		if (o.type === 'equal') {
			a++;
			b++;
			return { ...o, a, b };
		}
		if (o.type === 'remove') {
			a++;
			return { ...o, a, b: null };
		}
		b++;
		return { ...o, a: null, b };
	});
	const keep = new Array<boolean>(numbered.length).fill(false);
	numbered.forEach((o, i) => {
		if (o.type === 'equal') return;
		for (let j = Math.max(0, i - context); j <= Math.min(numbered.length - 1, i + context); j++)
			keep[j] = true;
	});
	let skipped = 0;
	numbered.forEach((o, i) => {
		if (keep[i]) {
			if (skipped)
				rows.push({ type: 'skip', text: `${skipped} unchanged lines`, a: null, b: null });
			skipped = 0;
			rows.push({ type: o.type, text: o.text, a: o.a, b: o.b });
		} else skipped++;
	});
	if (skipped && rows.length)
		rows.push({ type: 'skip', text: `${skipped} unchanged lines`, a: null, b: null });
	return rows;
}
