// Multi-series charts (MultiSeriesChart): many items of one type on one
// chart (every container of an environment), each with its own colour.
// The tooltip names every shown item with a value at the hovered bucket,
// largest first; hidden items (left out by a filter) are greyed out on the
// chart and absent from the tooltip. Pure functions.
import { formatValue, type ValueUnit } from './timeseries';

/** One item of a MultiSeriesChart; values align with the timestamps. */
export interface SeriesItem {
	name: string;
	color: string;
	/** null is a gap (no sample), never zero. */
	values: (number | null)[];
	/** Parts of each value named after it in the tooltip (received and sent). */
	parts?: { label: string; values: (number | null)[] }[];
}

export interface TooltipRow {
	name: string;
	color: string;
	value: number;
	parts: { label: string; value: number | null }[];
}

/** The rows of a bucket: shown items with a value there, largest first. */
export function tooltipRows(
	items: readonly SeriesItem[],
	index: number,
	shown: (name: string) => boolean = () => true
): TooltipRow[] {
	const rows: TooltipRow[] = [];
	for (const it of items) {
		const v = it.values[index];
		if (v === null || v === undefined || !shown(it.name)) continue;
		rows.push({
			name: it.name,
			color: it.color,
			value: v,
			parts: (it.parts ?? []).map((p) => ({ label: p.label, value: p.values[index] ?? null }))
		});
	}
	return rows.sort((a, b) => b.value - a.value || a.name.localeCompare(b.name));
}

/** Rows per tooltip column; longer lists wrap into up to four columns. */
export const ROWS_PER_COLUMN = 20;

const tipTime = new Intl.DateTimeFormat('en', {
	month: 'short',
	day: 'numeric',
	hour: '2-digit',
	minute: '2-digit',
	hourCycle: 'h23'
});

export function escapeHtml(s: string): string {
	return s.replace(
		/[&<>"']/g,
		(c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] ?? c
	);
}

/**
 * The tooltip HTML of one bucket: its time, then one row per item (colour,
 * name, value and its parts), in columns of ROWS_PER_COLUMN read top to
 * bottom. Names are escaped.
 */
export function tooltipHtml(time: number, rows: readonly TooltipRow[], unit: ValueUnit): string {
	const head = `<div style="margin-bottom:6px;opacity:.75">${escapeHtml(tipTime.format(time))}</div>`;
	if (!rows.length) return `${head}<div style="opacity:.75">No samples</div>`;
	const cols = Math.min(4, Math.ceil(rows.length / ROWS_PER_COLUMN));
	const perCol = Math.ceil(rows.length / cols);
	const cells = rows
		.map((r) => {
			const parts = r.parts.length
				? `<span style="opacity:.65">${escapeHtml(
						r.parts.map((p) => `${formatValue(p.value, unit)} ${p.label}`).join(', ')
					)}</span>`
				: '';
			return (
				'<div style="display:flex;align-items:center;gap:8px;min-width:0;white-space:nowrap">' +
				`<span style="flex:none;width:8px;height:8px;border-radius:50%;background:${r.color}"></span>` +
				`<span style="flex:1;min-width:0;max-width:240px;overflow:hidden;text-overflow:ellipsis">${escapeHtml(r.name)}</span>` +
				`<b style="font-weight:600;font-variant-numeric:tabular-nums">${escapeHtml(formatValue(r.value, unit))}</b>` +
				parts +
				'</div>'
			);
		})
		.join('');
	return (
		head +
		`<div style="display:grid;grid-auto-flow:column;grid-template-rows:repeat(${perCol},auto);` +
		`column-gap:24px;row-gap:2px">${cells}</div>`
	);
}

/** The index of the newest bucket where any item has a value (-1: none). */
export function lastIndex(items: readonly SeriesItem[]): number {
	let last = -1;
	for (const it of items)
		for (let i = it.values.length - 1; i > last; i--)
			if (it.values[i] !== null && it.values[i] !== undefined) {
				last = i;
				break;
			}
	return last;
}

/** The sum of the shown items' values at a bucket (null: none has one). */
export function totalAt(
	items: readonly SeriesItem[],
	index: number,
	shown: (name: string) => boolean = () => true
): number | null {
	let sum: number | null = null;
	for (const it of items) {
		const v = it.values[index];
		if (v === null || v === undefined || !shown(it.name)) continue;
		sum = (sum ?? 0) + v;
	}
	return sum;
}
