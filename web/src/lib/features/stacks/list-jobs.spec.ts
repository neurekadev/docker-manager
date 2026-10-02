import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { matchingJobs, trackedEntries } from '$lib/features/jobs/active';
import { runningByStack, runningHint, runningLabel, stackListMatch } from './list-jobs';

function job(id: string, p: Partial<Job> = {}): Job {
	return {
		id,
		kind: 'stack.deploy',
		state: 'running',
		environmentId: 'e1',
		targets: [{ type: 'stack', id: 'st-1' }],
		items: [],
		progress: {},
		...p
	} as Job;
}

describe('stack list jobs', () => {
	it('maps each stack to its newest running job from the running list', () => {
		// The running list is newest first.
		const list = [
			job('0190-4', { kind: 'stack.stop', targets: [{ type: 'stack', id: 'st-2' }] }),
			job('0190-3', { kind: 'update.check' }),
			job('0190-2', { kind: 'stack.start' }),
			job('0190-1', {
				kind: 'container.restart',
				targets: [{ type: 'container', id: 'web' }]
			}),
			job('0190-0', { environmentId: 'e2', targets: [{ type: 'stack', id: 'st-9' }] })
		];
		const running = runningByStack(
			trackedEntries([], matchingJobs(list, stackListMatch('e1')))
		);
		expect([...running].map(([s, j]) => [s, j.id])).toEqual([
			['st-2', '0190-4'],
			['st-1', '0190-2']
		]);
		// Every environment without a selection.
		expect(
			runningByStack(trackedEntries([], matchingJobs(list, stackListMatch(null)))).get('st-9')
				?.id
		).toBe('0190-0');
	});

	it('shows a job started here at once and drops it once it ended', () => {
		const started = job('0190-5');
		const shown = runningByStack(
			trackedEntries([{ id: started.id, job: started, local: true }], [])
		);
		expect(shown.get('st-1')?.id).toBe('0190-5');
		const ended = runningByStack(
			trackedEntries(
				[
					{
						id: started.id,
						job: { ...started, state: 'succeeded' },
						local: true,
						finished: true
					}
				],
				[]
			)
		);
		expect(ended.size).toBe(0);
	});

	it('says what runs', () => {
		expect(runningLabel({ kind: 'stack.deploy', state: 'running' })).toBe('Deploying');
		expect(runningLabel({ kind: 'stack.stop', state: 'cancelling' })).toBe('Stopping');
		expect(runningLabel({ kind: 'stack.deploy', state: 'queued' })).toBe('Waiting');
		expect(runningLabel({ kind: 'stack.deploy', state: 'blocked' })).toBe('Waiting');
		expect(runningLabel({ kind: 'files.copy', state: 'running' })).toBe('Running');
		expect(runningLabel({ kind: 'environment.migrate', state: 'running' })).toBe('Migrating');
		expect(runningHint({ kind: 'stack.deploy' })).toBe('Deploy Stack: open the job');
	});
});
