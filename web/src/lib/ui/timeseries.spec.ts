import { describe, expect, it } from 'vitest';
import { besidePointer, timeSeriesOption } from '$lib/lazy';
import { formatTimeRange, formatValue, gapIntervals, latestValue, valueExtent } from './timeseries';

const t = (m: number) => Date.UTC(2026, 8, 25, 12, m);

describe('time-series gaps (#5: offline intervals are visible)', () => {
	it('finds inner runs of missing samples and a trailing open one', () => {
		const ts = [0, 1, 2, 3, 4, 5, 6, 7].map(t);
		const v = [1, null, null, 2, 3, null, null, null];
		expect(gapIntervals(ts, v, t(8))).toEqual([
			{ from: t(1), to: t(3) },
			{ from: t(5), to: t(8), open: true }
		]);
	});

	it('ignores a single late bucket and treats undefined as missing', () => {
		const ts = [0, 1, 2, 3].map(t);
		expect(gapIntervals(ts, [1, null, 2, 3], t(4))).toEqual([]);
		expect(gapIntervals(ts, [1, undefined, undefined, 3], t(4))).toEqual([
			{ from: t(1), to: t(3) }
		]);
		expect(gapIntervals(ts, [1, null, 2, 3], t(4), 1)).toEqual([{ from: t(1), to: t(2) }]);
	});

	it('reports a series without any sample as one open gap', () => {
		expect(gapIntervals([0, 1].map(t), [null, null], t(2))).toEqual([
			{ from: t(0), to: t(2), open: true }
		]);
	});

	it('never turns a gap into zero: latest and extent skip nulls', () => {
		expect(latestValue([1, 2, null, null])).toBe(2);
		expect(latestValue([null])).toBeNull();
		expect(valueExtent([null, 3, 0, 5, null])).toEqual({ min: 0, max: 5 });
		expect(valueExtent([null])).toBeNull();
	});

	it('formats values by metric unit with up to two decimals', () => {
		expect(formatValue(12.44, 'percent')).toBe('12.44%');
		expect(formatValue(12.446, 'percent')).toBe('12.45%');
		expect(formatValue(100, 'percent')).toBe('100%');
		expect(formatValue(1932735283, 'bytes')).toBe('1.8 GB');
		expect(formatValue(123.456 * 1024 ** 3, 'bytes')).toBe('123.46 GB');
		expect(formatValue(2048, 'bytes_per_second')).toBe('2 KB/s');
		expect(formatValue(1.25 * 1024 ** 2, 'bytes_per_second')).toBe('1.25 MB/s');
		expect(formatValue(0.5, 'load')).toBe('0.5');
		expect(formatValue(1.234, 'load')).toBe('1.23');
		expect(formatValue(2, 'load')).toBe('2');
		expect(formatValue(3.6, 'count')).toBe('4');
		expect(formatValue(48.5, 'celsius')).toBe('48.5 °C');
		expect(formatValue(38.854, 'celsius')).toBe('38.85 °C');
		expect(formatValue(61, 'celsius')).toBe('61 °C');
		expect(formatValue(null, 'percent')).toBe('—');
	});

	it('words gap ranges, open ones as "since"', () => {
		// Midday local time: the first two ranges stay within one day.
		const now = new Date(2026, 8, 25, 12, 0).getTime();
		const from = now - 30 * 60_000;
		expect(formatTimeRange({ from, to: now, open: true }, now)).toMatch(/^since \d\d:\d\d$/);
		expect(formatTimeRange({ from, to: from + 60_000 }, now)).toMatch(/^\d\d:\d\d–\d\d:\d\d$/);
		expect(formatTimeRange({ from: from - 3 * 86_400_000, to: from }, now)).toMatch(
			/[A-Z][a-z]{2} \d/
		);
	});
});

describe('timeSeriesOption (the ECharts option of TimeSeriesChart)', () => {
	const opts = {
		timestamps: [t(0), t(1), t(2)],
		lines: [
			{ name: 'CPU', values: [1, null, 3], color: '#2bb0f6', area: true },
			{ name: 'Other', values: [2, 2, null] }
		],
		format: (v: number) => `${v}%`,
		from: t(0),
		to: t(5),
		yMin: 0,
		yMax: 100,
		gaps: [{ from: t(3), to: t(5) }]
	};

	it('keeps nulls as breaks, spans the whole range and shades gaps on the first line', () => {
		const o = timeSeriesOption(opts);
		expect(o.xAxis).toMatchObject({ type: 'time', min: t(0), max: t(5) });
		expect(o.yAxis).toMatchObject({ min: 0, max: 100, interval: 25 });
		expect(o.series.every((s) => s.connectNulls === false)).toBe(true);
		expect(o.series[0].data).toEqual([
			[t(0), 1],
			[t(1), null],
			[t(2), 3]
		]);
		expect(o.series[0].markArea?.data).toEqual([[{ xAxis: t(3) }, { xAxis: t(5) }]]);
		expect(o.series[1].markArea).toBeUndefined();
		expect(o.series[0].areaStyle).toBeTruthy();
		expect(o.series[1].areaStyle).toBeUndefined();
	});

	it('formats tooltip values and says "No sample" for gaps', () => {
		const o = timeSeriesOption({ ...opts, gaps: [] });
		expect(o.tooltip.valueFormatter(12)).toBe('12%');
		expect(o.tooltip.valueFormatter(null)).toBe('No sample');
		expect(o.series[0].markArea).toBeUndefined();
		expect(timeSeriesOption({ ...opts, yMax: undefined }).yAxis.interval).toBeUndefined();
	});

	it('draws a dashed line only where asked, keeping its colour and width', () => {
		const o = timeSeriesOption({
			...opts,
			lines: [
				opts.lines[0],
				{ name: 'Before compression', values: [4, 4, 4], color: '#b4c4f2', dashed: true }
			]
		});
		expect(o.series[0].lineStyle).toEqual({ color: '#2bb0f6', width: 1.75 });
		expect(o.series[1].lineStyle).toEqual({ color: '#b4c4f2', width: 1.75, type: 'dashed' });
	});
});

