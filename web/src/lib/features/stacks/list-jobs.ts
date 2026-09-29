// Running jobs on the stack list (docs/internal/web.md, "Job progress
// after reload"): one match for the whole list (never a query per row),
// the newest running job of each stack, and what the row says while it
// runs ("Deploying"). A deploy, start or stop started from the list, from
// the stack's page or elsewhere shows in its row, also after a reload.
// Pure; tested in list-jobs.spec.ts.
import type { Job } from '$lib/api/client';
import type { JobMatch } from '$lib/features/jobs/active';
import { jobKindLabel } from '$lib/features/jobs/labels';

/** The jobs acting on the listed stacks (update checks left out). */
export function stackListMatch(environmentId?: string | null): JobMatch {
	return {
		targets: [{ type: 'stack' }],
		excludeKinds: ['update.check'],
		...(environmentId ? { environmentId } : {})
	};
}

/** The newest running job of each stack (entries newest first). */
export function runningByStack(
	entries: readonly { active: boolean; job?: Pick<Job, 'id' | 'kind' | 'state' | 'targets'> }[]
): Map<string, Pick<Job, 'id' | 'kind' | 'state' | 'targets'>> {
	const out = new Map<string, Pick<Job, 'id' | 'kind' | 'state' | 'targets'>>();
	for (const e of entries) {
		if (!e.active || !e.job) continue;
		for (const t of e.job.targets ?? [])
			if (t.type === 'stack' && !out.has(t.id)) out.set(t.id, e.job);
	}
	return out;
}

const RUNNING: Record<string, string> = {
	'environment.migrate': 'Migrating',
	'stack.build': 'Building',
	'stack.deploy': 'Deploying',
	'stack.down': 'Taking down',
	'stack.import': 'Importing',
	'stack.migrate': 'Migrating',
	'stack.pull': 'Pulling',
	'stack.remove': 'Deleting',
	'stack.remove_source': 'Removing source',
	'stack.rename': 'Renaming',
	'stack.restart': 'Restarting',
	'stack.start': 'Starting',
	'stack.stop': 'Stopping',
	'stack.update': 'Updating',
	'update.run': 'Updating',
	'backup.run': 'Backing up',
	'restore.run': 'Restoring'
};

/** What a stack's row says while the job runs: "Deploying"; "Waiting" while queued. */
export function runningLabel(job: Pick<Job, 'kind' | 'state'>): string {
	if (job.state === 'queued' || job.state === 'blocked') return 'Waiting';
	return RUNNING[job.kind] ?? 'Running';
}

/** The indicator's tooltip: "Deploy stack: open the job". */
export function runningHint(job: Pick<Job, 'kind'>): string {
	return `${jobKindLabel(job.kind)}: open the job`;
}
