<script lang="ts">
	// The stack page's running and finished jobs (tray.svelte.ts): one
	// JobProgress panel each (per-item results, partial failures, recovery
	// advice), a toast repeating the action when it ends, and Dismiss.
	import { goto } from '$app/navigation';
	import { useQueryClient } from '@tanstack/svelte-query';
	import X from '@lucide/svelte/icons/x';
	import type { Job } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { IconButton, JobProgress, toast } from '$lib/ui';
	import { stackKeys } from './queries';
	import type { JobTray, TrackedJob } from './tray.svelte';

	interface Props {
		tray: JobTray;
	}

	let { tray }: Props = $props();
	const queryClient = useQueryClient();

	function finished(t: TrackedJob, job: Job) {
		tray.markFinished(t.id);
		if (t.silent) {
			// reported by the caller
		} else if (job.state === 'succeeded') {
			toast.success(t.success);
		} else if (job.state === 'cancelled') {
			toast.info(`${t.title} was cancelled`);
		} else {
			toast.error(t.failure, {
				body: job.error?.recovery ?? job.error?.message,
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
			<div class="entry">
				<JobProgress jobId={t.id} title={t.title} onfinish={(j) => finished(t, j)} />
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
