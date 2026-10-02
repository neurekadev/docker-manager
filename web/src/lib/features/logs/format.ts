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

/** How the search reads its text. */
export interface SearchOptions {
	/** Match upper and lower case exactly. */
	caseSensitive?: boolean;
	/** The text is a regular expression. */
	regex?: boolean;
}

/** A compiled search: whether a line matches and where. */
export interface Matcher {
	test(text: string): boolean;
	/** The [start, end) ranges of the matches, in order, none empty. */
	ranges(text: string): [number, number][];
}

/**
 * Compiles the search box's text: null when it is empty (every line
 * matches), 'invalid' for a regular expression that does not compile.
 * Plain text ignores the spaces around it.
 */
export function compileSearch(query: string, o: SearchOptions = {}): Matcher | 'invalid' | null {
	if (o.regex) {
		if (!query) return null;
		let re: RegExp;
		try {
			re = new RegExp(query, o.caseSensitive ? 'g' : 'gi');
		} catch {
			return 'invalid';
		}
		const one = new RegExp(re.source, o.caseSensitive ? '' : 'i');
		return {
			test: (text) => one.test(text),
			ranges(text) {
				const out: [number, number][] = [];
				for (const m of text.matchAll(re))
					if (m[0].length) out.push([m.index, m.index + m[0].length]);
				return out;
			}
		};
	}
	const q = o.caseSensitive ? query.trim() : query.trim().toLowerCase();
	if (!q) return null;
	const fold = (t: string) => (o.caseSensitive ? t : t.toLowerCase());
	return {
		test: (text) => fold(text).includes(q),
		ranges(text) {
			const t = fold(text);
			const out: [number, number][] = [];
			for (let i = t.indexOf(q); i >= 0; i = t.indexOf(q, i + q.length))
				out.push([i, i + q.length]);
			return out;
		}
	};
}

/** Splits `text` around the search's matches (one plain part without one). */
export function highlight(text: string, m: Matcher | null): Part[] {
	const ranges = m ? m.ranges(text) : [];
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
			(!m || m.test(l.text))
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
