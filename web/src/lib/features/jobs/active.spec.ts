// Running jobs of a view (docs/internal/web.md, "Job progress after
// reload"): matching, the tracked union with finished jobs kept, and the
// cap on per-job event streams.
import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { ACTIVE_JOB_STATES, isActiveJobState, isTerminal } from '$lib/api/job-states';
import { matchJob, matchingJobs, streamedIds, trackedEntries, type KnownJob } from './active';
import { STATE_FILTERS, jobActive } from './labels';

function job(p: Partial<Job> & { id: string }): Job {
	return {
		kind: 'stack.deploy',
		state: 'running',
		origin: 'manual',
		executor: 'agent',
		targets: [],
		attempt: 1,
		progress: {},
		items: [],
		locks: [],
		locksHeld: true,
		cancelRequested: false,
		cancellable: true,
		retryable: false,
		createdAt: '2026-09-28T10:00:00Z',
		updatedAt: '2026-09-28T10:00:00Z',
		...p
	} as Job;
}

describe('job states', () => {
	it('keeps one list of the states of a job that has not ended', () => {
		expect([...ACTIVE_JOB_STATES]).toEqual([
			'queued',
			'blocked',
			'dispatched',
			'running',
			'cancelling'
		]);
		for (const s of ACTIVE_JOB_STATES) {
			expect(isActiveJobState(s)).toBe(true);
			expect(isTerminal(s)).toBe(false);
			expect(jobActive(s)).toBe(true);
		}
		expect(isActiveJobState('succeeded')).toBe(false);
		expect(isActiveJobState(undefined)).toBe(false);
		expect(STATE_FILTERS.find((f) => f.id === 'active')?.states).toEqual([
			...ACTIVE_JOB_STATES
		]);
	});
});

describe('matchJob', () => {
	const web1 = job({
		id: 'j1',
		kind: 'container.restart',
		environmentId: 'e1',
		targets: [{ type: 'container', id: 'web' }]
	});

	it('matches a target, falling back to the job environment for the target', () => {
		expect(matchJob(web1, { targets: [{ type: 'container', id: 'web' }] })).toBe(true);
		expect(
			matchJob(web1, { targets: [{ type: 'container', id: 'web', environmentId: 'e1' }] })
		).toBe(true);
		expect(
			matchJob(web1, { targets: [{ type: 'container', id: 'web', environmentId: 'e2' }] })
		).toBe(false);
		expect(matchJob(web1, { targets: [{ type: 'container', id: 'db' }] })).toBe(false);
		expect(matchJob(web1, { targets: [{ type: 'volume', id: 'web' }] })).toBe(false);
	});

	it("uses the target's own environment when it has one (migrations)", () => {
		const moved = job({
			id: 'j2',
			kind: 'stack.migrate',
			environmentId: 'e2',
			targets: [{ type: 'volume', id: 'data', environmentId: 'e1' }]
		});
		expect(
			matchJob(moved, { targets: [{ type: 'volume', id: 'data', environmentId: 'e1' }] })
		).toBe(true);
		expect(
			matchJob(moved, { targets: [{ type: 'volume', id: 'data', environmentId: 'e2' }] })
		).toBe(false);
		// The environment filter takes the job's or any target's environment.
		expect(matchJob(moved, { environmentId: 'e1' })).toBe(true);
		expect(matchJob(moved, { environmentId: 'e2' })).toBe(true);
		expect(matchJob(moved, { environmentId: 'e3' })).toBe(false);
	});

	it('matches any of several targets and target ID prefixes', () => {
		const img = job({
			id: 'j3',
			kind: 'image.pull',
			environmentId: 'e1',
			targets: [{ type: 'image', id: 'nginx:1.27' }]
		});
		expect(
			matchJob(img, {
				targets: [
					{ type: 'image', id: 'sha256:abc' },
					{ type: 'image', id: 'nginx:1.27' }
				]
			})
		).toBe(true);
		const copy = job({
			id: 'j4',
			kind: 'files.copy',
			environmentId: 'e1',
			targets: [{ type: 'path', id: '/stack/s1/config' }]
		});
		expect(
			matchJob(copy, {
				kindPrefix: 'files.',
				targets: [{ type: 'path', idPrefix: '/stack/s1/' }]
			})
		).toBe(true);
		expect(
			matchJob(copy, {
				kindPrefix: 'files.',
				targets: [{ type: 'path', idPrefix: '/stack/s2/' }]
			})
		).toBe(false);
	});

	it('filters kinds by list, prefix and exclusion', () => {
		expect(matchJob(web1, { kinds: ['container.restart', 'container.stop'] })).toBe(true);
		expect(matchJob(web1, { kinds: ['container.stop'] })).toBe(false);
		expect(matchJob(web1, { kindPrefix: ['image.', 'container.'] })).toBe(true);
		expect(matchJob(web1, { kinds: ['image.pull'], kindPrefix: 'container.' })).toBe(true);
		expect(matchJob(web1, { excludeKinds: ['container.restart'] })).toBe(false);
		expect(
			matchJob(
				job({ id: 'j5', kind: 'update.check', targets: [{ type: 'stack', id: 's1' }] }),
				{
					targets: [{ type: 'stack', id: 's1' }],
					excludeKinds: ['update.check']
				}
			)
		).toBe(false);
	});

	it('filters by policy, null meaning jobs run for no policy', () => {
		const scheduled = job({
			id: 'j6',
			kind: 'prune.run',
			environmentId: 'e1',
			policyId: 'mp-1'
		});
		const oneOff = job({ id: 'j7', kind: 'prune.run', environmentId: 'e1' });
		expect(matchJob(scheduled, { policyId: 'mp-1' })).toBe(true);
		expect(matchJob(oneOff, { policyId: 'mp-1' })).toBe(false);
		const hostPrune = { kinds: ['prune.run'], policyId: null, environmentId: 'e1' };
		expect(matchJob(oneOff, hostPrune)).toBe(true);
		expect(matchJob(scheduled, hostPrune)).toBe(false);
	});

	it('lists nothing without a match', () => {
		expect(matchingJobs([web1], null)).toEqual([]);
		expect(matchingJobs(undefined, {})).toEqual([]);
		expect(matchingJobs([web1], { environmentId: 'e1' })).toEqual([web1]);
	});
});

