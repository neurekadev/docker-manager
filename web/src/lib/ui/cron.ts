// Cron expressions in words and the presets of CronField (#13). Pure: the
// manager's parser stays the one authority on what an expression means
// (POST /schedules/previews); this only reads the common five-field shapes
// ("Daily at 03:00") and falls back to the raw expression for the rest.

const DAY_NAMES = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const DAY_ALIASES: Record<string, number> = {
	sun: 0,
	mon: 1,
	tue: 2,
	wed: 3,
	thu: 4,
	fri: 5,
	sat: 6
};
const UTC_ZONES = new Set([
	'utc',
	'etc/utc',
	'gmt',
	'etc/gmt',
	'universal',
	'etc/universal',
	'zulu',
	'etc/zulu',
	'uct',
	'etc/uct',
	'etc/gmt+0',
	'etc/gmt-0',
	'etc/gmt0',
	'gmt0'
]);

/** The viewer's IANA zone (UTC when unknown). */
function viewerZone(): string {
	try {
		return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
	} catch {
		return 'UTC';
	}
}

function sameZone(a: string, b: string): boolean {
	const x = a.trim().toLowerCase();
	const y = b.trim().toLowerCase();
	return x === y || (UTC_ZONES.has(x) && UTC_ZONES.has(y));
}

/** "UTC", "Europe/Berlin", "America/New York". */
function zoneWords(zone: string): string {
	return UTC_ZONES.has(zone.trim().toLowerCase()) ? 'UTC' : zone.trim().replaceAll('_', ' ');
}

/** A whole number within [min, max], or null. */
function int(s: string, min: number, max: number): number | null {
	if (!/^\d+$/.test(s)) return null;
	const n = Number(s);
	return n >= min && n <= max ? n : null;
}

/** A list of numbers and ranges ("1,15", "1-5", "mon,wed"), or null. */
function list(
	s: string,
	min: number,
	max: number,
	aliases: Record<string, number> = {}
): number[] | null {
	const out: number[] = [];
	for (const part of s.split(',')) {
		const [a, b, ...rest] = part.split('-');
		if (rest.length) return null;
		const lo = aliases[a.toLowerCase()] ?? int(a, min, max);
		const hi = b === undefined ? lo : (aliases[b.toLowerCase()] ?? int(b, min, max));
		if (lo === null || hi === null || hi < lo) return null;
		for (let i = lo; i <= hi; i++) out.push(i);
	}
	return [...new Set(out)].sort((x, y) => x - y);
}

/** "*" or "*\/N": the step, or null. */
function every(s: string): number | null {
	if (s === '*') return 1;
	const m = s.match(/^\*\/(\d+)$/);
	return m ? Number(m[1]) || null : null;
}

const pad = (n: number) => String(n).padStart(2, '0');
const clock = (h: number, m: number) => `${pad(h)}:${pad(m)}`;

/** "Monday", "Monday and Thursday", "Monday, Wednesday and Friday". */
function joinWords(words: string[]): string {
	if (words.length <= 1) return words.join('');
	return `${words.slice(0, -1).join(', ')} and ${words[words.length - 1]}`;
}

const DESCRIPTORS: Record<string, string> = {
	'@yearly': '0 0 1 1 *',
	'@annually': '0 0 1 1 *',
	'@monthly': '0 0 1 * *',
	'@weekly': '0 0 * * 0',
	'@daily': '0 0 * * *',
	'@midnight': '0 0 * * *',
	'@hourly': '0 * * * *'
};

