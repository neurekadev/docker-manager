import { describe, expect, it } from 'vitest';
import { ApiRequestError } from '$lib/api/client';
import { errorView, fieldError } from './errors';
import {
	formatBytes,
	formatDateTime,
	formatDuration,
	formatGoDuration,
	parseGoDuration,
	formatPercent,
	formatRelative,
	formatUptime,
	secondsSince,
	shortId
} from './format';
import { statusInfo } from './status';
import { placeTooltip } from './tooltip';
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
		expect(formatDuration(1)).toBe('1 s');
		expect(formatDuration(45)).toBe('45 s');
		expect(formatDuration(200)).toBe('3 min 20 s');
		expect(formatDuration(180)).toBe('3 min');
		expect(formatDuration(3 * 3600)).toBe('3 h');
		expect(formatDuration(17 * 3600 + 9 * 60 + 5)).toBe('17 h 9 min');
		expect(formatDuration(14 * 86400)).toBe('14 d');
		expect(formatDuration(3 * 86400 + 4 * 3600 + 59)).toBe('3 d 4 h');
		expect(formatDuration(-3)).toBe('0 s');
		expect(formatDuration(Number.NaN)).toBe('0 s');
		expect(shortId('sha256:a1b2c3d4e5f6a7b8c9d0')).toBe('a1b2c3d4e5f6');
	});

	it('reads Go duration strings in the same style', () => {
		expect(parseGoDuration('17h9m0s')).toBe(17 * 3600 + 9 * 60);
		expect(parseGoDuration('1m30.5s')).toBe(90.5);
		expect(parseGoDuration('250ms')).toBe(0.25);
		expect(parseGoDuration('1h0m0.000001s')).toBeCloseTo(3600.000001);
		expect(parseGoDuration('-5s')).toBe(-5);
		expect(parseGoDuration('0')).toBe(0);
		expect(parseGoDuration('5 minutes')).toBeNull();
		expect(parseGoDuration('')).toBeNull();
		expect(parseGoDuration(undefined)).toBeNull();
		expect(formatGoDuration('17h9m0s')).toBe('17 h 9 min');
		expect(formatGoDuration('3m20s')).toBe('3 min 20 s');
		expect(formatGoDuration('1s')).toBe('1 s');
		expect(formatGoDuration('72h')).toBe('3 d');
		expect(formatGoDuration('soon')).toBe('soon');
		expect(formatGoDuration(null)).toBe('—');
	});

	it('formats absolute dates one way, 24 h, in a zone', () => {
		expect(formatDateTime('2026-09-27T16:54:00Z', 'UTC')).toBe('Sep 27, 2026, 16:54');
		expect(formatDateTime('2026-09-25T01:00:00Z', 'Europe/Berlin')).toBe('Sep 25, 2026, 03:00');
		expect(formatDateTime(new Date('2026-01-02T00:05:00Z'), 'UTC')).toBe('Jan 2, 2026, 00:05');
		expect(formatDateTime('not a date', 'UTC')).toBe('—');
		expect(formatDateTime(undefined)).toBe('—');
		expect(formatDateTime('2026-09-27T16:54:00Z', 'Nowhere/Invalid')).toMatch(/2026/);
	});

	it('formats live uptimes compactly, with seconds below a day', () => {
		expect(formatUptime(0)).toBe('0s');
		expect(formatUptime(42.9)).toBe('42s');
		expect(formatUptime(5 * 60 + 3)).toBe('5m 03s');
		expect(formatUptime(3 * 3600 + 12 * 60 + 8)).toBe('3h 12m 08s');
		expect(formatUptime(23 * 3600 + 59 * 60 + 59)).toBe('23h 59m 59s');
		expect(formatUptime(4 * 86400 + 3 * 3600 + 12 * 60 + 8)).toBe('4d 3h 12m');
		expect(formatUptime(-5)).toBe('0s');
		expect(formatUptime(Number.NaN)).toBe('0s');
	});

	it('measures seconds since an ISO time, null when unknown', () => {
		const now = Date.parse('2026-09-25T12:00:00Z');
		expect(secondsSince('2026-09-25T11:59:30Z', now)).toBe(30);
		expect(secondsSince(undefined, now)).toBeNull();
		expect(secondsSince('not a date', now)).toBeNull();
	});

	it('formats relative times against an injected now', () => {
		const now = new Date('2026-09-25T12:00:00Z');
		expect(formatRelative('2026-09-25T11:59:40Z', now)).toBe('just now');
		expect(formatRelative('2026-09-25T11:55:00Z', now)).toBe('5 minutes ago');
		expect(formatRelative('2026-09-23T12:00:00Z', now)).toBe('2 days ago');
		expect(formatRelative('2026-09-11T12:00:00Z', now)).toBe('2 weeks ago');
		// Rounding never produces "60 seconds" or "60 minutes".
		expect(formatRelative('2026-09-25T11:59:00.400Z', now)).toBe('1 minute ago');
		expect(formatRelative('2026-09-25T11:00:10Z', now)).toBe('1 hour ago');
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

	it('words the manager move refusal the same everywhere', () => {
		const e = new ApiRequestError('moving', 409, {
			code: 'manager_moved',
			message:
				'Docker Manager is moving (or moved) to a new server: this manager is read-only.',
			details: [],
			requestId: 'req-2',
			retryable: false
		});
		expect(errorView(e)).toMatchObject({ code: 'manager_moved', status: 409 });
		expect(errorView(e).message).toBe(
			'This Docker Manager is moving, or moved, to a new server, so nothing can be changed here. Use Docker Manager on the new server.'
		);
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

describe('tooltip placement', () => {
	const viewport = { width: 1000, height: 800 };
	const size = { width: 100, height: 30 };

	it('centres the tooltip above its anchor', () => {
		expect(
			placeTooltip({ top: 200, left: 400, width: 40, height: 20 }, size, viewport)
		).toEqual({ top: 164, left: 370, side: 'top' });
	});

	it('flips below an anchor near the top and keeps it inside the viewport', () => {
		expect(placeTooltip({ top: 10, left: 2, width: 20, height: 20 }, size, viewport)).toEqual({
			top: 36,
			left: 8,
			side: 'bottom'
		});
		expect(
			placeTooltip({ top: 400, left: 990, width: 10, height: 20 }, size, viewport).left
		).toBe(892);
	});
});
