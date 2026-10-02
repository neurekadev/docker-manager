// Presentation helpers of the log viewer (#8): local timestamps like the
// mockup ("2024-04-27 10:14:22"), the search (plain or regular expression,
// matching lines only, highlighted), the level and stream filter and the
// download text.
import type { LogLine } from './feed.svelte';
import { LOG_LEVELS, LOG_STREAMS, type LogLevel, type LogStream } from './level';

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

/** [start, end) of a match in a line's text. */
export type Range = [number, number];

/** A search: whether a line matches and where (in order, none empty). */
export interface Matcher {
	test(line: LogLine): boolean;
	ranges(line: LogLine): Range[];
}

/**
 * The search box's plain text as a matcher (null when empty): case
 * folded unless `caseSensitive`, the spaces around it ignored.
 */
export function plainSearch(query: string, caseSensitive = false): Matcher | null {
	const q = caseSensitive ? query.trim() : query.trim().toLowerCase();
	if (!q) return null;
	const fold = (t: string) => (caseSensitive ? t : t.toLowerCase());
	return {
		test: (l) => fold(l.text).includes(q),
		ranges(l) {
			const t = fold(l.text);
			const out: Range[] = [];
			for (let i = t.indexOf(q); i >= 0; i = t.indexOf(q, i + q.length))
				out.push([i, i + q.length]);
			return out;
		}
	};
}

/** A regular expression search; the search worker runs it (regex-search.svelte.ts). */
export interface Pattern {
	source: string;
	flags: string;
}

/**
 * The search box's text as a regular expression: null when empty,
 * 'invalid' when it does not compile.
 */
export function regexPattern(query: string, caseSensitive = false): Pattern | 'invalid' | null {
	if (!query) return null;
	const flags = caseSensitive ? 'g' : 'gi';
	try {
		new RegExp(query, flags);
	} catch {
		return 'invalid';
	}
	return { source: query, flags };
}

/**
 * Each text's matches of the pattern: null when it does not match, its
 * ranges otherwise (empty when it matches only empty strings). Runs in the
 * search worker, where a pattern that backtracks for minutes can be
 * stopped.
 */
export function regexMatches(p: Pattern, texts: readonly string[]): (Range[] | null)[] {
	const re = new RegExp(p.source, p.flags);
	return texts.map((text) => {
		let found = false;
		const out: Range[] = [];
		for (const m of text.matchAll(re)) {
			found = true;
			if (m[0].length) out.push([m.index, m.index + m[0].length]);
		}
		return found ? out : null;
	});
}

/** Splits `text` around the match ranges (one plain part without one). */
export function highlight(text: string, ranges: readonly Range[]): Part[] {
	if (!ranges.length) return [{ text, match: false }];
	const out: Part[] = [];
	let i = 0;
	for (const [a, b] of ranges) {
		if (a > i) out.push({ text: text.slice(i, a), match: false });
		out.push({ text: text.slice(a, b), match: true });
		i = b;
	}
	if (i < text.length) out.push({ text: text.slice(i), match: false });
	return out;
}

/** What the viewer shows of the buffered lines. */
export interface LineFilter {
	/** Whether the line's source is shown (the service chips); absent: all. */
	source?: (key: string) => boolean;
	/** The levels shown; absent: all. */
	levels?: readonly LogLevel[];
	/** The output streams shown; absent: both. */
	streams?: readonly LogStream[];
	/** The search; absent: every line. */
	match?: Matcher | null;
}

/** The lines that pass the filter, in order (the input when nothing filters). */
export function filterLines(lines: readonly LogLine[], f: LineFilter): readonly LogLine[] {
	const levels = f.levels && f.levels.length < LOG_LEVELS.length ? f.levels : undefined;
	const streams = f.streams && f.streams.length < LOG_STREAMS.length ? f.streams : undefined;
	const m = f.match;
	if (!f.source && !levels && !streams && !m) return lines;
	return lines.filter(
		(l) =>
			(!f.source || f.source(l.source)) &&
			(!levels || levels.includes(l.level)) &&
			(!streams || streams.includes(l.stream)) &&
			(!m || m.test(l))
	);
}

/** How many of the lines have each level and come from each stream. */
export function tally(lines: readonly LogLine[]): Record<LogLevel | LogStream, number> {
	const out = {
		error: 0,
		warning: 0,
		info: 0,
		debug: 0,
		verbose: 0,
		other: 0,
		stdout: 0,
		stderr: 0
	};
	for (const l of lines) {
		out[l.level]++;
		out[l.stream]++;
	}
	return out;
}

/**
 * The level filter's button text: "All levels", the chosen levels ("Error,
 * Warning"; "4 levels" from three on), then a single chosen stream.
 */
export function levelSummary(levels: readonly LogLevel[], streams: readonly LogStream[]): string {
	if (!levels.length || !streams.length) return 'Nothing selected';
	const names = LOG_LEVELS.filter((l) => levels.includes(l.value)).map((l) => l.label);
	const head =
		names.length === LOG_LEVELS.length
			? 'All levels'
			: names.length > 2
				? `${names.length} levels`
				: names.join(', ');
	if (streams.length === LOG_STREAMS.length) return head;
	return `${head} · ${LOG_STREAMS.find((s) => s.value === streams[0])!.short}`;
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
