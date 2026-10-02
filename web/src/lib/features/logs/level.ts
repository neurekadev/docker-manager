// Log levels of the log viewer (#8): read from the line's text in the common
// formats (`level=warn`, `"level":"info"`, pino's numbers, `[error]`,
// `ERROR`, `WRN`, `LOG:`, glog's `E0925`, `panic:`, `ValueError:`), so the
// level filter works on any container. Lines without a level are "Other";
// indented continuation lines (stack traces) take the level of the line
// before them from the same container (`continues`).

export type LogLevel = 'error' | 'warning' | 'info' | 'debug' | 'verbose' | 'other';
export type LogStream = 'stdout' | 'stderr';

/** The level filter's options, most severe first. */
export const LOG_LEVELS: readonly { value: LogLevel; label: string; hue: string }[] = [
	{ value: 'error', label: 'Error', hue: 'var(--danger)' },
	{ value: 'warning', label: 'Warning', hue: 'var(--warn)' },
	{ value: 'info', label: 'Info', hue: 'var(--info)' },
	{ value: 'debug', label: 'Debug', hue: 'var(--tile-violet-fg)' },
	{ value: 'verbose', label: 'Verbose', hue: 'var(--text-muted)' },
	{ value: 'other', label: 'Other', hue: 'var(--text-faint)' }
];

export const LOG_STREAMS: readonly { value: LogStream; label: string; short: string }[] = [
	{ value: 'stdout', label: 'Standard output', short: 'stdout' },
	{ value: 'stderr', label: 'Standard error', short: 'stderr' }
];

const WORDS: Record<string, LogLevel> = {
	error: 'error',
	err: 'error',
	eror: 'error',
	fatal: 'error',
	ftl: 'error',
	crit: 'error',
	critical: 'error',
	panic: 'error',
	emerg: 'error',
	emergency: 'error',
	alert: 'error',
	severe: 'error',
	warn: 'warning',
	warning: 'warning',
	wrn: 'warning',
	info: 'info',
	inf: 'info',
	information: 'info',
	informational: 'info',
	notice: 'info',
	debug: 'debug',
	dbg: 'debug',
	trace: 'verbose',
	trc: 'verbose',
	verbose: 'verbose',
	vrb: 'verbose',
	finest: 'verbose'
};

/** A level word ("WARN", "Debug3"), or undefined. */
function word(w: string): LogLevel | undefined {
	return WORDS[w.toLowerCase().replace(/[1-5]$/, '')];
}

/** pino and bunyan: 10 trace, 20 debug, 30 info, 40 warn, 50 error, 60 fatal. */
function numeric(n: number): LogLevel | undefined {
	if (n >= 50 && n <= 60) return 'error';
	if (n >= 40) return 'warning';
	if (n >= 30) return 'info';
	if (n >= 20) return 'debug';
	if (n >= 10) return 'verbose';
	return undefined;
}

// level=info, "level":"warn", severity: ERROR, "level":30
const KEY =
	/(?:^|[\s{,;"'])(?:level|lvl|severity|loglevel|log_level|log\.level)["']?\s*[=:]\s*["']?([a-z]+[1-5]?|\d{2})\b/gi;
// [error], <warn>, (Info)
const BRACKET = /[[<(]\s*([a-z]+[1-5]?)\s*[\]>)]/gi;
// Upper-case words only: lower-case ones are too common in messages.
const UPPER =
	/(?:^|[^\w-])(ERROR|ERR|EROR|FATAL|FTL|CRIT|CRITICAL|PANIC|EMERG|ALERT|SEVERE|WARN|WARNING|WRN|INFO|INF|NOTICE|DEBUG[1-5]?|DBG|TRACE|TRC|VERBOSE|VRB|LOG(?=:))(?![\w-])/;
// error: …, warning[E0425]: …, panic: …, npm ERR!
const START = /^\s*([a-z]+)(?=[:[!])/i;
// glog / klog: I0925 10:14:22.123456 …
const GLOG = /^([IWEF])\d{4} \d\d:\d\d:\d\d/;
const GLOG_LEVELS: Record<string, LogLevel> = { I: 'info', W: 'warning', E: 'error', F: 'error' };
// ValueError: …, java.lang.IllegalStateException: …, Exception in thread …
const EXCEPTION = /^[\w.$]*(?:Error|Exception)\b/;

/** How far into a line its level is looked for (prefixes, not messages). */
const HEAD = 160;

/** The level a line's text names, or 'other'. */
export function detectLevel(text: string): LogLevel {
	const g = GLOG.exec(text);
	if (g) return GLOG_LEVELS[g[1]];
	for (const m of text.slice(0, 2000).matchAll(KEY)) {
		const v = m[1];
		const level = /^\d+$/.test(v) ? numeric(Number(v)) : word(v);
		if (level) return level;
	}
	const head = text.slice(0, HEAD);
	let best: { at: number; level: LogLevel } | undefined;
	for (const m of head.matchAll(BRACKET)) {
		const level = word(m[1]);
		if (level) {
			best = { at: m.index, level };
			break;
		}
	}
	const u = UPPER.exec(head);
	if (u) {
		const at = u.index + u[0].length - u[1].length;
		const level = u[1] === 'LOG' ? 'info' : word(u[1]);
		if (level && (!best || at < best.at)) best = { at, level };
	}
	const s = START.exec(head);
	if (s) {
		const level = word(s[1]);
		const at = s.index + s[0].length - s[1].length;
		if (level && (!best || at < best.at)) best = { at, level };
	}
	if (best) return best.level;
	if (EXCEPTION.test(text) || text.startsWith('Traceback (most recent call last)'))
		return 'error';
	return 'other';
}

/** Whether a line continues the one before it (an indented stack frame). */
export function continues(text: string): boolean {
	return /^[ \t]+\S/.test(text) || /^(?:Caused by:|\.\.\. \d+ more)/.test(text);
}

// CSI sequences (colours, cursor moves) and OSC sequences (titles, links).
// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g;

/** The text without terminal escape sequences (coloured log output). */
export function stripAnsi(text: string): string {
	return text.includes('\x1b') ? text.replace(ANSI, '') : text;
}
