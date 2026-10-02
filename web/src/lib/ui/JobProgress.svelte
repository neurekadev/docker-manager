<script lang="ts">
	// Job progress (#22, #26): follows a job's event stream (JobWatcher) and
	// shows its state, progress, current step and per-item results. A partial
	// failure lists the failed items with their messages and the job's
	// recovery advice. `inline` is one line for tables and headers; `panel`
	// is the full view. Completion is announced in a polite live region and,
	// for jobs it follows itself, added to the notices bell (#25 Q6).
	import { onDestroy, untrack } from 'svelte';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleMinus from '@lucide/svelte/icons/circle-minus';
	import CircleX from '@lucide/svelte/icons/circle-x';
	import { JobWatcher, type JobWatcherOptions } from '$lib/api/jobs.svelte';
	import type { Job } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { notices as appNotices, type Notices } from '$lib/shell/notices.svelte';
	import { formatPercent, titleCase } from './format';
	import StatusBadge from './StatusBadge.svelte';
	import { statusInfo } from './status';
	import { errorMessage } from './errors';

	interface Props {
		/** Follow this job (creates a watcher)… */
		jobId?: string;
		/** …or render a watcher owned by the caller. */
		watcher?: JobWatcher;
		/** What the job does, e.g. "Deploy Silo". Defaults to the job kind. */
		title?: string;
		variant?: 'inline' | 'panel';
		onfinish?: (job: Job) => void;
		options?: Omit<JobWatcherOptions, 'onfinish'>;
		/** Where the finished job is announced (null: nowhere). */
		notices?: Notices | null;
		/**
		 * Show the title and state line and the error (default). The job page
		 * shows them itself and turns them off, so they appear once.
		 */
		summary?: boolean;
	}

	let {
		jobId,
		watcher,
		title,
		variant = 'panel',
		onfinish,
		options,
		notices = appNotices,
		summary = true
	}: Props = $props();

	const TONES: Record<string, 'ok' | 'warn' | 'danger' | 'info'> = {
		succeeded: 'ok',
		partial: 'warn',
		failed: 'danger',
		interrupted: 'danger',
		cancelled: 'info'
	};

	function finished(j: Job) {
		notices?.push({
			key: `job:${j.id}`,
			kind: 'job',
			tone: TONES[j.state] ?? 'info',
			title: `${title ?? kindLabel(j.kind)}: ${statusInfo(j.state, 'job').label.toLowerCase()}`,
			body: j.state === 'succeeded' ? undefined : j.error?.recovery,
			href: routes.job(j.id)
		});
		onfinish?.(j);
	}

	// The watcher is created once for the given jobId (keyed by the parent).
	const own = untrack(() =>
		!watcher && jobId ? new JobWatcher(jobId, { ...options, onfinish: finished }) : null
	);
	const w = $derived(watcher ?? own);
	if (own) {
		const stop = own.start();
		onDestroy(stop);
	}

	const job = $derived(w?.job ?? null);
	const state = $derived(job?.state ?? 'queued');
	const label = $derived(title ?? kindLabel(job?.kind));
	const percent = $derived(job?.progress?.percent);
	const failed = $derived(w?.failedItems ?? []);
	const items = $derived(w?.items ?? []);
	const succeeded = $derived(items.filter((i) => i.status === 'succeeded').length);
	const announcement = $derived(
		w?.terminal ? `${label}: ${statusInfo(state, 'job').label.toLowerCase()}` : ''
	);

	function kindLabel(kind?: string): string {
		if (!kind) return 'Job';
		return titleCase(kind.replaceAll('.', ' ').replaceAll('_', ' '));
	}
</script>

<div
	class="job {variant}"
	class:bare={!summary && w?.terminal && !items.length && !w?.log.length}
	aria-busy={!w?.terminal}
>
	{#if summary}
		<div class="head">
			<span class="title">{label}</span>
			<StatusBadge status={state} kind="job" />
		</div>
	{/if}

	{#if !w?.terminal}
		<div
			class="bar"
			role="progressbar"
			aria-label="{label} progress"
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
		{#if job?.progress?.step || job?.progress?.message}
			<p class="step">{job?.progress?.message ?? job?.progress?.step}</p>
		{/if}
		{#if job?.blockedBy}
			<p class="step">
				{job.blockedBy.reason === 'agent_offline'
					? 'Waiting for the environment to come back online.'
					: job.blockedBy.reason === 'lock'
						? 'Waiting for another job on the same resources to finish.'
						: 'Waiting for a free slot.'}
			</p>
		{/if}
	{/if}

	{#if variant === 'panel'}
		{#if summary && job?.error && w?.terminal}
			<div class="outcome {state}">
				<p class="message">{job.error.message}</p>
				{#if job.error.recovery}<p class="recovery">{job.error.recovery}</p>{/if}
			</div>
		{/if}
		{#if items.length}
			<p class="summary num">
				{#if failed.length}
					{failed.length} of {items.length} items failed, {succeeded} succeeded
				{:else}
					{succeeded} of {items.length} items done
				{/if}
			</p>
			<ul class="items" role="list" aria-label="Items">
				{#each items as item (item.name)}
					<li class={item.status}>
						{#if item.status === 'succeeded'}
							<CircleCheck size={16} strokeWidth={1.75} aria-hidden="true" />
						{:else if item.status === 'failed'}
							<CircleX size={16} strokeWidth={1.75} aria-hidden="true" />
						{:else}
							<CircleMinus size={16} strokeWidth={1.75} aria-hidden="true" />
						{/if}
						<span class="name mono">{item.name}</span>
						<span class="sr-only">{statusInfo(item.status, 'job').label}</span>
						{#if item.message}<span class="item-msg">{item.message}</span>{/if}
					</li>
				{/each}
			</ul>
		{/if}
		{#if w?.log.length}
			<ul class="log mono" role="list" aria-label="Messages">
				{#each w.log.slice(-8) as line (line.seq)}
					<li class:warning={line.warning}>{line.message}</li>
				{/each}
			</ul>
		{/if}
		{#if w?.error && !job}
			<p class="load-error">{errorMessage(w.error)}</p>
		{/if}
	{/if}
	<span class="sr-only" role="status">{announcement}</span>
</div>

<style>
	.job {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
	}

	.panel {
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.panel.bare {
		display: contents;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.bar {
		position: relative;
		height: 4px;
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
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.outcome {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
	}

	.outcome.failed,
	.outcome.interrupted {
		border-color: var(--danger-border);
		background: var(--danger-soft);
	}

	.outcome.partial {
		border-color: var(--warn-border);
		background: var(--warn-soft);
	}

	.message {
		color: var(--text-strong);
	}

	.recovery {
		margin-top: 2px;
		color: var(--text-default);
	}

	.summary {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.items {
		display: grid;
		gap: 2px;
		margin: 0;
	}

	.items li {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-height: 28px;
		color: var(--text-default);
	}

	.items li.succeeded {
		color: var(--ok);
	}
	.items li.failed {
		color: var(--danger);
	}
	.items li.skipped {
		color: var(--text-muted);
	}

	.name {
		color: var(--text-strong);
	}

	.item-msg {
		min-width: 0;
		color: var(--text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.log {
		display: grid;
		gap: 2px;
		margin: 0;
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-sm);
		background: var(--code-bg);
		color: var(--text-muted);
	}

	.log .warning {
		color: var(--warn);
	}

	.load-error {
		color: var(--danger);
	}

	@media (prefers-reduced-motion: reduce) {
		.fill.indeterminate {
			animation: none;
		}
	}
</style>
