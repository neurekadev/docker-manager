import { describe, expect, it } from 'vitest';
import {
	ROWS_PER_COLUMN,
	escapeHtml,
	lastIndex,
	tooltipHtml,
	tooltipRows,
	totalAt,
	type SeriesItem
} from './multiseries';

const items: SeriesItem[] = [
	{ name: 'web', color: 'hsl(1, 80%, 66%)', values: [1, 5, null] },
	{ name: 'db', color: 'hsl(2, 80%, 56%)', values: [3, 5, null] },
	{ name: 'cache', color: 'hsl(3, 80%, 76%)', values: [null, 2, null] }
];

describe('tooltipRows (the tooltip of MultiSeriesChart)', () => {
	it('lists every item with a value, largest first, ties by name', () => {
		expect(tooltipRows(items, 1).map((r) => [r.name, r.value])).toEqual([
			['db', 5],
			['web', 5],
			['cache', 2]
		]);
	});

	it('leaves out gaps and the items a filter hides', () => {
		expect(tooltipRows(items, 0).map((r) => r.name)).toEqual(['db', 'web']);
		expect(tooltipRows(items, 1, (n) => n !== 'db').map((r) => r.name)).toEqual([
			'web',
			'cache'
		]);
		expect(tooltipRows(items, 2)).toEqual([]);
	});

	it('carries the parts of each value at the bucket', () => {
		const net: SeriesItem = {
			name: 'web',
			color: 'red',
			values: [30],
			parts: [
				{ label: 'in', values: [20] },
				{ label: 'out', values: [null] }
			]
		};
		expect(tooltipRows([net], 0)[0].parts).toEqual([
			{ label: 'in', value: 20 },
			{ label: 'out', value: null }
		]);
	});
});

describe('tooltipHtml', () => {
	const at = Date.parse('2026-09-25T12:05:00Z');

	it('shows each row in its colour with its formatted value and parts', () => {
		const html = tooltipHtml(
			at,
			[
				{
					name: 'web',
					color: 'hsl(1, 80%, 66%)',
					value: 2048,
					parts: [{ label: 'in', value: 1024 }]
				}
			],
			'bytes_per_second'
		);
		expect(html).toContain('background:hsl(1, 80%, 66%)');
		expect(html).toContain('>web<');
		expect(html).toContain('2 KB/s');
		expect(html).toContain('1 KB/s in');
	});

	it('escapes names', () => {
		const html = tooltipHtml(
			at,
			[{ name: '<b>x</b>', color: 'red', value: 1, parts: [] }],
			'count'
		);
		expect(html).not.toContain('<b>x</b>');
		expect(html).toContain('&lt;b&gt;x&lt;/b&gt;');
		expect(escapeHtml(`a&"'`)).toBe('a&amp;&quot;&#39;');
	});

	it('wraps long lists into columns read top to bottom', () => {
		const rows = Array.from({ length: ROWS_PER_COLUMN * 2 + 1 }, (_, i) => ({
			name: `c${i}`,
			color: 'red',
			value: i,
			parts: []
		}));
		expect(tooltipHtml(at, rows.slice(0, 3), 'count')).toContain(
			'grid-template-rows:repeat(3,auto)'
		);
		expect(tooltipHtml(at, rows, 'count')).toContain(
			`grid-template-rows:repeat(${Math.ceil(rows.length / 3)},auto)`
		);
	});

	it('says so when no item has a value there', () => {
		expect(tooltipHtml(at, [], 'percent')).toContain('No samples');
	});
});

describe('lastIndex and totalAt (the headline of MultiSeriesChart)', () => {
	it('finds the newest bucket any item has a value in', () => {
		expect(lastIndex(items)).toBe(1);
		expect(lastIndex([{ name: 'x', color: 'red', values: [null, null] }])).toBe(-1);
		expect(lastIndex([])).toBe(-1);
	});

	it('sums the shown items and keeps a gap as null', () => {
		expect(totalAt(items, 1)).toBe(12);
		expect(totalAt(items, 1, (n) => n === 'cache')).toBe(2);
		expect(totalAt(items, 2)).toBeNull();
	});
});
