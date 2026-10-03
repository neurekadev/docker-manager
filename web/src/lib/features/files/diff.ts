// Line diff for the external-change conflict's "Compare" (#15): what is on
// disk now against the unsaved buffer. The edit script comes from the
// shared linear-space Myers in $lib/ui/diff, which stays fast on any size
// (a huge rewrite may show as larger replaced blocks, never a frozen tab).
import { diffLines as editScript } from '$lib/ui/diff';

export type DiffOp = { type: 'equal' | 'add' | 'remove'; text: string };

export interface LineDiff {
	ops: DiffOp[];
	added: number;
	removed: number;
}

export function splitLines(s: string): string[] {
	if (s === '') return [];
	const lines = s.replace(/\r\n?/g, '\n').split('\n');
	if (lines[lines.length - 1] === '') lines.pop();
	return lines;
}

/** Diff from `a` (disk) to `b` (buffer). */
export function diffLines(a: string, b: string): LineDiff {
	let added = 0;
	let removed = 0;
	const ops = editScript(splitLines(a), splitLines(b)).map((l): DiffOp => {
		if (l.kind === 'add') added++;
		else if (l.kind === 'del') removed++;
		return {
			type: l.kind === 'same' ? 'equal' : l.kind === 'add' ? 'add' : 'remove',
			text: l.text
		};
	});
	return { ops, added, removed };
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
