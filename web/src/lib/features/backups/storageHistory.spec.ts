import { describe, expect, it } from 'vitest';
import {
	DEFAULT_STORAGE_RANGE,
	STORAGE_RANGES,
	storageRangeFrom,
	storageRangeLabel,
	storageRows,
	storageSeries,
	storageSummary,
	type StorageHistory
} from './storageHistory';

const GB = 1024 ** 3;

function history(stored: (number | null)[], uncompressed: (number | null)[]): StorageHistory {
	return {
		from: '2026-09-01T12:00:00Z',
		to: '2026-09-05T12:00:00Z',
		stepSeconds: 86400,
		timestamps: stored.map((_, i) => new Date(Date.UTC(2026, 8, 1 + i)).toISOString()),
		storedBytes: stored,
		uncompressedBytes: uncompressed
	};
}

describe('storage ranges (#10)', () => {
	it('offers 7 days to a year, the last 30 days by default', () => {
		expect(STORAGE_RANGES.map((r) => r.label)).toEqual([
			'Last 7 Days',
			'Last 30 Days',
			'Last 90 Days',
			'Last Year'
		]);
		expect(DEFAULT_STORAGE_RANGE).toBe('30d');
		expect(storageRangeLabel('90d')).toBe('Last 90 Days');
		expect(storageRangeLabel('bogus')).toBe('Last 30 Days');
	});

	it('starts a range the chosen number of days before now', () => {
		const now = new Date('2026-09-28T12:00:00Z');
		expect(storageRangeFrom('7d', now)).toBe('2026-09-21T12:00:00.000Z');
		expect(storageRangeFrom('30d', now)).toBe('2026-08-29T12:00:00.000Z');
		expect(storageRangeFrom('1y', now)).toBe('2025-09-28T12:00:00.000Z');
	});
});

describe('storageSeries (#10)', () => {
	it('starts where the history does and never puts "before compression" below "stored"', () => {
		const s = storageSeries(
			history([null, null, 2 * GB, 3 * GB, 3 * GB], [null, null, 5 * GB, 7 * GB, 0])
		);
		expect(s).toEqual({
			timestamps: [
				'2026-09-03T00:00:00.000Z',
				'2026-09-04T00:00:00.000Z',
				'2026-09-05T00:00:00.000Z'
			],
			stored: [2 * GB, 3 * GB, 3 * GB],
			// Never below what is stored (as on the storage card).
			beforeCompression: [5 * GB, 7 * GB, 3 * GB]
		});
	});

	it('is empty before anything was measured', () => {
		expect(storageSeries(undefined)).toBeUndefined();
		expect(storageSeries(history([null, null], [null, null]))).toBeUndefined();
		expect(storageSeries(history([], []))).toBeUndefined();
	});
});

describe('storageSummary (#10)', () => {
	const series = (stored: number[]) => ({
		timestamps: stored.map((_, i) => `2026-09-0${i + 1}T00:00:00Z`),
		stored,
		beforeCompression: stored
	});

	it('states the latest figure and how it changed in the range', () => {
		expect(storageSummary(series([2 * GB, 3.5 * GB]), 'Last 30 Days')).toBe(
			'3.5 GB stored now, up 1.5 GB in the last 30 days.'
		);
		expect(storageSummary(series([3 * GB, 2 * GB]), 'Last Year')).toBe(
			'2 GB stored now, down 1 GB in the last year.'
		);
		expect(storageSummary(series([GB]), 'Last 7 Days')).toBe(
			'1 GB stored now, no change in the last 7 days.'
		);
	});
});

describe('storageRows (#10)', () => {
	it('lists the changes and the latest point, newest first', () => {
		const rows = storageRows({
			timestamps: [
				'2026-09-01T00:00:00Z',
				'2026-09-02T00:00:00Z',
				'2026-09-03T00:00:00Z',
				'2026-09-04T00:00:00Z'
			],
			stored: [GB, GB, 2 * GB, 2 * GB],
			beforeCompression: [3 * GB, 3 * GB, 3 * GB, 4 * GB]
		});
		expect(rows.map((r) => [r.at, r.stored, r.beforeCompression])).toEqual([
			['2026-09-04T00:00:00Z', '2 GB', '4 GB'],
			['2026-09-03T00:00:00Z', '2 GB', '3 GB'],
			['2026-09-01T00:00:00Z', '1 GB', '3 GB']
		]);
		expect(rows[0].when).toMatch(/2026/);
	});

	it('keeps the latest point when nothing changed', () => {
		const rows = storageRows({
			timestamps: ['2026-09-01T00:00:00Z', '2026-09-02T00:00:00Z', '2026-09-03T00:00:00Z'],
			stored: [GB, GB, GB],
			beforeCompression: [GB, GB, GB]
		});
		expect(rows.map((r) => r.at)).toEqual(['2026-09-03T00:00:00Z', '2026-09-01T00:00:00Z']);
	});
});
