// Jobs leaving a running list (#10): a backup or retention shown from GET
// /backup-activity disappears from it when it ends. The view reports its
// outcome (a toast, refreshed data) once: the job is read when it has left
// the list and handed to `onfinish` if it really ended.
import { untrack } from 'svelte';
import { api, type ApiClient, type Job } from '$lib/api/client';

const TERMINAL = ['succeeded', 'failed', 'partial', 'cancelled', 'interrupted'];

/** Jobs that were in `before` and are not in `now`. */
export function leftList(before: readonly string[], now: readonly string[]): string[] {
	return before.filter((id) => !now.includes(id));
}

/**
 * Calls `onfinish` once for every job of `ids` (the running list) that
 * leaves it and has ended. Call during component initialisation.
 */
export function onJobsFinished(
	ids: () => string[],
	onfinish: (job: Job) => void,
	client: ApiClient = api
) {
	let seen: string[] = [];
	const told: string[] = [];
	$effect(() => {
		const now = ids();
		untrack(() => {
			for (const id of leftList(seen, now)) {
				if (told.includes(id)) continue;
				told.push(id);
				void client
					.GET('/api/v1/jobs/{jobId}', { params: { path: { jobId: id } } })
					.then((r) => {
						if (r.data && TERMINAL.includes(r.data.state)) onfinish(r.data);
					})
					.catch(() => {});
			}
			seen = now;
		});
	});
}
