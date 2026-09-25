import { describe, expect, it } from 'vitest';
import type { Schedule } from '$lib/api/client';
import { dstLabel, formatRunTime, policyHref, runReason, runStatus, scheduleState } from './model';

type Run = Schedule['recentRuns'][number];
const run = (r: Partial<Run>): Run =>
	({
		jobs: [],
		outcome: 'enqueued',
		result: '',
		scheduledFor: '2026-09-20T00:30:00Z',
		...r
	}) as Run;

describe('schedules model (#13)', () => {
	it('shows run times in the policy zone, not the viewer’s', () => {
		// 00:30 UTC on Sep 27 is 02:30 in Berlin (CEST) and 20:30 the day before in New York.
		expect(formatRunTime('2026-09-27T00:30:00Z', 'Europe/Berlin')).toBe('Sun, Sep 27, 02:30');
		expect(formatRunTime('2026-09-27T00:30:00Z', 'America/New_York')).toBe(
			'Sat, Sep 26, 20:30'
		);
		expect(formatRunTime('2026-09-27T00:30:00Z', 'Not/AZone')).toBe('2026-09-27T00:30:00Z');
	});

	it('annotates DST gaps and repeats', () => {
		expect(dstLabel({ dst: 'repeated' })).toBe('Clocks move back');
		expect(dstLabel({ dst: 'gap' })).toBe('Clocks move forward');
		expect(dstLabel({ dst: 'none' })).toBeNull();
	});

	it('shows a run by its job result once known, otherwise by its outcome and reason', () => {
		expect(runStatus(run({ outcome: 'enqueued', result: 'partial' }))).toEqual({
			status: 'partial',
			label: '',
			kind: 'job'
		});
		expect(runStatus(run({ outcome: 'enqueued', result: 'enqueued' }))).toMatchObject({
			label: 'Started'
		});
		expect(runStatus(run({ outcome: 'missed' }))).toMatchObject({ label: 'Missed' });
		expect(runReason(run({ outcome: 'missed', errorClass: 'missed', missedCount: 3 }))).toBe(
			'DockYard was not running at 3 scheduled times.'
		);
		expect(runReason(run({ outcome: 'skipped', errorClass: 'previous_run_active' }))).toBe(
			'The previous run was still active.'
		);
		expect(
			runReason(run({ outcome: 'rejected', reason: 'The environment is archived.' }))
		).toBe('The environment is archived.');
		expect(runReason(run({ outcome: 'failed', errorClass: 'target_not_found' }))).toBe(
			'target not found'
		);
	});

	it('links each kind to where its policies are edited and words the state', () => {
		expect(policyHref('prune')).toBe('/maintenance');
		expect(policyHref('update_run')).toBe('/updates');
		expect(policyHref('backup_verification')).toBe('/backups');
		expect(scheduleState({ enabled: true })).toEqual({ status: 'running', label: 'Enabled' });
		expect(scheduleState({ enabled: false })).toEqual({ status: 'stopped', label: 'Disabled' });
		expect(scheduleState({ enabled: true, invalidReason: 'bad' })).toEqual({
			status: 'failed',
			label: 'Invalid'
		});
	});
});
