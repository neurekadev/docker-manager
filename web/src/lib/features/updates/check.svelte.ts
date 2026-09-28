// "Check for updates" from an image's update badge (#20): starts the
// update.check job of the policy covering the image and follows it. The
// running checks are kept by policy, so every badge of that policy (all
// services of a stack) spins until the job ends; then the lists showing
// update states refresh (live events do too; this covers a missed one)
// and a toast says how it went.
import type { QueryClient } from '@tanstack/svelte-query';
import type { Job } from '$lib/api/client';
import { JobWatcher } from '$lib/api/jobs.svelte';
import { liveKeys } from '$lib/live/keys';
import { checkUpdates } from '$lib/features/stacks/actions';
import { errorMessage } from '$lib/ui/errors';
import { toast as appToast, type Toasts } from '$lib/ui/toast.svelte';

class UpdateChecks {
	/** Policy IDs whose check is running. */
	running = $state<Record<string, true>>({});

	has(policyId: string): boolean {
		return !!this.running[policyId];
	}

	set(policyId: string, on: boolean) {
		const next = { ...this.running };
		if (on) next[policyId] = true;
		else delete next[policyId];
		this.running = next;
	}
}

export const updateChecks = new UpdateChecks();

export interface CheckOptions {
	queryClient?: QueryClient;
	/** Test seams. */
	start?: (policyId: string) => Promise<Pick<Job, 'id'>>;
	watcher?: (id: string, onfinish: (job: Job) => void) => { start(): unknown };
	toast?: Pick<Toasts, 'success' | 'error'>;
}

/** The query prefixes showing update states (container lists, stack images, policies). */
export const UPDATE_STATE_KEYS = [
	liveKeys.list('containers'),
	liveKeys.item('containers'),
	['stacks', 'item'],
	liveKeys.list('policies'),
	liveKeys.item('policies')
];

/**
 * Checks the images of one update policy for updates (`label`: what the
 * badge shows, e.g. "nginx:1.27"). Does nothing while that policy's check
 * runs already.
 */
export async function checkForUpdates(
	policyId: string,
	label: string,
	o: CheckOptions = {}
): Promise<void> {
	if (updateChecks.has(policyId)) return;
	const t = o.toast ?? appToast;
	updateChecks.set(policyId, true);
	const finish = (j: Job) => {
		updateChecks.set(policyId, false);
		for (const key of UPDATE_STATE_KEYS)
			void o.queryClient?.invalidateQueries({ queryKey: key });
		if (j.state === 'succeeded') t.success(`Checked ${label} for updates`);
		else
			t.error(`The update check for ${label} did not finish.`, {
				body: j.error?.message ?? 'Open the job under Jobs for the details.'
			});
	};
	try {
		const job = await (o.start ?? ((id: string) => checkUpdates(id)))(policyId);
		const w = o.watcher
			? o.watcher(job.id, finish)
			: new JobWatcher(job.id, { onfinish: finish });
		w.start();
	} catch (e) {
		updateChecks.set(policyId, false);
		t.error(`The update check for ${label} could not start.`, { body: errorMessage(e) });
	}
}