describe('timeSeriesOption for many items (MultiSeriesChart)', () => {
	const base = {
		timestamps: [t(0), t(1)],
		format: (v: number) => `${v}%`,
		from: t(0),
		to: t(1),
		stacked: true,
		lines: [
			{ name: 'web', values: [1, 2], color: 'hsl(1, 80%, 66%)' },
			{ name: 'db', values: [3, null], color: 'hsl(2, 80%, 56%)', muted: true }
		]
	};

	it('stacks smoothed filled areas like Beszel and greys muted lines out behind the others', () => {
		const o = timeSeriesOption(base);
		expect(o.series.every((s) => s.stack === 'total')).toBe(true);
		expect(o.series[0]).toMatchObject({
			z: 2,
			smooth: true,
			smoothMonotone: 'x',
			symbol: 'circle',
			lineStyle: { color: 'hsl(1, 80%, 66%)', width: 1 },
			areaStyle: { color: 'hsl(1, 80%, 66%)', opacity: 0.4 }
		});
		expect(o.series[1]).toMatchObject({ z: 1, symbol: 'none' });
		expect(o.series[1].lineStyle.color).not.toBe('hsl(2, 80%, 56%)');
		expect(o.series[1].lineStyle.opacity).toBeLessThan(1);
		expect(o.series[1].areaStyle?.opacity).toBeLessThan(0.4);
		const flat = timeSeriesOption({ ...base, stacked: false }).series[0];
		expect(flat.stack).toBeUndefined();
		expect(flat.smooth).toBeUndefined();
	});

	it('renders the caller’s tooltip of the hovered bucket, outside the card, beside the pointer', () => {
		const place = () => [1, 2];
		const o = timeSeriesOption({ ...base, tooltip: (i) => `bucket ${i}` }, place);
		const tip = o.tooltip as unknown as {
			formatter: (p: unknown) => string;
			appendTo: string;
			position: (...a: unknown[]) => number[];
		};
		expect(tip.formatter([{ dataIndex: 1 }, { dataIndex: 1 }])).toBe('bucket 1');
		expect(tip.formatter([])).toBe('');
		expect(tip.appendTo).toBe('body');
		expect(tip.position([0, 0], [], null, null, { contentSize: [10, 10] })).toEqual([1, 2]);
		expect('formatter' in timeSeriesOption(base).tooltip).toBe(false);
	});

	it('keeps only the pointer line when the caller shows the details itself (phones)', () => {
		const o = timeSeriesOption({ ...base, tooltip: (i) => `bucket ${i}`, hideTooltip: true });
		expect(o.tooltip).toMatchObject({ trigger: 'axis', showContent: false });
		expect('formatter' in o.tooltip).toBe(false);
	});
});

describe('besidePointer (placing a long tooltip)', () => {
	const view = { width: 1000, height: 800 };

	it('goes right of the pointer, vertically centred on it', () => {
		expect(besidePointer([100, 100], [200, 100], { left: 50, top: 300 }, view)).toEqual([
			116, 50
		]);
	});

	it('flips left of the pointer at the right edge of the window', () => {
		expect(besidePointer([700, 100], [200, 100], { left: 200, top: 300 }, view)).toEqual([
			484, 50
		]);
	});

	it('stays inside the window vertically and starts at its top when taller', () => {
		// Near the bottom: moved up to end 8 px above the window's edge.
		expect(besidePointer([100, 150], [200, 300], { left: 0, top: 600 }, view)[1]).toBe(-108);
		// Near the top: moved down to start 8 px below it.
		expect(besidePointer([100, 10], [200, 300], { left: 0, top: 20 }, view)[1]).toBe(-12);
		// Taller than the window: its top edge.
		expect(besidePointer([100, 100], [200, 2000], { left: 0, top: 300 }, view)[1]).toBe(-292);
	});
});
