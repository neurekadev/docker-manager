import { describe, expect, it } from 'vitest';
import { ApiRequestError } from '$lib/api/client';
import { errorView, fieldError } from './errors';
import { formatBytes, formatDuration, formatPercent, formatRelative, shortId } from './format';
import { statusInfo } from './status';
import {
	compareValues,
	nextSort,
	selectionState,
	sortRows,
	toggleAll,
	virtualWindow,
	type Column
} from './table';

interface Row {
	name: string;
	cpu: number | null;
}

const cols: Column<Row>[] = [
	{ id: 'name', header: 'Name', sortValue: (r) => r.name },
	{ id: 'cpu', header: 'CPU', sortValue: (r) => r.cpu },
	{ id: 'plain', header: 'Plain' }
];

describe('table helpers', () => {
	const rows: Row[] = [
		{ name: 'silo-web', cpu: 2.4 },
		{ name: 'silo-api', cpu: null },
		{ name: 'silo-db-10', cpu: 1.2 },
		{ name: 'silo-db-9', cpu: 12 }
	];

	it('sorts naturally, stably, with empty values last in both directions', () => {
		expect(
			sortRows(rows, cols, { column: 'name', direction: 'asc' }).map((r) => r.name)
		).toEqual(['silo-api', 'silo-db-9', 'silo-db-10', 'silo-web']);
		expect(sortRows(rows, cols, { column: 'cpu', direction: 'asc' }).map((r) => r.cpu)).toEqual(
			[1.2, 2.4, 12, null]
		);
		expect(
			sortRows(rows, cols, { column: 'cpu', direction: 'desc' }).map((r) => r.cpu)
		).toEqual([12, 2.4, 1.2, null]);
		// Unsortable column or no sort: input order, as a copy.
		const same = sortRows(rows, cols, { column: 'plain', direction: 'asc' });
		expect(same).toEqual(rows);
		expect(same).not.toBe(rows);
		expect(compareValues('', 'a', 'desc')).toBe(1);
	});

	it('cycles sort direction per column', () => {
		expect(nextSort(null, 'name')).toEqual({ column: 'name', direction: 'asc' });
		expect(nextSort({ column: 'name', direction: 'asc' }, 'name')).toEqual({
			column: 'name',
			direction: 'desc'
		});
		expect(nextSort({ column: 'name', direction: 'desc' }, 'name')).toEqual({
			column: 'name',
			direction: 'asc'
		});
		expect(nextSort({ column: 'name', direction: 'desc' }, 'cpu')).toEqual({
			column: 'cpu',
			direction: 'asc'
		});
	});

	it('computes header selection and toggles all visible rows only', () => {
		expect(selectionState([], [])).toBe(false);
		expect(selectionState(['a', 'b'], [])).toBe(false);
		expect(selectionState(['a', 'b'], ['a'])).toBe('mixed');
		expect(selectionState(['a', 'b'], ['b', 'a', 'z'])).toBe(true);
		expect(toggleAll(['a', 'b'], ['z', 'a'])).toEqual(['z', 'a', 'b']);
		expect(toggleAll(['a', 'b'], ['z', 'a', 'b'])).toEqual(['z']);
	});

	it('windows long lists with spacers and overscan', () => {
		expect(virtualWindow(0, 480, 48, 0)).toEqual({ start: 0, end: 0, padTop: 0, padBottom: 0 });
		const top = virtualWindow(0, 480, 48, 1000, 8);
		expect(top).toEqual({ start: 0, end: 26, padTop: 0, padBottom: 974 * 48 });
		const mid = virtualWindow(48 * 500, 480, 48, 1000, 8);
		expect(mid.start).toBe(492);
		expect(mid.end).toBe(518);
		expect(mid.padTop + (mid.end - mid.start) * 48 + mid.padBottom).toBe(1000 * 48);
		const end = virtualWindow(48 * 995, 480, 48, 1000, 8);
		expect(end.end).toBe(1000);
		expect(end.padBottom).toBe(0);
	});
});

describe('formatting', () => {
	it('formats bytes, percentages and durations like the mockup', () => {
		expect(formatBytes(312 * 2 ** 20)).toBe('312 MB');
		expect(formatBytes(1.8 * 2 ** 30)).toBe('1.8 GB');
		expect(formatBytes(8 * 2 ** 30)).toBe('8 GB');
		expect(formatBytes(512)).toBe('512 B');
		expect(formatBytes(null)).toBe('—');
		expect(formatPercent(12.4)).toBe('12.4%');
		expect(formatPercent(0.7)).toBe('0.7%');
		expect(formatPercent(45)).toBe('45%');
		expect(formatPercent(undefined)).toBe('—');
		expect(formatDuration(45)).toBe('45 s');
		expect(formatDuration(3 * 3600)).toBe('3 hours');
		expect(formatDuration(14 * 86400)).toBe('14 days');
		expect(shortId('sha256:a1b2c3d4e5f6a7b8c9d0')).toBe('a1b2c3d4e5f6');
	});

	it('formats relative times against an injected now', () => {
		const now = new Date('2026-09-25T12:00:00Z');
		expect(formatRelative('2026-09-25T11:59:40Z', now)).toBe('just now');
		expect(formatRelative('2026-09-25T11:55:00Z', now)).toBe('5 minutes ago');
		expect(formatRelative('2026-09-23T12:00:00Z', now)).toBe('2 days ago');
		expect(formatRelative('2026-09-11T12:00:00Z', now)).toBe('2 weeks ago');
		expect(formatRelative('2026-09-25T13:00:00Z', now)).toBe('in 1 hour');
	});
});

describe('status vocabulary', () => {
	it('maps API states to tone and plain text', () => {
		expect(statusInfo('running')).toEqual({ tone: 'ok', label: 'Running', pulse: false });
		expect(statusInfo('offline').tone).toBe('offline');
		expect(statusInfo('restarting').pulse).toBe(true);
		expect(statusInfo('partial').label).toBe('Partially running');
		expect(statusInfo('partial', 'job').label).toBe('Partly failed');
		expect(statusInfo('running', 'job').pulse).toBe(true);
		expect(statusInfo('some_new_state')).toEqual({
			tone: 'neutral',
			label: 'Some new state',
			pulse: false
		});
	});
});

describe('error views', () => {
	it('shows the API error shape: message, code, request ID, fields', () => {
		const e = new ApiRequestError('x', 422, {
			code: 'validation_failed',
			message: 'invalid schedule',
			requestId: 'req-1',
			retryable: false,
			details: [{ field: 'body.cron', message: 'expected 5 fields' }]
		});
		const v = errorView(e);
		expect(v).toMatchObject({
			message: 'Invalid schedule.',
			code: 'validation_failed',
			requestId: 'req-1',
			retryable: false,
			network: false
		});
		expect(fieldError(e, 'body.cron')).toBe('expected 5 fields');
		expect(fieldError(e, 'body.name')).toBeUndefined();
	});

	it('explains network failures without an HTTP status', () => {
		const v = errorView(new ApiRequestError('Failed to fetch', null));
		expect(v.network).toBe(true);
		expect(v.retryable).toBe(true);
		expect(v.message).toMatch(/could not reach the manager/);
		expect(errorView(new Error('boom')).message).toBe('Boom.');
		expect(errorView(new ApiRequestError('x', 503)).retryable).toBe(true);
	});
});
