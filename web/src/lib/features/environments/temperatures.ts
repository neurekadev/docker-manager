// The host's temperature chart (#146): the sensor series of GET …/metrics
// as MultiSeriesChart items, one line per sensor (temperatures do not add
// up, so they are not stacked). Like the per-container charts, every
// sensor gets its own colour in Beszel's order: ranked by its maximum over
// the range, the hottest first with the first colour (rankColor). Sensors
// without a reading in the range are left out, so a host without sensors
// has no items and no chart. Pure.
import type { EnvironmentMetrics } from '$lib/api/client';
import { rankColor } from '$lib/design/hue';
import type { SeriesItem } from '$lib/ui/multiseries';

/** The average of each bucket (the line) and its maximum (the ranking). */
export const TEMPERATURE_KEY = 'temperature.celsius';
export const TEMPERATURE_MAX_KEY = 'temperature.celsius.max';

type Values = (number | null)[];

/** The largest value (null: only gaps). */
export function peakOf(values: Values): number | null {
	let peak: number | null = null;
	for (const v of values) if (v !== null && (peak === null || v > peak)) peak = v;
	return peak;
}

/**
 * Sensors in Beszel's order: those with a reading, ranked by their peak
 * (the maximum over the range; else the largest value of the line),
 * hottest first, ties by name, each coloured rankColor(rank, n).
 */
export function rankByPeak(
	sensors: readonly { name: string; values: Values; peak?: number | null }[]
): SeriesItem[] {
	const ranked = sensors
		.filter((s) => peakOf(s.values) !== null)
		.map((s) => ({ ...s, peak: s.peak ?? peakOf(s.values) ?? 0 }))
		.sort((a, b) => b.peak - a.peak || a.name.localeCompare(b.name));
	return ranked.map(({ name, values }, rank) => ({
		name,
		values,
		color: rankColor(rank, ranked.length)
	}));
}

/** The sensors of a metrics response as chart items (rankByPeak). */
export function temperatureItems(m: EnvironmentMetrics | undefined): SeriesItem[] {
	const lines = new Map<string, Values>();
	const peaks = new Map<string, number>();
	for (const s of m?.series ?? []) {
		if (!s.sensor) continue;
		const values = s.values.map((v) => v ?? null);
		if (s.key === TEMPERATURE_KEY) lines.set(s.sensor, values);
		const peak = peakOf(values);
		if (s.key === TEMPERATURE_MAX_KEY && peak !== null) peaks.set(s.sensor, peak);
	}
	return rankByPeak(
		[...lines].map(([name, values]) => ({ name, values, peak: peaks.get(name) }))
	);
}
