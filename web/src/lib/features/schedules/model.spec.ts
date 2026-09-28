import { describe, expect, it } from 'vitest';
import type { Schedule } from '$lib/api/client';
import { applyListFilters } from '$lib/features/resources/filters';
import { scheduleFilters, scheduleSearch } from './filters';
import {
	dstLabel,
	formatRunTime,
	policyHref,
	runReason,
	runStatus,
	scheduleScope,
	scheduleState,
	sharedNames
} from './model';

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
			'Docker Manager was not running at 3 scheduled times.'
		);
		expect(runReason(run({ outcome: 'skipped', errorClass: 'previous_run_active' }))).toBe(
			'The previous run was still active.'
		);
		expect(
			runReason(run({ outcome: 'rejected', reason: 'The environment is archived.' }))
		).toBe('The environment is archived.');
		expect(runReason(run({ outcome: 'failed', errorClass: 'target_not_found' }))).toBe(
			'What the policy covers no longer exists.'
		);
		// A reason that is a class is never shown raw.
		expect(runReason(run({ outcome: 'skipped', reason: 'skipped_overlap' }))).toBe(
			'The previous run was still active.'
		);
		expect(runReason(run({ outcome: 'failed', errorClass: 'some_new_class' }))).toBe(
			'Some new class.'
		);
	});

	it('links each kind to where its policies are edited and words the state', () => {
		expect(policyHref('prune')).toBe('/maintenance');
		expect(policyHref('prune', 'mp-1')).toBe('/maintenance/mp-1');
		expect(policyHref('update_run')).toBe('/updates');
		expect(policyHref('update_check', 'up-1')).toBe('/updates/up-1');
		expect(policyHref('backup', 'bp-1')).toBe('/backups/policies/bp-1');
		expect(policyHref('backup_verification', 'br-1')).toBe('/backups/repositories/br-1');
		expect(policyHref('backup_verification')).toBe('/backups/repositories');
		expect(scheduleState({ enabled: true })).toEqual({ status: 'running', label: 'Enabled' });
		expect(scheduleState({ enabled: false })).toEqual({ status: 'stopped', label: 'Disabled' });
		expect(scheduleState({ enabled: true, invalidReason: 'bad' })).toEqual({
			status: 'failed',
			label: 'Invalid'
		});
	});
});

describe('schedule filters (#13)', () => {
	const sched = (x: Partial<Schedule> & { id: string }): Schedule =>
		({
			policyName: x.id,
			kind: 'backup',
			kindLabel: 'Backup',
			cron: '0 3 * * *',
			timeZone: 'UTC',
			enabled: true,
			recentRuns: [],
			...x
		}) as Schedule;
	const rows = [
		sched({ id: 'nightly', environmentId: 'e1' }),
		sched({
			id: 'weekly prune',
			kind: 'prune',
			kindLabel: 'Prune',
			enabled: false,
			environmentId: 'e2',
			timeZone: 'Europe/Berlin'
		}),
		sched({ id: 'manager state', invalidReason: 'bad cron' })
	];
	const envs = [
		{ id: 'e2', name: 'nas' },
		{ id: 'e1', name: 'homelab' }
	];
	const names = new Map(envs.map((e) => [e.id, e.name]));
	const run = (values: Record<string, string>, q = '', all = false) =>
		applyListFilters(
			rows,
			scheduleFilters(rows, { envs: all ? envs : [] }),
			{ q, values },
			scheduleSearch((id) => names.get(id))
		).map((s) => s.id);

	it('filters by kind and state; the environment only while all are shown', () => {
		expect(run({ kind: 'prune' })).toEqual(['weekly prune']);
		expect(run({ state: 'enabled' })).toEqual(['nightly']);
		expect(run({ state: 'disabled' })).toEqual(['weekly prune']);
		expect(run({ state: 'invalid' })).toEqual(['manager state']);
		expect(scheduleFilters(rows, { envs: [] }).map((f) => f.id)).toEqual(['kind', 'state']);
		expect(run({ environment: 'e2' }, '', true)).toEqual(['weekly prune']);
		expect(run({ environment: '-' }, '', true)).toEqual(['manager state']);
		expect(
			scheduleFilters(rows, { envs })
				.find((f) => f.id === 'environment')
				?.options?.map((o) => o.label)
		).toEqual(['Manager or all environments', 'homelab', 'nas']);
	});

	it('searches the policy, kind, time zone and environment', () => {
		expect(run({}, 'NIGHTLY')).toEqual(['nightly']);
		expect(run({}, 'berlin')).toEqual(['weekly prune']);
		expect(run({}, 'homelab')).toEqual(['nightly']);
		expect(run({}, 'manager')).toEqual(['manager state']);
		expect(run({}, 'daily at 03:00')).toEqual(['nightly', 'weekly prune', 'manager state']);
	});

	it('names where a schedule applies and which names need it', () => {
		const envName = (id: string) => names.get(id);
		expect(scheduleScope({ kind: 'backup', environmentId: 'e1' }, envName)).toBe('homelab');
		expect(scheduleScope({ kind: 'update_check' }, envName)).toBe('All environments');
		expect(scheduleScope({ kind: 'backup' }, envName)).toBe('Manager');
		expect([
			...sharedNames([
				{ policyId: 'a', policyName: 'Image updates' },
				{ policyId: 'a', policyName: 'Image updates' },
				{ policyId: 'b', policyName: 'Image updates' },
				{ policyId: 'c', policyName: 'Nightly' }
			])
		]).toEqual(['Image updates']);
	});
});
