// Time-series helpers (#5 charts, TimeSeriesChart). Metric responses carry
// one value per bucket with null where no sample exists (the agent was
// offline or the value unknown); nulls are gaps, never zero. Pure functions.
import { formatBytes, formatNumber, formatPercent, formatTemperature } from './format';

export type ValueUnit = 'percent' | 'bytes' | 'bytes_per_second' | 'load' | 'count' | 'celsius';

/**
 * Formats a value of a metric unit (#5 units) with up to two decimals,
 * trailing zeros dropped (#147): 12.34%, 1.5 GB, 2 KB/s, 0.5, 48.5 °C;
 * counts whole.
 */
export function formatValue(v: number | null | undefined, unit: ValueUnit): string {
	if (v === null || v === undefined || !Number.isFinite(v)) return '—';
	switch (unit) {
		case 'percent':
			return formatPercent(v);
		case 'bytes':
			return formatBytes(v);
		case 'bytes_per_second':
			return `${formatBytes(v)}/s`;
		case 'load':
			return formatNumber(v);
		case 'celsius':
			return formatTemperature(v);
		default:
			return String(Math.round(v));
	}
}

/** One line of a TimeSeriesChart; values align with its timestamps. */
export interface ChartLine {
	name: string;
	values: (number | null)[];
	color?: string;
	area?: boolean;
	/** A dashed line, also in the legend (a reference line next to a solid one). */
	dashed?: boolean;
}

export interface TimeRange {
	from: number;
	to: number;
	/** The gap lasts until the end of the range (still no samples). */
	open?: boolean;
}

/**
 * Runs of missing samples as time ranges. A run starts at its first null
 * bucket and ends where the next sample starts, or at `end` (the range end,
 * e.g. now) when the series ends without samples. Runs shorter than
 * `minBuckets` (a single late sample) are ignored.
 */
export function gapIntervals(
	timestamps: readonly number[],
	values: readonly (number | null | undefined)[],
	end: number,
	minBuckets = 2
): TimeRange[] {
	const out: TimeRange[] = [];
	let start = -1;
	for (let i = 0; i <= timestamps.length; i++) {
		const missing = i < timestamps.length && (values[i] === null || values[i] === undefined);
		if (missing) {
			if (start < 0) start = i;
			continue;
		}
		if (start >= 0) {
			if (i - start >= minBuckets)
				out.push(
					i < timestamps.length
						? { from: timestamps[start], to: timestamps[i] }
						: { from: timestamps[start], to: end, open: true }
				);
			start = -1;
		}
	}
	return out;
}

/** The newest non-null value, or null. */
export function latestValue(values: readonly (number | null | undefined)[]): number | null {
	for (let i = values.length - 1; i >= 0; i--) {
		const v = values[i];
		if (v !== null && v !== undefined) return v;
	}
	return null;
}

/** Minimum and maximum of the non-null values (null when there are none). */
export function valueExtent(
	values: readonly (number | null | undefined)[]
): { min: number; max: number } | null {
	let min = Infinity;
	let max = -Infinity;
	for (const v of values) {
		if (v === null || v === undefined) continue;
		if (v < min) min = v;
		if (v > max) max = v;
	}
	return min === Infinity ? null : { min, max };
}

const timeFmt = new Intl.DateTimeFormat('en', {
	hour: '2-digit',
	minute: '2-digit',
	hourCycle: 'h23'
});
const dayFmt = new Intl.DateTimeFormat('en', {
	month: 'short',
	day: 'numeric',
	hour: '2-digit',
	minute: '2-digit',
	hourCycle: 'h23'
});

/** "12:05–12:40" or "since 12:40"; with dates unless both are today. */
export function formatTimeRange(r: TimeRange, now: number): string {
	const sameDay = (a: number, b: number) =>
		new Date(a).toDateString() === new Date(b).toDateString();
	const fmt = sameDay(r.from, now) && sameDay(r.to, now) ? timeFmt : dayFmt;
	return r.open ? `since ${fmt.format(r.from)}` : `${fmt.format(r.from)}–${fmt.format(r.to)}`;
}
