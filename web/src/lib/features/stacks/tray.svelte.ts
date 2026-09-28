// Jobs of a stack page (#22: long operations show JobProgress with partial
// failures). The stack layout owns one tray and shows it under the header;
// actions add the job they started with the copy for its end ("Deployed
// Silo" / "Silo was not deployed"), and the layout adopts the stack's
// running jobs from the running list (`adopt`), so a reload or coming back
// finds them again (docs/internal/web.md, "Job progress after reload").
// Finished jobs stay until the user dismisses them, so failures and their
// recovery advice are not lost.
import { getContext, setContext } from 'svelte';
import { SvelteSet } from 'svelte/reactivity';
import type { Job } from '$lib/api/client';

/** A success toast with a body or an action ("Deploy"). */
export interface SuccessToast {
	title: string;
	body?: string;
	action?: { label: string; onclick: () => void };
}

export interface TrackedJob {
	id: string;
	/** The job's kind when a view depends on it (stack.rename turns the header's actions off). */
	kind?: string;
	/** What runs, e.g. "Deploy Silo". */
	title: string;
	/** Toast when it succeeds, e.g. "Deployed Silo". */
	success: string;
	/**
	 * The success toast computed once the job ended (e.g. "Nothing to
	 * deploy" when a deploy changed nothing); `success` when it fails.
	 */
	successFor?: (job: Job) => Promise<string | SuccessToast>;
	/** Toast title when it fails, e.g. "Silo was not deployed". */
	failure: string;
	/** Called once when the job ends (any terminal state). */
	onfinish?: (job: Job) => void;
	/** Show progress only: the caller reports the end itself. */
	silent?: boolean;
}

/** Jobs a tray shows at most. */
const TRAY_SIZE = 5;

export class JobTray {
	jobs = $state<TrackedJob[]>([]);
	/** IDs of jobs that reached a terminal state. */
	finished = $state<string[]>([]);
	/** Every job the tray showed (a dismissed or pushed-out job is not adopted again). */
	#seen = new SvelteSet<string>();

	add(job: Pick<Job, 'id'>, t: Omit<TrackedJob, 'id'>) {
		this.#seen.add(job.id);
		this.jobs = [{ id: job.id, ...t }, ...this.jobs.filter((j) => j.id !== job.id)].slice(
			0,
			TRAY_SIZE
		);
	}

	/**
	 * Shows running jobs the tray has not shown yet (the stack's jobs from
	 * the running list after a reload, or started elsewhere), oldest
	 * first so the newest ends up on top. A job an action added keeps
	 * the action's copy.
	 */
	adopt(running: readonly Job[], describe: (job: Job) => Omit<TrackedJob, 'id'>) {
		const fresh = running.filter((j) => !this.#seen.has(j.id));
		for (const j of fresh.toReversed()) this.add(j, describe(j));
	}

	/** Forgets every job (the layout now shows another stack). */
	reset() {
		this.jobs = [];
		this.finished = [];
		this.#seen.clear();
	}

	markFinished(id: string) {
		if (!this.finished.includes(id)) this.finished = [...this.finished, id];
	}

	dismiss(id: string) {
		this.jobs = this.jobs.filter((j) => j.id !== id);
		this.finished = this.finished.filter((f) => f !== id);
	}

	/** A job of this tray is still running (e.g. to disable a second deploy). */
	get busy(): boolean {
		return this.jobs.some((j) => !this.finished.includes(j.id));
	}

	/** A job of this kind (e.g. stack.rename) this tray tracks is still running. */
	running(kind: string): boolean {
		return this.jobs.some((j) => j.kind === kind && !this.finished.includes(j.id));
	}
}

const KEY = Symbol('stack-job-tray');

export function provideJobTray(tray = new JobTray()): JobTray {
	setContext(KEY, tray);
	return tray;
}

/** The enclosing stack page's tray (a detached one outside it). */
export function useJobTray(): JobTray {
	return getContext<JobTray | undefined>(KEY) ?? new JobTray();
}
