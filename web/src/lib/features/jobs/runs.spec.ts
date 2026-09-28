import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { groupRuns, runDuration, runJob, runSummary } from './runs';

const job = (id: string, j: Partial<Job>): Job =>
	({
		id,
		kind: 'update.check',
		state: 'succeeded',
		origin: 'scheduled',
		executor: 'manager',
		targets: [],
		attempt: 1,
		progress: {},
		items: [],
		locks: [],
		locksHeld: false,
		cancelRequested: false,
		cancellable: false,
		createdAt: '2026-09-27T03:00:00Z',
		updatedAt: '2026-09-27T03:00:00Z',
		...j
	}) as Job;

describe('policy runs (#13)', () => {
	it('sums up the jobs of a run in one line', () => {
		const ok = { state: 'succeeded' };
		expect(runSummary(Array(20).fill(ok), 'update.check').text).toBe(
			'20 checks, all succeeded'
		);
		const mixed = [...Array(18).fill(ok), { state: 'failed' }, { state: 'partial' }];
		expect(runSummary(mixed, 'update.check')).toMatchObject({
			text: '2 of 20 checks failed',
			state: 'failed',
			failed: 2
		});
		expect(runSummary([ok, { state: 'running' }], 'update.run').text).toBe(
			'Running 1 of 2 updates'
		);
		expect(runSummary([ok], 'prune.run').text).toBe('The prune succeeded');
		expect(runSummary([{ state: 'cancelled' }], 'stack.deploy').text).toBe(
			'The job was cancelled'
		);
	});

	it('groups the jobs one run started, newest run first', () => {
		const runs = groupRuns([
			job('c', { createdAt: '2026-09-28T03:00:00Z' }),
			job('b', { createdAt: '2026-09-27T03:00:05Z' }),
			job('a', { createdAt: '2026-09-27T03:00:00Z' }),
			job('m', { createdAt: '2026-09-27T03:00:01Z', origin: 'manual' })
		]);
		expect(runs.map((r) => r.jobs.map((j) => j.id))).toEqual([['c'], ['m'], ['a', 'b']]);
		expect(runs[2].at).toBe('2026-09-27T03:00:00Z');
	});

	it('times a finished run and opens its failed job', () => {
		const run = {
			jobs: [
				job('a', {
					startedAt: '2026-09-27T03:00:00Z',
					finishedAt: '2026-09-27T03:00:20Z'
				}),
				job('b', {
					state: 'failed',
					startedAt: '2026-09-27T03:00:01Z',
					finishedAt: '2026-09-27T03:03:20Z'
				})
			]
		};
		expect(runDuration(run)).toBe('3 min 20 s');
		expect(runJob(run)?.id).toBe('b');
		expect(runJob({ jobs: [run.jobs[0], run.jobs[0]] })).toBeUndefined();
		expect(runDuration({ jobs: [job('x', {})] })).toBe('');
	});
});
