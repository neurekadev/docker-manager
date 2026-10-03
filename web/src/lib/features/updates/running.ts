// Running update checks by policy (#20, docs/internal/web.md "Job progress
// after reload"), read from the caller's running jobs (activeJobsQuery):
// which target records have a check running (image update badges spin for
// them, also after a reload or for a check started elsewhere). Pure;
// tested in running.spec.ts.
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
