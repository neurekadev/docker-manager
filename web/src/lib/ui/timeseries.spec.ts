import { describe, expect, it } from 'vitest';
import { timeSeriesOption } from '$lib/lazy';
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

	it('formats values by metric unit', () => {
		expect(formatValue(12.44, 'percent')).toBe('12.4%');
		expect(formatValue(1932735283, 'bytes')).toBe('1.8 GB');
		expect(formatValue(2048, 'bytes_per_second')).toBe('2 KB/s');
		expect(formatValue(0.5, 'load')).toBe('0.50');
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
