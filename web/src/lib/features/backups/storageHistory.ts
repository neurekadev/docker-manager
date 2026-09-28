// Storage over time (#10): what the backup repositories stored, from
// GET /backup-storage/history (a point at the range start, every UTC hour
// or day and now; each the sum of every location's latest measurement,
// null before the first). Pure helpers for StorageHistoryCard.
import type { Schema } from '$lib/api/client';
import { formatBytes, formatDateTime } from '$lib/ui/format';

export type StorageHistory = Schema<'BackupStorageHistory'>;

/** The ranges the card offers; the server picks hourly or daily points. */
export const STORAGE_RANGES = [
	{ id: '7d', label: 'Last 7 days', days: 7 },
	{ id: '30d', label: 'Last 30 days', days: 30 },
	{ id: '90d', label: 'Last 90 days', days: 90 },
	{ id: '1y', label: 'Last year', days: 365 }
] as const;

export type StorageRangeId = (typeof STORAGE_RANGES)[number]['id'];

export const DEFAULT_STORAGE_RANGE: StorageRangeId = '30d';

function range(id: string) {
	return STORAGE_RANGES.find((r) => r.id === id) ?? STORAGE_RANGES[1];
}

/** "Last 30 days" (unknown IDs: the default range). */
export function storageRangeLabel(id: string): string {
	return range(id).label;
}

/** The start of a range ending now, as the API's `from` (ISO). */
export function storageRangeFrom(id: string, now: Date = new Date()): string {
	return new Date(now.getTime() - range(id).days * 86_400_000).toISOString();
}

export interface StorageSeries {
	/** ISO times, ascending, from the first measured point on. */
	timestamps: string[];
	/** Stored at the destinations (after deduplication and compression). */
	stored: number[];
	/**
	 * The same data before compression: stored plus what compression saved
	 * (never below stored, as on the storage card).
	 */
	beforeCompression: number[];
}

/**
 * The chart's lines: points before the first measurement are left out
 * (the axis still spans the range), so the lines start where the history
 * does. Undefined when nothing was measured in or before the range.
 */
export function storageSeries(h: StorageHistory | undefined): StorageSeries | undefined {
	if (!h) return undefined;
	const out: StorageSeries = { timestamps: [], stored: [], beforeCompression: [] };
	h.timestamps.forEach((t, i) => {
		const stored = h.storedBytes[i];
		if (stored === null || stored === undefined) return;
		const uncompressed = h.uncompressedBytes[i] ?? 0;
		out.timestamps.push(t);
		out.stored.push(stored);
		out.beforeCompression.push(Math.max(stored, uncompressed));
	});
	return out.timestamps.length ? out : undefined;
}

/**
 * One sentence for the chart: the latest figure and how it changed in the
 * range ("80 GB stored now, up 2 GB in the last 30 days.").
 */
export function storageSummary(s: StorageSeries, rangeLabel: string): string {
	const last = s.stored[s.stored.length - 1];
	const diff = last - s.stored[0];
	const within = `in the ${rangeLabel.charAt(0).toLowerCase()}${rangeLabel.slice(1)}`;
	const change =
		diff === 0
			? `no change ${within}`
			: `${diff > 0 ? 'up' : 'down'} ${formatBytes(Math.abs(diff))} ${within}`;
	return `${formatBytes(last)} stored now, ${change}.`;
}

export interface StorageRow {
	at: string;
	when: string;
	stored: string;
	beforeCompression: string;
}

/**
 * The figures as table rows, newest first (the chart's text alternative).
 * Only points where a figure changed are listed, plus the latest.
 */
export function storageRows(s: StorageSeries): StorageRow[] {
	const rows: StorageRow[] = [];
	const n = s.timestamps.length;
	for (let i = n - 1; i >= 0; i--) {
		const changed =
			i === 0 ||
			s.stored[i] !== s.stored[i - 1] ||
			s.beforeCompression[i] !== s.beforeCompression[i - 1];
		if (i !== n - 1 && !changed) continue;
		rows.push({
			at: s.timestamps[i],
			when: formatDateTime(s.timestamps[i]),
			stored: formatBytes(s.stored[i]),
			beforeCompression: formatBytes(s.beforeCompression[i])
		});
	}
	return rows;
}
