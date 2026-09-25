import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import type { TimeSeriesOptions } from '$lib/lazy';
import TimeSeriesChart from './TimeSeriesChart.svelte';
import Sparkline from './Sparkline.svelte';

const lazy = vi.hoisted(() => ({
	mounted: [] as TimeSeriesOptions[],
	updates: [] as TimeSeriesOptions[],
	spark: [] as (number | null)[][],
	resolve: null as null | (() => void)
}));

vi.mock('$lib/lazy', async (orig) => ({
	...(await orig<typeof import('$lib/lazy')>()),
	mountTimeSeries: vi.fn(async (_el: HTMLElement, o: TimeSeriesOptions) => {
		lazy.mounted.push(o);
		return {
			update: (u: TimeSeriesOptions) => lazy.updates.push(u),
			resize() {},
			destroy() {}
		};
	}),
	// The sparkline's library "loads" only when the test says so.
	mountSparkline: vi.fn(
		(_el: HTMLElement, v: (number | null)[]) =>
			new Promise((resolve) => {
				lazy.spark.push(v);
				lazy.resolve = () =>
					resolve({ update: (u: (number | null)[]) => lazy.spark.push(u), destroy() {} });
			})
	)
}));

const ts = [
	'2026-09-25T12:00:00Z',
	'2026-09-25T12:01:00Z',
	'2026-09-25T12:02:00Z',
	'2026-09-25T12:03:00Z'
];

describe('TimeSeriesChart (#5 charts)', () => {
	it('names the chart, shows the latest value and lists gaps as text', async () => {
		render(TimeSeriesChart, {
			props: {
				title: 'CPU',
				unit: 'percent',
				timestamps: ts,
				to: '2026-09-25T12:05:00Z',
				yMax: 100,
				lines: [{ name: 'CPU', values: [10, 12.5, null, null] }],
				now: Date.parse('2026-09-25T12:05:00Z')
			}
		});
		const fig = screen.getByRole('figure', { name: 'CPU' });
		expect(fig).toHaveTextContent('12.5%');
		const gaps = within(fig).getByRole('list', { name: 'CPU: time without samples' });
		expect(gaps).toHaveTextContent(/No samples since \d\d:\d\d/);
		expect(fig).toHaveTextContent('CPU: latest 12.5%. 1 gap without samples.');
		await waitFor(() => expect(lazy.mounted).toHaveLength(1));
		const o = lazy.mounted[0];
		expect(o.from).toBe(Date.parse(ts[0]));
		expect(o.to).toBe(Date.parse('2026-09-25T12:05:00Z'));
		expect(o.gaps).toEqual([{ from: Date.parse(ts[2]), to: o.to, open: true }]);
		expect(o.lines[0].values).toEqual([10, 12.5, null, null]);
		expect(o.format(50)).toBe('50%');
	});

	it('has a text legend with each line’s latest value when there are several lines', () => {
		render(TimeSeriesChart, {
			props: {
				title: 'Network',
				unit: 'bytes_per_second',
				timestamps: ts,
				lines: [
					{ name: 'Received', values: [1024, 2048, 2048, 4096], color: '#4ff98b' },
					{ name: 'Sent', values: [512, 512, 1024, null], color: '#707ffc' }
				]
			}
		});
		const legend = within(screen.getByRole('figure', { name: 'Network' })).getAllByRole(
			'list'
		)[0];
		expect(legend).toHaveTextContent('Received 4 KB/s');
		expect(legend).toHaveTextContent('Sent 1 KB/s');
	});

	it('says so when a range has no samples at all', () => {
		render(TimeSeriesChart, {
			props: {
				title: 'Load',
				unit: 'load',
				timestamps: ts,
				lines: [{ name: 'Load', values: [null, null, null, null] }]
			}
		});
		expect(screen.getByRole('figure', { name: 'Load' })).toHaveTextContent(
			'Load: no samples in this range.'
		);
	});
});

describe('Sparkline', () => {
	it('draws values that arrived while ECharts was still loading', async () => {
		const { rerender } = render(Sparkline, { props: { values: [], label: 'CPU trend' } });
		await rerender({ values: [1, 2, null, 3], label: 'CPU trend' });
		lazy.resolve?.();
		await waitFor(() => expect(lazy.spark.at(-1)).toEqual([1, 2, null, 3]));
		expect(screen.getByText('CPU trend')).toBeInTheDocument();
	});
});
