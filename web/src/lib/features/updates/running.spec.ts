// Running update checks by policy (#20): the records with a running check
// (badges spin for them), from the running jobs list.
import { describe, expect, it } from 'vitest';
import type { Job } from '$lib/api/client';
import { checkingPolicies } from './running';

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
