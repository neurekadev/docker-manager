// Runs of a policy (#13, #14, #20): the jobs one run started (an update
// policy checks every target in its own job) summed up in one line, "20
// checks, all succeeded" or "2 of 20 checks failed", instead of one line
// per job. Pure; tested in runs.spec.ts.
import type { Job } from '$lib/api/client';
import { formatDuration } from '$lib/ui/format';

/** What a job of a kind is called in a run's summary. */
const NOUNS: Record<string, [string, string]> = {
	'update.check': ['check', 'checks'],
	'update.run': ['update', 'updates'],
	'prune.run': ['prune', 'prunes'],
	'backup.run': ['backup', 'backups'],
	'backup.verify': ['verification', 'verifications'],
	'manager.backup': ['backup', 'backups'],
	'manager.verify': ['verification', 'verifications']
};

const ACTIVE = new Set(['queued', 'blocked', 'dispatched', 'running', 'cancelling', 'enqueued']);
const FAILED = new Set(['failed', 'interrupted', 'partial']);
/** Worst first: the state a run's badge shows. */
const RANK = [
	'failed',
	'interrupted',
	'partial',
	'cancelled',
	'cancelling',
	'running',
	'dispatched',
	'blocked',
	'queued',
	'enqueued',
	'succeeded'
];

export interface RunSummary {
	/** "20 checks, all succeeded", "2 of 20 checks failed", "Running 3 of 20 checks". */
	text: string;
	/** The worst job state (the run's badge). */
	state: string;
	total: number;
	failed: number;
	active: number;
}

/** One line for the jobs of one run (each `{ state }`, of one `kind`). */
export function runSummary(jobs: readonly { state: string }[], kind: string): RunSummary {
	const [one, many] = NOUNS[kind] ?? ['job', 'jobs'];
	const total = jobs.length;
	const failed = jobs.filter((j) => FAILED.has(j.state)).length;
	const active = jobs.filter((j) => ACTIVE.has(j.state)).length;
	const cancelled = jobs.filter((j) => j.state === 'cancelled').length;
	const state = RANK.find((s) => jobs.some((j) => j.state === s)) ?? jobs[0]?.state ?? '';
	const noun = total === 1 ? one : many;
	let text: string;
	if (!total) text = 'No jobs';
	else if (active)
		text = `Running ${total === 1 ? `the ${one}` : `${active} of ${total} ${many}`}`;
	else if (failed)
		text = total === 1 ? `The ${one} failed` : `${failed} of ${total} ${many} failed`;
	else if (cancelled)
		text =
			total === 1 ? `The ${one} was cancelled` : `${cancelled} of ${total} ${many} cancelled`;
	else text = total === 1 ? `The ${one} succeeded` : `${total} ${noun}, all succeeded`;
	return { text, state, total, failed, active };
}

/** Jobs a policy started together (one run). */
export interface JobRun {
	key: string;
	kind: string;
	origin: Job['origin'];
	/** When the run started (its first job was created). */
	at: string;
	jobs: Job[];
}

/**
 * Groups a policy's jobs (newest first, as GET /jobs returns them) into
 * runs: jobs of the same kind and origin created within `windowMs` of the
 * run's first job belong to it.
 */
export function groupRuns(jobs: readonly Job[], windowMs = 120_000): JobRun[] {
	const runs: JobRun[] = [];
	const sorted = [...jobs].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
	for (const j of sorted) {
		const t = Date.parse(j.createdAt);
		const run = runs.find(
			(r) =>
				r.kind === j.kind &&
				r.origin === j.origin &&
				t - Date.parse(r.at) >= 0 &&
				t - Date.parse(r.at) <= windowMs
		);
		if (run) run.jobs.push(j);
		else runs.push({ key: j.id, kind: j.kind, origin: j.origin, at: j.createdAt, jobs: [j] });
	}
	return runs.reverse();
}

/** How long a run took: its first job's start to its last job's end ("" while running). */
export function runDuration(run: Pick<JobRun, 'jobs'>): string {
	if (run.jobs.some((j) => !j.finishedAt)) return '';
	const start = Math.min(...run.jobs.map((j) => Date.parse(j.startedAt ?? j.createdAt)));
	const end = Math.max(...run.jobs.map((j) => Date.parse(j.finishedAt ?? '')));
	if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return '';
	return formatDuration((end - start) / 1000);
}

/** The job a run's summary opens: its first failed job, else its only job. */
export function runJob(run: Pick<JobRun, 'jobs'>): Job | undefined {
	return (
		run.jobs.find((j) => FAILED.has(j.state)) ??
		(run.jobs.length === 1 ? run.jobs[0] : undefined)
	);
}
