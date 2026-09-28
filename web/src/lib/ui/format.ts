// Number, size and time formatting (#22 copy rules: exact numbers, units
// the API defines in #5: CPU % of the environment's cores, bytes as
// binary units). Pure functions; `now` is injectable for tests.

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

/** Bytes in binary units with the mockup's short labels: 1.8 GB, 312 MB. */
export function formatBytes(bytes: number | null | undefined, digits = 1): string {
	if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) return '—';
	let v = Math.abs(bytes);
	let u = 0;
	while (v >= 1024 && u < UNITS.length - 1) {
		v /= 1024;
		u++;
	}
	const s =
		u === 0 || v >= 100 ? Math.round(v).toString() : v.toFixed(digits).replace(/\.0$/, '');
	return `${bytes < 0 ? '-' : ''}${s} ${UNITS[u]}`;
}

/** A percentage with one decimal below 10 %: 12.4%, 0.7%, 45%. */
export function formatPercent(v: number | null | undefined): string {
	if (v === null || v === undefined || !Number.isFinite(v)) return '—';
	return `${v < 10 ? v.toFixed(1) : v < 100 ? v.toFixed(1).replace(/\.0$/, '') : Math.round(v)}%`;
}

/**
 * A duration in its two largest units, one style everywhere: "1 s",
 * "45 s", "3 min 20 s", "17 h 9 min", "3 d 4 h". A zero second unit is
 * left out ("3 min", "2 h"). Invalid or negative values read as "0 s".
 */
export function formatDuration(seconds: number): string {
	const s = Math.max(0, Math.round(Number.isFinite(seconds) ? seconds : 0));
	const pair = (big: number, bigUnit: string, small: number, smallUnit: string) =>
		small ? `${big} ${bigUnit} ${small} ${smallUnit}` : `${big} ${bigUnit}`;
	if (s < 60) return `${s} s`;
	const m = Math.floor(s / 60);
	if (m < 60) return pair(m, 'min', s % 60, 's');
	const h = Math.floor(m / 60);
	if (h < 24) return pair(h, 'h', m % 60, 'min');
	return pair(Math.floor(h / 24), 'd', h % 24, 'h');
}

const GO_UNITS: Record<string, number> = {
	ns: 1e-9,
	us: 1e-6,
	µs: 1e-6,
	μs: 1e-6,
	ms: 1e-3,
	s: 1,
	m: 60,
	h: 3600
};

/**
 * Seconds of a Go duration string ("17h9m0s", "1m30.5s", "250ms", "-5s");
 * null when it is not one.
 */
export function parseGoDuration(value: string | null | undefined): number | null {
	const v = value?.trim();
	if (!v) return null;
	if (v === '0') return 0;
	const m = v.match(/^([+-]?)((?:\d+(?:\.\d*)?|\.\d+)(?:ns|us|µs|μs|ms|s|m|h))+$/);
	if (!m) return null;
	let total = 0;
	for (const part of v.matchAll(/(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g))
		total += Number(part[1]) * GO_UNITS[part[2]];
	return m[1] === '-' ? -total : total;
}

/**
 * A Go duration string in formatDuration's style: "17h9m0s" → "17 h 9 min",
 * "3m20s" → "3 min 20 s". Sub-second values read as "0 s"; anything that
 * is not a Go duration is returned unchanged.
 */
export function formatGoDuration(value: string | null | undefined): string {
	const secs = parseGoDuration(value);
	if (secs === null) return value?.trim() || '—';
	return formatDuration(secs);
}

/**
 * A live uptime, compact and with seconds below a day so it visibly ticks:
 * "42s", "5m 03s", "3h 12m 08s", "4d 3h 12m". Negative values (clock skew)
 * read as "0s".
 */
export function formatUptime(seconds: number): string {
	const s = Math.max(0, Math.floor(Number.isFinite(seconds) ? seconds : 0));
	const pad = (n: number) => String(n).padStart(2, '0');
	if (s < 60) return `${s}s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m}m ${pad(s % 60)}s`;
	const h = Math.floor(m / 60);
	if (h < 24) return `${h}h ${pad(m % 60)}m ${pad(s % 60)}s`;
	return `${Math.floor(h / 24)}d ${h % 24}h ${m % 60}m`;
}

/** Seconds from an ISO start time to `nowMs`; null when absent or invalid. */
export function secondsSince(iso: string | null | undefined, nowMs: number): number | null {
	if (!iso) return null;
	const t = Date.parse(iso);
	return Number.isFinite(t) ? (nowMs - t) / 1000 : null;
}

const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

/** "2 days ago", "in 5 minutes", "just now". */
export function formatRelative(iso: string | Date, now: Date = new Date()): string {
	const t = typeof iso === 'string' ? new Date(iso) : iso;
	const diff = (t.getTime() - now.getTime()) / 1000;
	const abs = Math.abs(diff);
	if (abs < 45) return 'just now';
	const table: [number, Intl.RelativeTimeFormatUnit][] = [
		[60, 'second'],
		[3600, 'minute'],
		[86400, 'hour'],
		[86400 * 7, 'day'],
		[86400 * 30, 'week'],
		[86400 * 365, 'month']
	];
	let unit: Intl.RelativeTimeFormatUnit = 'year';
	let div = 86400 * 365;
	for (let i = 0; i < table.length; i++) {
		const d = i === 0 ? 1 : table[i - 1][0];
		// Pick the unit by the rounded value, so 59.6 s reads "1 minute ago"
		// rather than "60 seconds ago".
		if (Math.round(abs / d) * d < table[i][0]) {
			unit = table[i][1];
			div = d;
			break;
		}
	}
	return rtf.format(Math.round(diff / div), unit);
}

const dateTimeFormats = new Map<string, Intl.DateTimeFormat>();

/**
 * Absolute date and time, one format everywhere: "Sep 25, 2026, 03:00"
 * (24 h), in the viewer's zone or the given one. Absent or invalid values
 * read as "—". Relative times ("5 minutes ago") carry this as their
 * tooltip: `title={formatDateTime(iso)}`.
 */
export function formatDateTime(iso: string | Date | null | undefined, timeZone?: string): string {
	if (iso === null || iso === undefined || iso === '') return '—';
	const t = typeof iso === 'string' ? new Date(iso) : iso;
	if (!Number.isFinite(t.getTime())) return '—';
	const key = timeZone ?? '';
	let f = dateTimeFormats.get(key);
	if (!f) {
		try {
			f = new Intl.DateTimeFormat('en', {
				year: 'numeric',
				month: 'short',
				day: 'numeric',
				hour: '2-digit',
				minute: '2-digit',
				hourCycle: 'h23',
				timeZone
			});
		} catch {
			// An unknown zone: the viewer's own.
			return formatDateTime(t);
		}
		dateTimeFormats.set(key, f);
	}
	return f.format(t);
}

/** A digest or ID shortened for display (full value in a title/copy). */
export function shortId(id: string, n = 12): string {
	const s = id.replace(/^sha256:/, '');
	return s.length > n ? s.slice(0, n) : s;
}
