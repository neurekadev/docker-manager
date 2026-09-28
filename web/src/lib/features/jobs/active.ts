// Running jobs of a view (docs/internal/web.md, "Job progress after
// reload"): which of the caller's running jobs (activeJobsQuery) belong to
// what a page shows, and which of them the page follows over its own event
// stream. Pure; tested in active.spec.ts.
import type { Job } from '$lib/api/client';

/** A job target a view shows (any of `JobMatch.targets` matches). */
export interface TargetMatch {
	type: string;
	/** The target's ID (stack ID, container or volume name, image reference, path). */
	id?: string;
	/** …or the start of it (a file scope's paths: `/stack/<id>/`). */
	idPrefix?: string;
	/** The target's environment (a target without one is in the job's). */
	environmentId?: string;
}

/** What a view shows; every given field must match. */
export interface JobMatch {
	/** The job acts on one of these. */
	targets?: readonly TargetMatch[];
	/** The job's kind is one of these… */
	kinds?: readonly string[];
	/** …or starts with one of these (`files.`); with `kinds`, either matches. */
	kindPrefix?: string | readonly string[];
	/** Never these kinds (a stack page leaves out update checks). */
	excludeKinds?: readonly string[];
	/** Jobs run for this policy; `null`: jobs run for no policy. */
	policyId?: string | null;
	/** The job is in this environment or acts on a target in it. */
	environmentId?: string;
}

/** What matching reads of a job (a Job or a live `job` event). */
export interface MatchableJob {
	kind: string;
	environmentId?: string;
	policyId?: string;
	targets?: readonly { type: string; id: string; environmentId?: string }[];
}

function targetMatches(
	t: { type: string; id: string; environmentId?: string },
	jobEnv: string | undefined,
	m: TargetMatch
): boolean {
	if (t.type !== m.type) return false;
	if (m.id !== undefined && t.id !== m.id) return false;
	if (m.idPrefix !== undefined && !t.id.startsWith(m.idPrefix)) return false;
	if (m.environmentId && (t.environmentId || jobEnv) !== m.environmentId) return false;
	return true;
}

/** Whether a job belongs to what the view shows (`m`). */
export function matchJob(job: MatchableJob, m: JobMatch): boolean {
	const prefixes = m.kindPrefix === undefined ? [] : [m.kindPrefix].flat();
	if (m.kinds?.length || prefixes.length) {
		const byKind = !!m.kinds?.includes(job.kind);
		const byPrefix = prefixes.some((p) => job.kind.startsWith(p));
		if (!byKind && !byPrefix) return false;
	}
	if (m.excludeKinds?.includes(job.kind)) return false;
	if (m.policyId === null && job.policyId) return false;
	if (typeof m.policyId === 'string' && job.policyId !== m.policyId) return false;
	const targets = job.targets ?? [];
	if (
		m.environmentId &&
		job.environmentId !== m.environmentId &&
		!targets.some((t) => t.environmentId === m.environmentId)
	)
		return false;
	if (
		m.targets &&
		!m.targets.some((tm) => targets.some((t) => targetMatches(t, job.environmentId, tm)))
	)
		return false;
	return true;
}

/** The running jobs of `jobs` matching `m` (null: none). */
export function matchingJobs(jobs: readonly Job[] | undefined, m: JobMatch | null): Job[] {
	return m ? (jobs ?? []).filter((j) => matchJob(j, m)) : [];
}

/** A job a view has shown: started here, or seen in the running list. */
export interface KnownJob {
	id: string;
	/** The latest data (the POST answer, a running-list entry or the finished job). */
	job?: Job;
	/** What the view calls it ("Deploy Silo"); else derived from the job. */
	title?: string;
	/** Started by this view (shows before the running list lists it). */
	local?: boolean;
	/** The running list listed it once. */
	listed?: boolean;
	/** The view saw it end (JobProgress's onfinish). */
	finished?: boolean;
}

/** One job to show. */
export interface TrackedEntry {
	id: string;
	job?: Job;
	title?: string;
	/** Still running (a progress bar; no Dismiss yet). */
	active: boolean;
	/** In the running list now (its progress comes with the list). */
	listed: boolean;
}

/**
 * The jobs a view shows, newest first: the jobs it started (at once), the
 * matching running jobs, and the jobs it showed that have ended since
 * (kept until dismissed, so the outcome stays visible).
 */
export function trackedEntries(
	known: readonly KnownJob[],
	running: readonly Job[],
	dismissed: Iterable<string> = []
): TrackedEntry[] {
	const gone = new Set(dismissed);
	const runningById = new Map(running.map((j) => [j.id, j]));
	const byId = new Map<string, TrackedEntry>();
	for (const k of known) {
		if (gone.has(k.id)) continue;
		const listed = runningById.has(k.id);
		byId.set(k.id, {
			id: k.id,
			job: k.finished ? k.job : (runningById.get(k.id) ?? k.job),
			title: k.title,
			active: !k.finished && (listed || (!!k.local && !k.listed)),
			listed: listed && !k.finished
		});
	}
	for (const j of running) {
		if (byId.has(j.id) || gone.has(j.id)) continue;
		byId.set(j.id, { id: j.id, job: j, active: true, listed: true });
	}
	// Job IDs are time-ordered (UUIDv7): the newest sorts last.
	return [...byId.values()].sort((a, b) => (a.id < b.id ? 1 : a.id > b.id ? -1 : 0));
}

/** Simultaneous job event streams one view opens (HTTP/1.1: six connections per host). */
export const MAX_JOB_STREAMS = 3;

/**
 * The entries a view follows over their own event stream (JobProgress):
 * the first `cap` running ones the list reports, and every entry the list
 * does not (just started, or ended: an ended job's stream replays its end
 * and closes at once). The rest show as compact rows fed by the list.
 */
export function streamedIds(
	entries: readonly Pick<TrackedEntry, 'id' | 'active' | 'listed'>[],
	cap: number = MAX_JOB_STREAMS
): Set<string> {
	const out = new Set<string>();
	// Unlisted running jobs have no list data: they always stream.
	let open = entries.filter((e) => e.active && !e.listed).length;
	for (const e of entries) {
		if (!e.active || !e.listed) out.add(e.id);
		else if (open < cap) {
			out.add(e.id);
			open++;
		}
	}
	return out;
}
