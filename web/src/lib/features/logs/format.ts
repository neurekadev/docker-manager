// Presentation helpers of the log viewer (#8): local timestamps like the
// mockup ("2024-04-27 10:14:22"), search highlighting and the download text.
import type { LogLine } from './feed.svelte';

const pad = (n: number, w = 2) => String(n).padStart(w, '0');

/** "2026-09-25 12:14:22" in the browser's time zone. */
export function formatLogTime(at: string): string {
	const d = new Date(at);
	if (Number.isNaN(d.getTime())) return at;
	return (
		`${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
		`${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
	);
}

export interface Part {
	text: string;
	match: boolean;
}

/** Splits `text` around case-insensitive occurrences of `q` (lower case). */
export function highlight(text: string, q: string): Part[] {
	if (!q) return [{ text, match: false }];
	const lower = text.toLowerCase();
	const out: Part[] = [];
	let i = 0;
	for (;;) {
		const j = lower.indexOf(q, i);
		if (j < 0) break;
		if (j > i) out.push({ text: text.slice(i, j), match: false });
		out.push({ text: text.slice(j, j + q.length), match: true });
		i = j + q.length;
	}
	if (i < text.length) out.push({ text: text.slice(i), match: false });
	return out.length ? out : [{ text, match: false }];
}

/** The lines as a plain text file (RFC 3339 timestamps, UTC). */
export function logText(
	lines: readonly LogLine[],
	o: { timestamps?: boolean; source?: (key: string) => string } = {}
): string {
	return (
		lines
			.map((l) => {
				const parts: string[] = [];
				if (o.timestamps) parts.push(l.at);
				if (o.source) parts.push(o.source(l.source));
				if (l.stream === 'stderr') parts.push('stderr');
				parts.push(l.text);
				return parts.join(' ');
			})
			.join('\n') + (lines.length ? '\n' : '')
	);
}
