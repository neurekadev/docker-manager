<script lang="ts">
	// The stack page's running jobs (tray.svelte.ts): one JobProgress panel
	// each while it runs. When a job ends it leaves the tray and a toast
	// reports the outcome (a failure keeps its advice and "Open job" until
	// closed). At most MAX_JOB_STREAMS running jobs follow their own event
	// stream; the others are compact rows fed by the running list
	// (`running`).
	import { goto } from '$app/navigation';
	import { useQueryClient } from '@tanstack/svelte-query';
	import type { Job } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { JobProgress, toast } from '$lib/ui';
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
				action: { label: 'Open Job', onclick: () => void goto(routes.job(job.id)) }
			});
		}
		// The stack, its services, revisions and image status changed.
		void queryClient.invalidateQueries({ queryKey: stackKeys.all });
		t.onfinish?.(job);
		// The toast reports the outcome; the panel does not stay behind.
		tray.dismiss(t.id);
	}
</script>

{#if tray.jobs.length}
	<div class="tray" aria-label="Jobs Started Here" role="region">
		{#each tray.jobs as t (t.id)}
			{@const job = listed.get(t.id)}
			<div class="entry">
				{#if streamed.has(t.id) || !job}
					<JobProgress jobId={t.id} title={t.title} onfinish={(j) => finished(t, j)} />
				{:else}
					<JobRow {job} title={t.title} />
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
		min-width: 0;
	}
</style>
