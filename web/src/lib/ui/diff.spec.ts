import { describe, expect, it } from 'vitest';
import { diffLines, diffText, hunkLines, splitLines } from './diff';

// Rebuilds both sides from an edit script: the script is only correct if
// it reproduces the old text (same + del) and the new one (same + add).
function sides(lines: ReturnType<typeof diffLines>) {
	return {
		before: lines.filter((l) => l.kind !== 'add').map((l) => l.text),
		after: lines.filter((l) => l.kind !== 'del').map((l) => l.text)
	};
}

describe('diff', () => {
	it('splits lines without a phantom last line', () => {
		expect(splitLines('')).toEqual([]);
		expect(splitLines('a\nb\n')).toEqual(['a', 'b']);
		expect(splitLines('a\r\nb')).toEqual(['a', 'b']);
	});

	it('finds the minimal edit of a changed line with line numbers on both sides', () => {
		const out = diffLines(
			['services:', '  web:', '    image: nginx:1.26'],
			['services:', '  web:', '    image: nginx:1.27']
		);
		expect(out.map((l) => [l.kind, l.oldNo, l.newNo])).toEqual([
			['same', 1, 1],
			['same', 2, 2],
			['del', 3, undefined],
			['add', undefined, 3]
		]);
	});

	it('reproduces both texts for arbitrary edits (deterministic pseudo-random corpus)', () => {
		let seed = 42;
		const rnd = (n: number) => {
			seed = (seed * 1103515245 + 12345) % 2147483648;
			return seed % n;
		};
		const words = ['a', 'b', 'c', 'd', 'e'];
		for (let run = 0; run < 200; run++) {
			const a = Array.from({ length: rnd(12) }, () => words[rnd(5)]);
			const b = Array.from({ length: rnd(12) }, () => words[rnd(5)]);
			const out = diffLines(a, b);
			expect(sides(out)).toEqual({ before: a, after: b });
		}
	});

	it('reproduces both texts for longer edits that need several bisections', () => {
		let seed = 7;
		const rnd = (n: number) => {
			seed = (seed * 1103515245 + 12345) % 2147483648;
			return seed % n;
		};
		for (let run = 0; run < 50; run++) {
			const a = Array.from({ length: 50 + rnd(300) }, () => `line ${rnd(40)}`);
			const b = a.flatMap((l) => {
				const r = rnd(10);
				if (r === 0) return [];
				if (r === 1) return [l, `new ${rnd(40)}`];
				if (r === 2) return [`changed ${rnd(40)}`];
				return [l];
			});
			expect(sides(diffLines(a, b))).toEqual({ before: a, after: b });
		}
	});

	it('keeps the edit minimal across separate changes', () => {
		const a = ['a', 'b', 'c', 'd', 'e', 'f', 'g'];
		const b = ['a', 'x', 'c', 'd', 'e', 'g', 'y'];
		const out = diffLines(a, b);
		// b → x, f removed, y added: 4 edits around the common a c d e g.
		expect(out.filter((l) => l.kind !== 'same')).toHaveLength(4);
		expect(sides(out)).toEqual({ before: a, after: b });
	});

	it('stays correct when a large rewrite exceeds the search bound', () => {
		const a = Array.from({ length: 12000 }, (_, i) => `old ${i}`);
		const b = a.map((l, i) => (i % 1000 === 0 ? l : `new ${i}`));
		const out = diffLines(a, b);
		expect(sides(out)).toEqual({ before: a, after: b });
		expect(out[0]).toEqual({ kind: 'same', text: 'old 0', oldNo: 1, newNo: 1 });
	});

	it('regroups an edit script without diffing again', () => {
		const before = Array.from({ length: 30 }, (_, i) => `line ${i + 1}`).join('\n');
		const after = before.replace('line 15', 'line fifteen');
		const script = diffLines(splitLines(before), splitLines(after));
		expect(hunkLines(script, 3)).toEqual(diffText(before, after, 3));
		const all = hunkLines(script, Number.MAX_SAFE_INTEGER);
		expect(all.hunks).toHaveLength(1);
		expect(all.hunks[0].lines).toHaveLength(31);
		expect(all.skippedAfter).toBe(0);
	});

	it('groups changes into hunks with context and counts the hidden lines', () => {
		const before = Array.from({ length: 30 }, (_, i) => `line ${i + 1}`).join('\n');
		const after = before.replace('line 15', 'line fifteen');
		const r = diffText(before, after, 3);
		expect(r.added).toBe(1);
		expect(r.removed).toBe(1);
		expect(r.same).toBe(false);
		expect(r.hunks).toHaveLength(1);
		expect(r.hunks[0].skippedBefore).toBe(11);
		expect(r.hunks[0].lines).toHaveLength(8);
		expect(r.skippedAfter).toBe(12);
	});

	it('reports identical texts', () => {
		const r = diffText('a\nb\n', 'a\nb\n');
		expect(r.same).toBe(true);
		expect(r.hunks).toEqual([]);
	});

	it('shows a new file as additions only', () => {
		const r = diffText('', 'KEY=1\nOTHER=2\n');
		expect(r.added).toBe(2);
		expect(r.removed).toBe(0);
		expect(r.hunks[0].lines.every((l) => l.kind === 'add')).toBe(true);
	});
});
