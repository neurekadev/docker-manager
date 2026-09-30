// Per-container charts of an environment (#5): the series of every
// container from GET …/metrics/containers/history as MultiSeriesChart
// items, one colour per container (by name order, the same on every chart),
// and the name filter. Pure.
import type { ContainerMetricsHistory } from '$lib/api/queries';
import { seriesColor } from '$lib/design/hue';
import type { SeriesItem } from '$lib/ui/multiseries';

/** The metric keys the charts read. */
export const CONTAINER_CHART_SERIES = [
	'cpu.percent',
	'memory.used_bytes',
	'network.rx_bytes_per_second',
	'network.tx_bytes_per_second',
	'block.read_bytes_per_second',
	'block.write_bytes_per_second'
];

/** Buckets per chart: enough detail without a large answer per container. */
export const CONTAINER_CHART_POINTS = 120;

/** The bucket width asked for a range (the manager rounds it up to its storage). */
export function containerChartStep(seconds: number): number {
	return Math.max(10, Math.ceil(seconds / CONTAINER_CHART_POINTS));
}

export interface ContainerCharts {
	cpu: SeriesItem[];
	memory: SeriesItem[];
	network: SeriesItem[];
	disk: SeriesItem[];
}

type Values = (number | null)[];

function valuesOf(c: ContainerMetricsHistory['items'][number], key: string, n: number): Values {
	const s = c.series.find((x) => x.key === key);
	return s ? s.values.map((v) => v ?? null) : new Array<null>(n).fill(null);
}

/** a + b per bucket; a gap only where both are gaps. */
export function sumValues(a: Values, b: Values): Values {
	return a.map((v, i) => {
		const w = b[i] ?? null;
		return v === null && w === null ? null : (v ?? 0) + (w ?? 0);
	});
}

/**
 * The items of the four charts. Network and disk I/O draw the sum of both
 * directions and name them in the tooltip ("in"/"out", "read"/"write").
 */
export function containerCharts(h: ContainerMetricsHistory | undefined): ContainerCharts {
	const out: ContainerCharts = { cpu: [], memory: [], network: [], disk: [] };
	if (!h) return out;
	const n = h.timestamps.length;
	const sorted = [...h.items].sort((a, b) => a.container.localeCompare(b.container));
	sorted.forEach((c, i) => {
		const name = c.container;
		const color = seriesColor(i);
		const v = (key: string) => valuesOf(c, key, n);
		const rx = v('network.rx_bytes_per_second');
		const tx = v('network.tx_bytes_per_second');
		const read = v('block.read_bytes_per_second');
		const write = v('block.write_bytes_per_second');
		out.cpu.push({ name, color, values: v('cpu.percent') });
		out.memory.push({ name, color, values: v('memory.used_bytes') });
		out.network.push({
			name,
			color,
			values: sumValues(rx, tx),
			parts: [
				{ label: 'in', values: rx },
				{ label: 'out', values: tx }
			]
		});
		out.disk.push({
			name,
			color,
			values: sumValues(read, write),
			parts: [
				{ label: 'read', values: read },
				{ label: 'write', values: write }
			]
		});
	});
	return out;
}

/**
 * The containers a filter shows: names containing the text, ignoring case
 * (all of them for an empty filter).
 */
export function nameFilter(text: string): (name: string) => boolean {
	const q = text.trim().toLowerCase();
	return q ? (name) => name.toLowerCase().includes(q) : () => true;
}
