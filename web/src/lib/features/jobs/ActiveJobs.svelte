<script lang="ts">
	// The running jobs of a view (docs/internal/web.md, "Job progress after
	// reload"): one JobProgress per job it started or that matches what it
	// shows, found again after a reload or when the user comes back. Ended
	// jobs keep their outcome until dismissed. At most `max` running jobs
	// follow their own event stream (browsers open six connections per host
	// over HTTP/1.1); the others are compact rows fed by the running list.
	import { untrack } from 'svelte';
	import X from '@lucide/svelte/icons/x';
	import type { Job } from '$lib/api/client';
	import { IconButton, JobProgress } from '$lib/ui';
	import { MAX_JOB_STREAMS, streamedIds, type JobMatch, type TrackedEntry } from './active';
	import JobRow from './JobRow.svelte';
	import { jobTitle, type NameOf } from './labels';
	import { useTrackedJobs, type TrackedJobs } from './tracked.svelte';

	interface Props {
		/** The view's tracked jobs (useTrackedJobs)… */
		jobs?: TrackedJobs;
		/** …or what the view shows (the component tracks it itself). */
		filter?: JobMatch | null;
		/** What a job is called here (default: "Restart container web"). */
		titleOf?: (job: Job) => string | undefined;
		/** Names for targets the page knows (stacks, policies). */
		nameOf?: NameOf;
		variant?: 'inline' | 'panel';
		/** Called once per job when it ends. */
		onfinish?: (job: Job) => void;
		/** Running jobs that follow their own stream. */
		max?: number;
		/** Accessible name of the region. */
		label?: string;
	}

	let {
		jobs,
		filter = null,
		titleOf,
		nameOf,
		variant = 'panel',
		onfinish,
		max = MAX_JOB_STREAMS,
		label = 'Running jobs'
	}: Props = $props();

	const own = untrack(() => (jobs ? null : useTrackedJobs(() => filter)));
	const tracked = $derived(jobs ?? own!);
	const entries = $derived(tracked.entries);
	const streamed = $derived(streamedIds(entries, max));

	function titleFor(e: TrackedEntry): string {
		if (e.title) return e.title;
		if (!e.job) return 'Job';
		return titleOf?.(e.job) || jobTitle(e.job, nameOf);
	}

	function finished(job: Job) {
		tracked.markFinished(job);
		onfinish?.(job);
	}
</script>

{#if entries.length}
	<div class="active-jobs {variant}" role="region" aria-label={label}>
		{#each entries as e (e.id)}
			<div class="entry">
				{#if streamed.has(e.id) || !e.job}
					<JobProgress jobId={e.id} title={titleFor(e)} {variant} onfinish={finished} />
				{:else}
					<JobRow job={e.job} title={titleFor(e)} />
				{/if}
				{#if !e.active}
					<span class="dismiss">
						<IconButton
							size="sm"
							label="Dismiss {titleFor(e)}"
							icon={X}
							onclick={() => tracked.dismiss(e.id)}
						/>
					</span>
				{/if}
			</div>
		{/each}
	</div>
{/if}

<style>
	.active-jobs {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
		gap: var(--space-3);
	}

	.active-jobs.inline {
		grid-template-columns: minmax(0, 1fr);
		gap: var(--space-2);
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

	.inline .dismiss {
		top: 0;
	}

	.entry :global(.job.panel .head) {
		padding-right: var(--space-8);
	}

	.inline .entry :global(.job .head) {
		padding-right: var(--space-8);
	}
</style>
