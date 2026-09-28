// Runs a bulk action (#6, #22 polish): one request per object through the
// same helpers as the single-object actions, every accepted job followed
// with trackJob (quietly: no toast or notice per object), and one summary
// toast once every job has ended. Refused requests count as failed.
import type { QueryClient, QueryKey } from '@tanstack/svelte-query';
import type { Job } from '$lib/api/client';
import { toast as appToast, type Toasts } from '$lib/ui/toast.svelte';
import { bulkSummary, type BulkOutcome, type BulkPlan } from './bulk';
import { trackJob, type TrackOptions } from './jobs.svelte';
import type { RefusalContext } from './refusals';

export interface BulkRun<T> {
	plan: BulkPlan<T>;
	/** "stop", "remove" (the summary's verb). */
	verb: string;
	noun: { one: string; many: string };
	name: (item: T) => string;
	/** Sends one request (the single-object helper); resolves to its job. */
	send: (item: T) => Promise<Pick<Job, 'id'>>;
	ctx: (item: T) => RefusalContext;
	queryClient?: QueryClient;
	invalidate?: QueryKey[];
	/** Test seams. */
	toast?: Pick<Toasts, 'success' | 'warn' | 'error'>;
	watcher?: TrackOptions['watcher'];
}

const quiet = { success: () => 0, warn: () => 0, error: () => 0 };

/** Sends every request; resolves once they are sent (the summary follows the jobs). */
export async function runBulk<T>(r: BulkRun<T>): Promise<void> {
	const t = r.toast ?? appToast;
	const outcome: BulkOutcome = {
		succeeded: [],
		failed: [],
		refused: r.plan.refused.map((x) => ({ name: r.name(x.item), reason: x.reason })),
		skipped: r.plan.skipped.length
	};
	let pending = r.plan.run.length;
	let reported = false;
	const report = () => {
		if (reported || pending > 0) return;
		reported = true;
		const s = bulkSummary(r.verb, r.noun, outcome);
		if (s.body) t[s.tone](s.title, { body: s.body });
		else t[s.tone](s.title);
	};

	const sent = await Promise.allSettled(r.plan.run.map((item) => r.send(item)));
	sent.forEach((res, i) => {
		const item = r.plan.run[i];
		if (res.status === 'rejected') {
			outcome.failed.push(r.name(item));
			pending--;
			return;
		}
		trackJob(res.value, {
			ctx: r.ctx(item),
			queryClient: r.queryClient,
			invalidate: r.invalidate,
			toast: quiet,
			notices: null,
			watcher: r.watcher,
			onfinish: (j) => {
				(j.state === 'succeeded' ? outcome.succeeded : outcome.failed).push(r.name(item));
				pending--;
				report();
			}
		});
	});
	report();
}
