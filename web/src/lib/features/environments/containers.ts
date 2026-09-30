// Per-container charts of an environment (#5): the series of every
// container from GET …/metrics/containers/history as MultiSeriesChart
// items, drawn like Beszel: on each chart the containers are ranked by
// their total over the range; the ranking is the stacking order (the
// largest on top) and picks the colours (the largest the first). And the
// name filter. Pure.
import type { ContainerMetricsHistory } from '$lib/api/queries';
import { rankColor } from '$lib/design/hue';
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

/**
 * Buckets per chart, about Beszel's (1 min records over an hour): the
 * trend without every 10 s spike, and a small answer per container.
 */
export const CONTAINER_CHART_POINTS = 60;

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
	for (const c of sorted) {
		const name = c.container;
		// Ranked and coloured per chart once every container is in (byRank).
		const color = '';
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
	}
	return {
		cpu: byRank(out.cpu),
		memory: byRank(out.memory),
		network: byRank(out.network),
		disk: byRank(out.disk)
	};
}

/** The sum of the values over the range (gaps count as nothing). */
export function totalOf(values: Values): number {
	let sum = 0;
	for (const v of values) if (v !== null) sum += v;
	return sum;
}

/**
 * The items of one chart in Beszel's order: ranked by their total over the
 * range, largest first (ties by name), item i of n coloured rankColor(i,
 * n). MultiSeriesChart stacks them in this order, the first on top.
 */
export function byRank(items: SeriesItem[]): SeriesItem[] {
	const ranked = items
		.map((it) => ({ it, total: totalOf(it.values) }))
		.sort((a, b) => b.total - a.total || a.it.name.localeCompare(b.it.name));
	return ranked.map(({ it }, rank) => ({ ...it, color: rankColor(rank, ranked.length) }));
}

/**
 * The containers a filter shows: names containing any of its words
 * (separated by spaces, like Beszel's), ignoring case; all of them for an
 * empty filter.
 */
export function nameFilter(text: string): (name: string) => boolean {
	const words = text.toLowerCase().split(/\s+/).filter(Boolean);
	if (!words.length) return () => true;
	return (name) => {
		const n = name.toLowerCase();
		return words.some((w) => n.includes(w));
	};
}
