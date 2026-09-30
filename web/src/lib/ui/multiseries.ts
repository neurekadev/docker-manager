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

/** A bucket's time as the tooltip and the phone details show it. */
export function tipTimeText(time: number): string {
	return tipTime.format(time);
}

export function escapeHtml(s: string): string {
	return s.replace(
		/[&<>"']/g,
		(c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] ?? c
	);
}

/**
 * The tooltip HTML of one bucket: its time and the shown items' total, then
 * one row per item (colour, name, value right-aligned, its parts), in
 * columns of ROWS_PER_COLUMN read top to bottom. Names are escaped.
 */
export function tooltipHtml(
	time: number,
	rows: readonly TooltipRow[],
	unit: ValueUnit,
	total?: number | null
): string {
	const when = escapeHtml(tipTime.format(time));
	const sum =
		total !== undefined && total !== null && rows.length > 1
			? `<span>Total <b style="font-weight:600;font-variant-numeric:tabular-nums">${escapeHtml(
					formatValue(total, unit)
				)}</b></span>`
			: '';
	const head =
		'<div style="display:flex;justify-content:space-between;gap:24px;margin-bottom:6px">' +
		`<span style="opacity:.75">${when}</span>${sum}</div>`;
	if (!rows.length) return `${head}<div style="opacity:.75">No samples</div>`;
	const cols = Math.min(4, Math.ceil(rows.length / ROWS_PER_COLUMN));
	const perCol = Math.ceil(rows.length / cols);
	const withParts = rows.some((r) => r.parts.length);
	const cell = (r: TooltipRow) =>
		`<span style="width:8px;height:8px;border-radius:50%;background:${r.color}"></span>` +
		`<span style="min-width:0;max-width:220px;overflow:hidden;text-overflow:ellipsis">${escapeHtml(r.name)}</span>` +
		`<b style="font-weight:600;font-variant-numeric:tabular-nums;text-align:right">${escapeHtml(
			formatValue(r.value, unit)
		)}</b>` +
		(withParts
			? `<span style="opacity:.65;font-variant-numeric:tabular-nums">${escapeHtml(
					r.parts.map((p) => `${formatValue(p.value, unit)} ${p.label}`).join(', ')
				)}</span>`
			: '');
	// Each column is a grid of its own, so values line up at the right.
	const columns: string[] = [];
	for (let c = 0; c < cols; c++) {
		const part = rows.slice(c * perCol, (c + 1) * perCol);
		columns.push(
			`<div style="display:grid;grid-template-columns:8px auto auto${withParts ? ' auto' : ''};` +
				`align-items:center;column-gap:8px;row-gap:2px;white-space:nowrap">${part.map(cell).join('')}</div>`
		);
	}
	return `${head}<div style="display:flex;align-items:flex-start;gap:24px">${columns.join('')}</div>`;
}

/** The index of the bucket nearest to a time (-1 without buckets). */
export function nearestIndex(times: readonly number[], t: number): number {
	let best = -1;
	let dist = Infinity;
	times.forEach((x, i) => {
		const d = Math.abs(x - t);
		if (d < dist) {
			dist = d;
			best = i;
		}
	});
	return best;
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