/** The words of an expression without its zone, or null for other shapes. */
function describeFields(expr: string): string | null {
	const norm = DESCRIPTORS[expr.trim().toLowerCase()] ?? expr.trim();
	const f = norm.split(/\s+/);
	if (f.length !== 5) return null;
	const [min, hour, dom, month, dow] = f;
	if (month !== '*') return null;

	// Every N minutes: "*/15 * * * *".
	const minStep = every(min);
	if (minStep !== null && hour === '*' && dom === '*' && dow === '*') {
		return minStep === 1 ? 'Every minute' : `Every ${minStep} minutes`;
	}

	const minute = int(min, 0, 59);
	if (minute === null) return null;

	// Hourly or every N hours at :MM.
	const hourStep = every(hour);
	if (hourStep !== null && dom === '*' && dow === '*') {
		if (hourStep === 1) return minute === 0 ? 'Every hour' : `Hourly at :${pad(minute)}`;
		return minute === 0
			? `Every ${hourStep} hours`
			: `Every ${hourStep} hours at :${pad(minute)}`;
	}

	const hours = list(hour, 0, 23);
	if (!hours) return null;
	const at = joinWords(hours.map((h) => clock(h, minute)));

	if (dom === '*' && dow === '*') return `Daily at ${at}`;

	if (dom === '*') {
		const days = list(dow, 0, 7, DAY_ALIASES)?.map((d) => d % 7);
		if (!days) return null;
		const set = [...new Set(days)].sort((a, b) => a - b);
		if (set.length === 7) return `Daily at ${at}`;
		if (set.join(',') === '1,2,3,4,5') return `Weekdays at ${at}`;
		if (set.join(',') === '0,6') return `Weekends at ${at}`;
		// Monday first, Sunday last.
		const order = [...set.filter((d) => d !== 0), ...set.filter((d) => d === 0)];
		return `Weekly on ${joinWords(order.map((d) => DAY_NAMES[d]))} at ${at}`;
	}

	if (dow === '*') {
		const days = list(dom, 1, 31);
		if (!days) return null;
		return days.length === 1
			? `Monthly on day ${days[0]} at ${at}`
			: `Monthly on days ${joinWords(days.map(String))} at ${at}`;
	}
	return null;
}

/**
 * A cron expression in plain words: "Every 15 minutes", "Hourly at :05",
 * "Daily at 03:00", "Weekdays at 07:30", "Weekly on Monday and Thursday at
 * 04:00", "Monthly on day 1 at 02:00". The zone is added in words only
 * when it is not the viewer's ("Daily at 03:00 (UTC)"). Anything else is
 * the expression itself. `viewer` is the viewer's zone (tests).
 */
export function describeCron(expr: string, timeZone?: string, viewer?: string): string {
	const words = describeFields(expr ?? '');
	if (!words) return (expr ?? '').trim();
	if (!timeZone || sameZone(timeZone, viewer ?? viewerZone())) return words;
	// Intervals shorter than a day read the same in every zone.
	if (/^Every (minute|\d+ minutes|hour|\d+ hours)$/.test(words)) return words;
	return `${words} (${zoneWords(timeZone)})`;
}

export type CronPresetKind = 'hourly' | 'daily' | 'weekly' | 'custom';

/** CronField's presets: the shape of an expression it can edit with fields. */
export interface CronPreset {
	kind: CronPresetKind;
	/** 0–59. */
	minute: number;
	/** 0–23 (daily, weekly). */
	hour: number;
	/** 0 (Sunday) – 6 (weekly). */
	day: number;
}

/** The preset an expression matches ("0 3 * * *" → daily 03:00), else custom. */
export function parseCronPreset(expr: string): CronPreset {
	const fallback: CronPreset = { kind: 'custom', minute: 0, hour: 3, day: 1 };
	const f = (expr ?? '').trim().split(/\s+/);
	if (f.length !== 5) return fallback;
	const [min, hour, dom, month, dow] = f;
	const minute = int(min, 0, 59);
	if (minute === null || dom !== '*' || month !== '*') return fallback;
	if (hour === '*' && dow === '*') return { ...fallback, kind: 'hourly', minute };
	const h = int(hour, 0, 23);
	if (h === null) return fallback;
	if (dow === '*') return { ...fallback, kind: 'daily', minute, hour: h };
	const d = DAY_ALIASES[dow.toLowerCase()] ?? int(dow, 0, 7);
	if (d === null) return fallback;
	return { kind: 'weekly', minute, hour: h, day: d % 7 };
}

/** The expression of a preset; custom keeps `custom` (the raw expression). */
export function buildCron(p: CronPreset, custom = ''): string {
	const minute = Math.min(59, Math.max(0, Math.trunc(p.minute) || 0));
	const hour = Math.min(23, Math.max(0, Math.trunc(p.hour) || 0));
	switch (p.kind) {
		case 'hourly':
			return `${minute} * * * *`;
		case 'daily':
			return `${minute} ${hour} * * *`;
		case 'weekly':
			return `${minute} ${hour} * * ${((Math.trunc(p.day) % 7) + 7) % 7}`;
		default:
			return custom;
	}
}

/** Weekday options for pickers, Monday first. */
export const WEEKDAY_OPTIONS = [1, 2, 3, 4, 5, 6, 0].map((d) => ({
	value: String(d),
	label: DAY_NAMES[d]
}));