describe('trackedEntries', () => {
	it('shows jobs started here at once, before the running list has them', () => {
		const known: KnownJob[] = [{ id: 'b', title: 'Deploy Silo', local: true }];
		expect(trackedEntries(known, [])).toEqual([
			{ id: 'b', job: undefined, title: 'Deploy Silo', active: true, listed: false }
		]);
	});

	it('unites local jobs and matching running jobs, newest first, without duplicates', () => {
		const a = job({ id: '0190-a' });
		const c = job({ id: '0190-c', progress: { percent: 40 } });
		const known: KnownJob[] = [{ id: '0190-c', title: 'Deploy Silo', local: true }];
		const out = trackedEntries(known, [c, a]);
		expect(out.map((e) => e.id)).toEqual(['0190-c', '0190-a']);
		// The running list's data wins (its progress is newer).
		expect(out[0]).toMatchObject({ title: 'Deploy Silo', active: true, listed: true });
		expect(out[0].job?.progress.percent).toBe(40);
	});

	it('keeps jobs that ended until they are dismissed', () => {
		const done = job({ id: 'a', state: 'succeeded' });
		// Seen running, now gone from the list, not yet reported finished:
		// no longer active (its stream replays the end).
		expect(trackedEntries([{ id: 'a', job: job({ id: 'a' }), listed: true }], [])).toEqual([
			{ id: 'a', job: job({ id: 'a' }), title: undefined, active: false, listed: false }
		]);
		// Finished: the ended job is kept even while a stale list still has it.
		const finished: KnownJob[] = [{ id: 'a', job: done, listed: true, finished: true }];
		expect(trackedEntries(finished, [job({ id: 'a' })])).toEqual([
			{ id: 'a', job: done, title: undefined, active: false, listed: false }
		]);
		expect(trackedEntries(finished, [], new Set(['a']))).toEqual([]);
		// A dismissed job is not brought back by a stale list either.
		expect(trackedEntries([], [job({ id: 'a' })], new Set(['a']))).toEqual([]);
	});
});

describe('streamedIds', () => {
	it('streams at most the cap of listed running jobs, and every other entry', () => {
		const entries = [
			{ id: 'e1', active: true, listed: true },
			{ id: 'e2', active: true, listed: true },
			{ id: 'e3', active: true, listed: true },
			{ id: 'e4', active: true, listed: true },
			{ id: 'e5', active: false, listed: false }
		];
		expect([...streamedIds(entries, 2)]).toEqual(['e1', 'e2', 'e5']);
	});

	it('counts jobs just started (not listed yet) against the cap', () => {
		const entries = [
			{ id: 'new', active: true, listed: false },
			{ id: 'e1', active: true, listed: true },
			{ id: 'e2', active: true, listed: true }
		];
		expect([...streamedIds(entries, 2)].sort()).toEqual(['e1', 'new']);
	});
});
