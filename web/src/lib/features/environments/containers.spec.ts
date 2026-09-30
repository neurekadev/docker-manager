import { describe, expect, it } from 'vitest';
import type { ContainerMetricsHistory } from '$lib/api/queries';
import { seriesColor } from '$lib/design/hue';
import { containerChartStep, containerCharts, nameFilter, sumValues } from './containers';

const series = (key: string, values: (number | null)[]) => ({
	key,
	unit: 'bytes_per_second' as const,
	values
});

const history: ContainerMetricsHistory = {
	environmentId: 'env-1',
	from: '2026-09-25T12:00:00Z',
	to: '2026-09-25T12:02:00Z',
	stepSeconds: 60,
	resolution: '1m',
	timestamps: ['2026-09-25T12:00:00Z', '2026-09-25T12:01:00Z'],
	skewCorrected: false,
	incomplete: false,
	online: true,
	containers: [
		{
			container: 'web',
			series: [
				series('cpu.percent', [1, 2]),
				series('network.rx_bytes_per_second', [10, null]),
				series('network.tx_bytes_per_second', [5, null])
			]
		},
		{
			container: 'db',
			series: [
				series('cpu.percent', [3, null]),
				series('block.read_bytes_per_second', [null, 7]),
				series('block.write_bytes_per_second', [1, null])
			]
		}
	]
};

describe('containerCharts (the per-container charts of an environment)', () => {
	it('gives each container one colour by name order, the same on every chart', () => {
		const c = containerCharts(history);
		expect(c.cpu.map((i) => [i.name, i.color])).toEqual([
			['db', seriesColor(0)],
			['web', seriesColor(1)]
		]);
		for (const chart of [c.memory, c.network, c.disk])
			expect(chart.map((i) => i.color)).toEqual([seriesColor(0), seriesColor(1)]);
	});

	it('draws both directions of network and disk I/O as one sum and names them', () => {
		const c = containerCharts(history);
		const web = c.network[1];
		expect(web.values).toEqual([15, null]);
		expect(web.parts).toEqual([
			{ label: 'in', values: [10, null] },
			{ label: 'out', values: [5, null] }
		]);
		expect(c.disk[0].values).toEqual([1, 7]);
		expect(c.disk[0].parts?.map((p) => p.label)).toEqual(['read', 'write']);
	});

	it('keeps a missing series as gaps, never zeros', () => {
		const c = containerCharts(history);
		expect(c.memory[0].values).toEqual([null, null]);
		expect(c.disk[1].values).toEqual([null, null]);
		expect(containerCharts(undefined).cpu).toEqual([]);
	});

	it('sums with gaps only where both are gaps', () => {
		expect(sumValues([1, null, null], [2, 3, null])).toEqual([3, 3, null]);
	});
});

describe('nameFilter', () => {
	it('matches names containing the text, ignoring case and spaces around it', () => {
		const f = nameFilter('  Web ');
		expect(f('shop-web-1')).toBe(true);
		expect(f('db')).toBe(false);
		expect(nameFilter('')('db')).toBe(true);
	});
});

describe('containerChartStep', () => {
	it('asks for about 120 buckets and never below the 10 s samples', () => {
		expect(containerChartStep(3600)).toBe(30);
		expect(containerChartStep(600)).toBe(10);
		expect(containerChartStep(90 * 86400)).toBe(64800);
	});
});
