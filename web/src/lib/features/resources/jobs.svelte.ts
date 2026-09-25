// Follows the job a Docker mutation started (#6: every mutation answers
// 202 with a job, #26) until it ends: the page shows it while it runs
// (activeJobs, keyed by resource), the finish is a toast that repeats the
// action ("Stopped silo-web") or says why it failed, a notice in the bell,
// and a refresh of the affected queries. Live events (#23) refresh views
// too; the explicit refresh covers a missed event.
import type { QueryClient, QueryKey } from '@tanstack/svelte-query';
import { JobWatcher } from '$lib/api/jobs.svelte';
import type { Job } from '$lib/api/client';
import { routes } from '$lib/routes';
import { notices as appNotices, type Notices } from '$lib/shell/notices.svelte';
import { toast as appToast, type Toasts } from '$lib/ui/toast.svelte';
import { doneTitle, jobFailure, type RefusalContext } from './refusals';

class ActiveJobs {
	/** Running jobs by resource key (e.g. "container:<env>/<name>"). */
	byKey = $state<Record<string, JobWatcher>>({});

	set(key: string, w: JobWatcher) {
		this.byKey = { ...this.byKey, [key]: w };
	}

	clear(key: string, w: JobWatcher) {
		if (this.byKey[key] !== w) return;
		const next = { ...this.byKey };
		delete next[key];
		this.byKey = next;
	}
}

export const activeJobs = new ActiveJobs();

export function resourceKey(kind: string, env: string, name: string): string {
	return `${kind}:${env}/${name}`;
}

export interface TrackOptions {
	ctx: RefusalContext;
	/** activeJobs key of the resource (shows the running job on its page). */
	key?: string;
	queryClient?: QueryClient;
	/** Query key prefixes to refresh when the job ends. */
	invalidate?: QueryKey[];
	/** Success toast title (default: doneTitle(verb, name)). */
	success?: string;
	onfinish?: (job: Job) => void;
	/** Test seams. */
	watcher?: (id: string, onfinish: (job: Job) => void) => JobWatcher;
	toast?: Pick<Toasts, 'success' | 'error' | 'warn'>;
	notices?: Notices | null;
}

/** Starts following job; returns its watcher (already started). */
export function trackJob(job: Pick<Job, 'id'>, o: TrackOptions): JobWatcher {
	const t = o.toast ?? appToast;
	const n = o.notices === undefined ? appNotices : o.notices;
	const finish = (j: Job) => {
		const ok = j.state === 'succeeded';
		if (ok) {
			const title = o.success ?? doneTitle(o.ctx.verb, o.ctx.name);
			t.success(title);
			n?.push({ key: `job:${j.id}`, kind: 'job', tone: 'ok', title, href: routes.job(j.id) });
		} else {
			const r = jobFailure(j, o.ctx);
			const tone =
				j.state === 'cancelled' ? 'info' : j.state === 'partial' ? 'warn' : 'danger';
			if (j.state === 'cancelled') t.warn(r.title, { body: r.body });
			else t.error(r.title, { body: r.body });
			n?.push({
				key: `job:${j.id}`,
				kind: 'job',
				tone,
				title: r.title,
				body: r.body,
				href: routes.job(j.id)
			});
		}
		for (const k of o.invalidate ?? []) void o.queryClient?.invalidateQueries({ queryKey: k });
		if (o.key) activeJobs.clear(o.key, w);
		o.onfinish?.(j);
	};
	// finish runs only after the watcher exists (the job ends later).
	const w = o.watcher ? o.watcher(job.id, finish) : new JobWatcher(job.id, { onfinish: finish });
	if (o.key) activeJobs.set(o.key, w);
	w.start();
	return w;
}

/** A fresh Idempotency-Key for one user action (#4: safe retries). */
export function idempotencyKey(): string {
	return (
		globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`
	);
}
