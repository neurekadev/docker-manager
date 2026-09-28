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

/** What the viewer shows of the buffered lines. */
export interface LineFilter {
	/** Whether the line's source is shown (the service chips); absent: all. */
	source?: (key: string) => boolean;
	/** Only lines written to standard error. */
	errorsOnly?: boolean;
	/** Only lines containing `query` (lower case; empty: every line). */
	query?: string;
}

/** The lines that pass the filter, in order (the input when nothing filters). */
export function filterLines(lines: readonly LogLine[], f: LineFilter): readonly LogLine[] {
	const q = f.query ?? '';
	if (!f.source && !f.errorsOnly && !q) return lines;
	return lines.filter(
		(l) =>
			(!f.source || f.source(l.source)) &&
			(!f.errorsOnly || l.stream === 'stderr') &&
			(!q || l.text.toLowerCase().includes(q))
	);
}

/** How many of the lines contain `q` (lower case). */
export function countMatches(lines: readonly LogLine[], q: string): number {
	if (!q) return 0;
	let n = 0;
	for (const l of lines) if (l.text.toLowerCase().includes(q)) n++;
	return n;
}

/**
 * The service chips' state: `only` (from ?service=<name>) shows that one
 * service while it exists; otherwise every service not in `hidden`.
 */
export function serviceShown(
	service: string,
	o: { only?: string | null; hidden: readonly string[]; services: readonly string[] }
): boolean {
	if (o.only && o.services.includes(o.only)) return service === o.only;
	return !o.hidden.includes(service);
}

/** The hidden services after toggling `service`, starting from `only` if set. */
export function toggleHidden(
	service: string,
	o: { only?: string | null; hidden: readonly string[]; services: readonly string[] }
): string[] {
	const hidden =
		o.only && o.services.includes(o.only)
			? o.services.filter((s) => s !== o.only)
			: [...o.hidden];
	return hidden.includes(service) ? hidden.filter((s) => s !== service) : [...hidden, service];
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
