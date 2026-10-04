<script lang="ts">
	// A running job in one line, fed by the running-jobs list (no event
	// stream of its own; docs/internal/web.md, "Job progress after reload"):
	// its name linked to the job page, its state and a thin progress bar.
	// Views show it for running jobs beyond MAX_JOB_STREAMS.
	import type { Job } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { StatusBadge, formatPercent, progressText } from '$lib/ui';

	interface Props {
		job: Pick<Job, 'id' | 'state' | 'progress'>;
		title: string;
	}

	let { job, title }: Props = $props();
	const percent = $derived(job.progress?.percent);
	// A build's step in words, never a block of its output.
	const step = $derived(progressText(job.progress?.message || job.progress?.step));
</script>

<div class="row" aria-busy="true">
	<div class="head">
		<a class="title" href={routes.job(job.id)}>{title}</a>
		<StatusBadge status={job.state} kind="job" />
	</div>
	<div
		class="bar"
		role="progressbar"
		aria-label="{title} progress"
		aria-valuemin={0}
		aria-valuemax={100}
		aria-valuenow={percent ?? undefined}
		aria-valuetext={percent === undefined ? 'In progress' : formatPercent(percent)}
	>
		<span
			class="fill"
			class:indeterminate={percent === undefined}
			style="width: {percent ?? 40}%"
		></span>
	</div>
	{#if step}<p class="step">{step}</p>{/if}
</div>

<style>
	.row {
		display: flex;
		flex-direction: column;
		gap: var(--space-1);
		min-width: 0;
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-panel);
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}

	.title {
		min-width: 0;
		overflow: hidden;
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		text-decoration: none;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.title:hover {
		text-decoration: underline;
	}

	.bar {
		position: relative;
		height: 3px;
		overflow: hidden;
		border-radius: var(--radius-full);
		background: var(--surface-raised);
	}

	.fill {
		position: absolute;
		inset: 0 auto 0 0;
		border-radius: inherit;
		background: var(--accent);
		transition: width var(--duration-open) var(--ease-out);
	}

	.fill.indeterminate {
		animation: slide 1.4s ease-in-out infinite;
	}

	@keyframes slide {
		from {
			transform: translateX(-100%);
		}
		to {
			transform: translateX(250%);
		}
	}

	.step {
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	@media (prefers-reduced-motion: reduce) {
		.fill.indeterminate {
			animation: none;
		}
	}
</style>
