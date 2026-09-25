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

/** A duration in its largest sensible unit: "14 days", "3 hours", "45 s". */
export function formatDuration(seconds: number): string {
	const s = Math.max(0, Math.round(seconds));
	if (s < 60) return `${s} s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m} min`;
	const h = Math.floor(m / 60);
	if (h < 48) return `${h} ${h === 1 ? 'hour' : 'hours'}`;
	const d = Math.floor(h / 24);
	return `${d} ${d === 1 ? 'day' : 'days'}`;
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

/** Absolute date and time, e.g. "Sep 25, 2026, 03:00" (24 h), in a zone. */
export function formatDateTime(iso: string | Date, timeZone?: string): string {
	const t = typeof iso === 'string' ? new Date(iso) : iso;
	return new Intl.DateTimeFormat('en', {
		dateStyle: 'medium',
		timeStyle: 'short',
		hourCycle: 'h23',
		timeZone
	}).format(t);
}

/** A digest or ID shortened for display (full value in a title/copy). */
export function shortId(id: string, n = 12): string {
	const s = id.replace(/^sha256:/, '');
	return s.length > n ? s.slice(0, n) : s;
}
