// Running update jobs by policy (#20): the policies with a running check
// (badges spin for them) and the running checks and updates per policy
// (the Updates list), from the running jobs list.
import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { activityByPolicy, activityText, checkingPolicies } from './running';

function job(kind: string, policyId: string | undefined, state: Job['state'] = 'running'): Job {
	return {
		id: `${kind}-${policyId}-${state}`,
		kind,
		state,
		policyId,
		targets: []
	} as unknown as Job;
}

describe('checkingPolicies', () => {
	it('lists the policies with an unfinished update check, once per list', () => {
		const jobs = [
			job('update.check', 'p1'),
			job('update.check', 'p2', 'queued'),
			job('update.check', 'p3', 'succeeded'),
			job('update.run', 'p4'),
			job('update.check', undefined)
		];
		const set = checkingPolicies(jobs);
		expect([...set].sort()).toEqual(['p1', 'p2']);
		// Every badge reading the same list shares the result.
		expect(checkingPolicies(jobs)).toBe(set);
		expect(checkingPolicies(undefined).size).toBe(0);
	});
});

describe('activityByPolicy', () => {
	it('counts running checks and updates per policy', () => {
		const a = activityByPolicy([
			job('update.check', 'p1'),
			job('update.check', 'p1', 'dispatched'),
			job('update.run', 'p1'),
			job('update.run', 'p2', 'failed'),
			job('prune.run', 'p3'),
			job('update.run', undefined)
		]);
		expect(a.get('p1')).toEqual({ checks: 2, runs: 1 });
		expect(a.has('p2')).toBe(false);
		expect(a.has('p3')).toBe(false);
		expect(activityText(a.get('p1'))).toBe('1 update running, 2 checks running');
		expect(activityText({ checks: 1, runs: 0 })).toBe('1 check running');
		expect(activityText(undefined)).toBe('');
	});
});
