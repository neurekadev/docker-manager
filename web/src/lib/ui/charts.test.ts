import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/svelte';
import type { TimeSeriesOptions } from '$lib/lazy';
import MultiSeriesChart from './MultiSeriesChart.svelte';
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

describe('MultiSeriesChart (every container of an environment)', () => {
	const items = [
		{ name: 'db', color: 'hsl(1, 80%, 66%)', values: [2, 3, null, null] },
		{ name: 'web', color: 'hsl(2, 80%, 56%)', values: [1, 4, 6, null] },
		{ name: 'cache', color: 'hsl(3, 80%, 76%)', values: [null, 1, 1, null] }
	];

	it('shows the total of the newest bucket and names the largest items as text', async () => {
		const before = lazy.mounted.length;
		render(MultiSeriesChart, {
			props: { title: 'Docker CPU', unit: 'percent', timestamps: ts, items }
		});
		const fig = screen.getByRole('figure', { name: 'Docker CPU' });
		expect(fig).toHaveTextContent('7.0%');
		expect(fig).toHaveTextContent(
			'Docker CPU: latest total 7.0%. Largest: web 6.0%, cache 1.0%.'
		);
		await waitFor(() => expect(lazy.mounted).toHaveLength(before + 1));
		const o = lazy.mounted[before];
		expect(o.stacked).toBe(true);
		// Stacked from the bottom: the last item first, the first on top.
		expect(o.lines.map((l) => [l.name, !!l.muted])).toEqual([
			['cache', false],
			['web', false],
			['db', false]
		]);
		expect(o.tooltip?.(1)).toContain('web');
	});

	it('greys out what the filter leaves out and drops it from the tooltip and total', async () => {
		const before = lazy.mounted.length;
		render(MultiSeriesChart, {
			props: {
				title: 'Docker memory',
				unit: 'count',
				timestamps: ts,
				items,
				shown: (n: string) => n !== 'web'
			}
		});
		expect(screen.getByRole('figure', { name: 'Docker memory' })).toHaveTextContent(
			'Docker memory: latest total 1. Largest: cache 1.'
		);
		await waitFor(() => expect(lazy.mounted).toHaveLength(before + 1));
		const o = lazy.mounted[before];
		// Filtered out items stay in their place, greyed out.
		expect(o.lines.map((l) => [l.name, !!l.muted])).toEqual([
			['cache', false],
			['web', true],
			['db', false]
		]);
		const tip = o.tooltip?.(1) ?? '';
		expect(tip).toContain('db');
		expect(tip).not.toContain('web');
		// db 3 + cache 1 at that bucket; web (4) is left out.
		expect(tip).toMatch(/Total <b[^>]*>4</);
	});

	it('totals the newest bucket of the shown items, not of a hidden newer one', () => {
		render(MultiSeriesChart, {
			props: {
				title: 'Docker network',
				unit: 'count',
				timestamps: ts,
				items: [
					{ name: 'db', color: 'red', values: [2, 5, null, null] },
					{ name: 'web', color: 'blue', values: [1, 1, 1, 9] }
				],
				shown: (n: string) => n === 'db'
			}
		});
		expect(screen.getByRole('figure', { name: 'Docker network' })).toHaveTextContent(
			'Docker network: latest total 5. Largest: db 5.'
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

	it('redraws when the values change after ECharts loaded', async () => {
		const { rerender } = render(Sparkline, { props: { values: [1] } });
		lazy.resolve?.();
		await waitFor(() => expect(lazy.spark.at(-1)).toEqual([1]));
		await rerender({ values: [1, 5] });
		await waitFor(() => expect(lazy.spark.at(-1)).toEqual([1, 5]));
		await rerender({ values: [1, 5, 7] });
		await waitFor(() => expect(lazy.spark.at(-1)).toEqual([1, 5, 7]));
	});

	it('keeps working when the chart library fails to load', async () => {
		const { mountSparkline } = await import('$lib/lazy');
		vi.mocked(mountSparkline).mockImplementationOnce(() =>
			Promise.reject(new Error('chunk failed'))
		);
		const { rerender } = render(Sparkline, { props: { values: [1], label: 'Memory trend' } });
		await rerender({ values: [1, 2], label: 'Memory trend' });
		expect(screen.getByText('Memory trend')).toBeInTheDocument();
	});
});
