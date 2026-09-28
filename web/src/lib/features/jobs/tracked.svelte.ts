// The jobs one view shows (docs/internal/web.md, "Job progress after
// reload"): the jobs it started (shown at once), the caller's running jobs
// that match what it shows (activeJobsQuery, so a reload or coming back
// finds them again) and the jobs it showed that have ended since (kept
// until dismissed or the view goes away, so the outcome stays visible).
// Never keep a job ID only in a component: add it here and let the running
// list bring it back.
import { createQuery } from '@tanstack/svelte-query';
import { untrack } from 'svelte';
import type { ApiClient, Job } from '$lib/api/client';
import { activeJobsQuery } from '$lib/api/queries';
import { matchingJobs, trackedEntries, type JobMatch, type KnownJob } from './active';

export class TrackedJobs {
	/** Jobs this view has shown, newest first. */
	known = $state<KnownJob[]>([]);
	/** Ended jobs the user dismissed. */
	dismissed = $state<string[]>([]);

	readonly #list: () => readonly Job[] | undefined;
	readonly #match: () => JobMatch | null;
	/** The match the jobs were collected for (JSON). */
	#key: string | undefined;

	/** The caller's running jobs matching the view. */
	readonly running = $derived.by(() => matchingJobs(this.#list(), this.#match()));
	/** Everything to show, newest first. */
	readonly entries = $derived.by(() => trackedEntries(this.known, this.running, this.dismissed));
	/** A job of the view is still running. */
	readonly busy = $derived(this.entries.some((e) => e.active));

	constructor(list: () => readonly Job[] | undefined, match: () => JobMatch | null) {
		this.#list = list;
		this.#match = match;
	}

	/**
	 * Starts over when the match changed since the jobs were collected
	 * (another object in the same layout). `add` checks first, so a job
	 * started right after the match changed is kept.
	 */
	checkMatch() {
		const k = JSON.stringify(this.#match());
		untrack(() => {
			if (this.#key !== undefined && k !== this.#key) this.reset();
		});
		this.#key = k;
	}

	/** A job this view just started (shown before the running list has it). */
	add(job: Pick<Job, 'id'> & Partial<Job>, title?: string) {
		untrack(() => this.checkMatch());
		const full = job.kind !== undefined && job.state !== undefined ? (job as Job) : undefined;
		this.dismissed = this.dismissed.filter((d) => d !== job.id);
		this.known = [
			{ id: job.id, job: full, title, local: true },
			...this.known.filter((k) => k.id !== job.id)
		];
	}

	/** The job ended (JobProgress's onfinish): keep its outcome. */
	markFinished(job: Job) {
		const i = this.known.findIndex((k) => k.id === job.id);
		this.known =
			i >= 0
				? this.known.with(i, { ...this.known[i], job, finished: true })
				: [{ id: job.id, job, finished: true, listed: true }, ...this.known];
	}

	/** Hides an ended job. */
	dismiss(id: string) {
		this.dismissed = [...this.dismissed, id];
		this.known = this.known.filter((k) => k.id !== id);
	}

	/** Forgets everything (the view now shows another object). */
	reset() {
		this.known = [];
		this.dismissed = [];
	}

	/** A running job of the view of one of these kinds. */
	runningOf(...kinds: string[]): Job | undefined {
		for (const e of this.entries)
			if (e.active && e.job && kinds.includes(e.job.kind)) return e.job;
		return undefined;
	}

	/** Records the running jobs as shown (useTrackedJobs runs it on every list change). */
	sync() {
		const running = this.running;
		untrack(() => {
			let next = this.known;
			for (const j of running) {
				const i = next.findIndex((k) => k.id === j.id);
				if (i < 0) next = [...next, { id: j.id, job: j, listed: true }];
				else if (!next[i].listed) next = next.with(i, { ...next[i], listed: true });
			}
			if (next !== this.known) this.known = next;
		});
	}
}

export interface TrackedJobsOptions {
	/** Load the running list only while this holds (default: always). */
	enabled?: () => boolean;
	client?: ApiClient;
}

/**
 * The jobs a view shows for `match` (null: nothing yet, e.g. while the
 * object loads). A change of `match` (another object in the same layout)
 * starts over. Call during component initialisation.
 */
export function useTrackedJobs(
	match: () => JobMatch | null,
	o: TrackedJobsOptions = {}
): TrackedJobs {
	const list = createQuery(() => ({
		...activeJobsQuery(o.client),
		enabled: (o.enabled?.() ?? true) && match() !== null
	}));
	const tracked = new TrackedJobs(() => list.data, match);
	$effect.pre(() => tracked.checkMatch());
	$effect(() => tracked.sync());
	return tracked;
}
