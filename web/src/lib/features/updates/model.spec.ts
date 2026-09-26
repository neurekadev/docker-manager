import { describe, expect, it } from 'vitest';
import {
	candidateStatus,
	daysText,
	isClock,
	reasonLabel,
	recoveryText,
	runnable,
	summarizeTargets,
	summaryState,
	summaryText,
	windowText,
	type UpdateCandidate,
	type UpdatePolicy
} from './model';

const cand = (c: Partial<UpdateCandidate>): UpdateCandidate => ({
	id: 'c',
	service: 'web',
	reference: 'ghcr.io/silo/web:latest',
	eligible: true,
	nonVersionTag: false,
	status: 'unchecked',
	...c
});

const policy = (summary?: UpdatePolicy['summary']): UpdatePolicy =>
	({
		id: 'p',
		name: 'Silo',
		environmentId: 'e1',
		target: { type: 'stack', id: 's1' },
		view: 'full',
		actions: [],
		summary
	}) as UpdatePolicy;

describe('update candidates', () => {
	it('presents every status with a tone and a sentence-case label', () => {
		expect(candidateStatus('update_available')).toEqual({
			tone: 'warn',
			label: 'Update available'
		});
		expect(candidateStatus('quarantined').tone).toBe('danger');
		expect(candidateStatus('up_to_date').label).toBe('Up to date');
		expect(candidateStatus('check_failed').label).toBe('Check failed');
	});

	it('explains ineligibility in the words of the definition', () => {
		expect(reasonLabel(cand({ reason: 'digest_pinned' }))).toBe('Pinned by @sha256 digest');
		expect(reasonLabel(cand({ reason: 'build_only' }))).toMatch(/Built from source/);
		expect(reasonLabel(cand({}))).toBeNull();
	});

	it('runs only eligible candidates with an update available', () => {
		const list = [
			cand({ id: 'a', status: 'update_available' }),
			cand({ id: 'b', status: 'update_available', eligible: false }),
			cand({ id: 'c', status: 'quarantined' }),
			cand({ id: 'd', status: 'up_to_date' })
		];
		expect(runnable(list).map((c) => c.id)).toEqual(['a']);
	});

	it('guides manual recovery by pinning the previous digest in the user’s own source', () => {
		const text = recoveryText(
			cand({
				repository: 'silo/api',
				reference: 'ghcr.io/silo/api:2.4',
				previousDigest: 'sha256:abc'
			})
		);
		expect(text).toContain('silo/api@sha256:abc');
		expect(text).toContain('never edits your files');
		expect(recoveryText(cand({ guidance: 'Server says so.' }))).toBe('Server says so.');
	});
});

describe('policy summary', () => {
	it('puts quarantine and failures before available updates', () => {
		const base = {
			available: 2,
			failed: 0,
			ineligible: 0,
			quarantined: 0,
			unchecked: 0,
			upToDate: 1
		};
		expect(summaryText(policy({ ...base, quarantined: 1 }))).toEqual({
			text: '1 quarantined',
			tone: 'danger'
		});
		expect(summaryText(policy({ ...base, failed: 3 })).text).toBe('3 failed');
		expect(summaryText(policy(base))).toEqual({ text: '2 updates available', tone: 'warn' });
		expect(summaryText(policy({ ...base, available: 1 })).text).toBe('1 update available');
		expect(
			summaryText(policy({ ...base, available: 0, lastCheckAt: '2026-09-25T00:00:00Z' }))
		).toEqual({
			text: 'Up to date',
			tone: 'ok'
		});
		expect(summaryText(policy(undefined)).text).toBe('Not checked yet');
	});
});

describe('update window', () => {
	it('collapses consecutive days and spans midnight as given', () => {
		expect(daysText([1, 2, 3, 4, 5])).toBe('Mon–Fri');
		expect(daysText([0, 6])).toBe('Sun, Sat');
		expect(daysText([1, 2, 4])).toBe('Mon, Tue, Thu');
		expect(windowText({ days: [1, 2, 3, 4, 5], start: '01:00', end: '05:00' })).toBe(
			'Mon–Fri, 01:00–05:00'
		);
		expect(windowText({ start: '23:00', end: '02:00' })).toBe('Every day, 23:00–02:00');
		expect(windowText(undefined)).toBe('Any time');
	});

	it('accepts 24-hour clock times only', () => {
		expect(isClock('00:00')).toBe(true);
		expect(isClock('23:59')).toBe(true);
		expect(isClock('24:00')).toBe(false);
		expect(isClock('7:00')).toBe(false);
	});
});

describe('target summaries', () => {
	const sum = (s: Partial<Parameters<typeof summaryState>[0] & object>) => ({
		available: 0,
		failed: 0,
		ineligible: 0,
		quarantined: 0,
		unchecked: 0,
		upToDate: 0,
		...s
	});
	const at = '2026-09-25T12:00:00Z';

	it('ranks exclusions, failures, updates, unchecked and up to date', () => {
		expect(summaryState(sum({ available: 2 }), true).label).toBe('Excluded');
		expect(
			summaryState(sum({ failed: 1, quarantined: 1, available: 3, lastCheckAt: at }))
		).toEqual({
			tone: 'danger',
			label: '2 failures'
		});
		expect(summaryState(sum({ available: 1, lastCheckAt: at }))).toEqual({
			tone: 'warn',
			label: '1 update available'
		});
		expect(summaryState(sum({})).label).toBe('Not checked yet');
		expect(summaryState(undefined).label).toBe('Not checked yet');
		expect(summaryState(sum({ upToDate: 2, lastCheckAt: at })).tone).toBe('ok');
	});

	it('counts targets by state and keeps the newest check', () => {
		const out = summarizeTargets([
			sum({ available: 1, lastCheckAt: '2026-09-24T00:00:00Z' }),
			sum({ failed: 1, lastCheckAt: at }),
			sum({ upToDate: 3, lastCheckAt: '2026-09-20T00:00:00Z' }),
			sum({}),
			undefined
		]);
		expect(out).toEqual({
			withUpdates: 1,
			failing: 1,
			upToDate: 1,
			unchecked: 2,
			lastCheckAt: at
		});
	});
});
