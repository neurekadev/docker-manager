// Number, size and time formatting (#22 copy rules: exact numbers, units
// the API defines in #5: CPU % of the environment's cores, bytes as
// binary units). Pure functions; `now` is injectable for tests.

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

/** Decimal places every measured value keeps (#147). */
const DECIMALS = 2;

/** v rounded half away from zero to DECIMALS places. */
function round(v: number): number {
	const f = 10 ** DECIMALS;
	// toPrecision cleans the float noise of the scaling (1.005 * 100 =
	// 100.49999…), so a value on the half as written rounds up.
	const r = Math.round(Number((Math.abs(v) * f).toPrecision(15))) / f;
	return v < 0 ? -r : r;
}

/**
 * A measured number with up to two decimal places, trailing zeros
 * dropped: 2, 1.5, 1.25, 0.07, -12.35. The one rule for every measured
 * value (sizes, rates, percentages, load, ratios, CPUs, seconds shown as
 * a decimal); counts stay whole numbers. "—" when absent or not finite.
 */
export function formatNumber(v: number | null | undefined): string {
	if (v === null || v === undefined || !Number.isFinite(v)) return '—';
	const r = round(v);
	// No "-0" for a small negative value that rounds to zero.
	return r === 0 ? '0' : String(r);
}

/**
 * Bytes in binary units with the mockup's short labels and up to two
 * decimals: 1.8 GB, 1.25 GB, 312.45 MB, 2 GB; whole bytes below 1 KB
 * (512 B).
 */
export function formatBytes(bytes: number | null | undefined): string {
	if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) return '—';
	let v = Math.abs(bytes);
	let u = 0;
	while (v >= 1024 && u < UNITS.length - 1) {
		v /= 1024;
		u++;
	}
	// 1023.999 KB rounds to 1024 KB: read it as 1 MB.
	if (u > 0 && u < UNITS.length - 1 && round(v) >= 1024) {
		v /= 1024;
		u++;
	}
	const s = u === 0 ? String(Math.round(v)) : formatNumber(v);
	return `${bytes < 0 && s !== '0' ? '-' : ''}${s} ${UNITS[u]}`;
}

/** A percentage with up to two decimals: 12.34%, 0.07%, 45.6%, 100%. */
export function formatPercent(v: number | null | undefined): string {
	if (v === null || v === undefined || !Number.isFinite(v)) return '—';
	return `${formatNumber(v)}%`;
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

/**
 * A long span of hours (a disk's power-on time) in its two largest units,
 * years and days included: "3 y 41 d", "41 d 5 h", "5 h". A year is 365
 * days; a zero second unit is left out ("2 y"). Invalid or negative
 * values read as "0 h".
 */
export function formatHours(hours: number): string {
	const h = Math.max(0, Math.floor(Number.isFinite(hours) ? hours : 0));
	const pair = (big: number, bigUnit: string, small: number, smallUnit: string) =>
		small ? `${big} ${bigUnit} ${small} ${smallUnit}` : `${big} ${bigUnit}`;
	if (h < 24) return `${h} h`;
	const d = Math.floor(h / 24);
	if (d < 365) return pair(d, 'd', h % 24, 'h');
	return pair(Math.floor(d / 365), 'y', d % 365, 'd');
}

/** A temperature in degrees Celsius, up to two decimals: "38 °C", "41.5 °C". */
export function formatTemperature(celsius: number | null | undefined): string {
	if (celsius === null || celsius === undefined || !Number.isFinite(celsius)) return '—';
	return `${formatNumber(celsius)} °C`;
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

/** Words a Title Case label keeps lowercase unless first or last (#219). */
const MINOR_WORDS = new Set([
	'a',
	'an',
	'the',
	'and',
	'but',
	'or',
	'nor',
	'as',
	'at',
	'by',
	'for',
	'in',
	'of',
	'on',
	'per',
	'to',
	'via',
	'with'
]);

/**
 * Words joined into a label in Title Case (#219): "request ID" → "Request
 * ID", "sign-in policy" → "Sign-In Policy". Only plain lowercase words
 * change; acronyms, file names and values ("compose.yaml", ".env") stay.
 */
export function titleCase(s: string): string {
	const words = s.split(' ');
	return words
		.map((w, i) => {
			if (!/^[a-z][a-z-]*$/.test(w)) return w;
			if (i > 0 && i < words.length - 1 && MINOR_WORDS.has(w)) return w;
			return w
				.split('-')
				.map((p) => (p ? p[0].toUpperCase() + p.slice(1) : p))
				.join('-');
		})
		.join(' ');
}
