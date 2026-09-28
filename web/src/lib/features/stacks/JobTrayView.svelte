<script lang="ts">
	// The stack page's running and finished jobs (tray.svelte.ts): one
	// JobProgress panel each (per-item results, partial failures, recovery
	// advice), a toast repeating the action when it ends, and Dismiss. At
	// most MAX_JOB_STREAMS running jobs follow their own event stream; the
	// others are compact rows fed by the running list (`running`).
	import { goto } from '$app/navigation';
	import { useQueryClient } from '@tanstack/svelte-query';
	import X from '@lucide/svelte/icons/x';
	import type { Job } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { IconButton, JobProgress, toast } from '$lib/ui';
	import { streamedIds } from '$lib/features/jobs/active';
	import JobRow from '$lib/features/jobs/JobRow.svelte';
	import { stackKeys } from './queries';
	import { stackJobGuidance } from './rename';
	import type { JobTray, SuccessToast, TrackedJob } from './tray.svelte';

	interface Props {
		tray: JobTray;
		/** The stack's running jobs (activeJobsQuery). */
		running?: readonly Job[];
	}

	let { tray, running = [] }: Props = $props();
	const listed = $derived(new Map(running.map((j) => [j.id, j])));
	const streamed = $derived(
		streamedIds(
			tray.jobs.map((t) => ({
				id: t.id,
				active: !tray.finished.includes(t.id),
				listed: listed.has(t.id)
			}))
		)
	);
	const queryClient = useQueryClient();

	async function successToast(t: TrackedJob, job: Job): Promise<SuccessToast> {
		if (!t.successFor) return { title: t.success };
		try {
			const s = await t.successFor(job);
			return typeof s === 'string' ? { title: s } : s;
		} catch {
			return { title: t.success };
		}
	}

	function finished(t: TrackedJob, job: Job) {
		tray.markFinished(t.id);
		if (t.silent) {
			// reported by the caller
		} else if (job.state === 'succeeded') {
			void successToast(t, job).then((s) =>
				toast.success(s.title, { body: s.body, action: s.action })
			);
		} else if (job.state === 'cancelled') {
			toast.info(`${t.title} was cancelled`);
		} else {
			toast.error(t.failure, {
				body: stackJobGuidance(job.error),
				action: { label: 'Open job', onclick: () => void goto(routes.job(job.id)) }
			});
		}
		// The stack, its services, revisions and image status changed.
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
		t.onfinish?.(job);
	}
</script>

{#if tray.jobs.length}
	<div class="tray" aria-label="Jobs started here" role="region">
		{#each tray.jobs as t (t.id)}
			{@const job = listed.get(t.id)}
			<div class="entry">
				{#if streamed.has(t.id) || !job}
					<JobProgress jobId={t.id} title={t.title} onfinish={(j) => finished(t, j)} />
				{:else}
					<JobRow {job} title={t.title} />
				{/if}
				{#if tray.finished.includes(t.id)}
					<span class="dismiss">
						<IconButton
							size="sm"
							label="Dismiss {t.title}"
							icon={X}
							onclick={() => tray.dismiss(t.id)}
						/>
					</span>
				{/if}
			</div>
		{/each}
	</div>
{/if}

<style>
	.tray {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
		gap: var(--space-3);
	}

	.entry {
		position: relative;
		min-width: 0;
	}

	.dismiss {
		position: absolute;
		top: var(--space-2);
		right: var(--space-2);
	}

	.entry :global(.job.panel .head) {
		padding-right: var(--space-8);
	}
</style>
