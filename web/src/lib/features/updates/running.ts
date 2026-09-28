// Running update jobs by policy (#20, docs/internal/web.md "Job progress
// after reload"), read from the caller's running jobs (activeJobsQuery):
// which policies have a check running (image update badges spin for them,
// also after a reload or for a check started elsewhere) and how many
// checks and updates run per policy (the Updates list). Pure; tested in
// running.spec.ts.
import type { Job } from '$lib/api/client';
import { isActiveJobState } from '$lib/api/job-states';

const NONE: ReadonlySet<string> = new Set();
// One Set per running list (every badge reads the same array).
const checking = new WeakMap<readonly Job[], ReadonlySet<string>>();

/** The policies with a running update check in `jobs` (the running list). */
export function checkingPolicies(jobs: readonly Job[] | undefined): ReadonlySet<string> {
	if (!jobs) return NONE;
	let set = checking.get(jobs);
	if (!set) {
		set = new Set(
			jobs
				.filter((j) => j.kind === 'update.check' && j.policyId && isActiveJobState(j.state))
				.map((j) => j.policyId!)
		);
		checking.set(jobs, set);
	}
	return set;
}

/** Running checks and updates of one policy. */
export interface PolicyActivity {
	checks: number;
	runs: number;
}

/** The running checks and updates of every policy in `jobs`, by policy ID. */
export function activityByPolicy(jobs: readonly Job[] | undefined): Map<string, PolicyActivity> {
	const out = new Map<string, PolicyActivity>();
	for (const j of jobs ?? []) {
		if (!j.policyId || !isActiveJobState(j.state)) continue;
		if (j.kind !== 'update.check' && j.kind !== 'update.run') continue;
		const a = out.get(j.policyId) ?? { checks: 0, runs: 0 };
		if (j.kind === 'update.check') a.checks++;
		else a.runs++;
		out.set(j.policyId, a);
	}
	return out;
}

/** "2 checks running, 1 update running", or "" when nothing runs. */
export function activityText(a: PolicyActivity | undefined): string {
	if (!a) return '';
	const parts: string[] = [];
	if (a.runs) parts.push(`${a.runs} ${a.runs === 1 ? 'update' : 'updates'} running`);
	if (a.checks) parts.push(`${a.checks} ${a.checks === 1 ? 'check' : 'checks'} running`);
	return parts.join(', ');
}
