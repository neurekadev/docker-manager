<script lang="ts">
	// A build's log (#33): the BuildKit progress lines of the image.build
	// job (its progress messages, with its log and warning lines), streamed
	// from the job's event stream (the server keeps the newest
	// 500 events; replayed when the page opens). Follows the end while the
	// user doesn't scroll up. Credentials never appear here: the agent
	// scrubs the output before it leaves the host.
	import { onDestroy, tick, untrack } from 'svelte';
	import { ApiRequestError } from '$lib/api/client';
	import { JobWatcher } from '$lib/api/jobs.svelte';
	import type { Job } from '$lib/api/client';
	import { Switch, errorMessage, formatDateTime } from '$lib/ui';
	import { clockTime } from './source';

	interface Props {
		jobId: string;
		onfinish?: (job: Job) => void;
		/** Receives the watcher (status, cancel state) for the page. */
		onwatcher?: (w: JobWatcher) => void;
	}

	let { jobId, onfinish, onwatcher }: Props = $props();

	const w = untrack(() => new JobWatcher(jobId, { maxLog: 500, onfinish: (j) => onfinish?.(j) }));
	untrack(() => onwatcher?.(w));
	const stop = w.start();
	onDestroy(stop);

	const lines = $derived([...w.log, ...w.output].sort((a, b) => a.seq - b.seq));

	let follow = $state(true);
	let box = $state<HTMLElement>();
	// Keyed on the newest line, not the count: once the buffer is full every
	// new line drops the oldest and the count stays the same.
	$effect(() => {
		void lines.at(-1)?.seq;
		if (!follow || !box) return;
		void tick().then(() => box && (box.scrollTop = box.scrollHeight));
	});

	function onscroll() {
		if (!box) return;
		const atEnd = box.scrollHeight - box.scrollTop - box.clientHeight < 24;
		if (!atEnd && follow) follow = false;
	}

	const gone = $derived(w.error instanceof ApiRequestError && w.error.status === 404 && !w.job);
</script>

{#if lines.length}
	<div class="log-head">
		<span class="count muted num"
			>{lines.length} lines{w.output.length >= 500 ? ' (the newest 500)' : ''}</span
		>
		<Switch label="Follow" bind:checked={follow} />
	</div>
{/if}
{#if gone}
	<p class="empty muted">This build's log is no longer kept.</p>
{:else if w.error && !w.job}
	<p class="empty muted">The log could not be loaded: {errorMessage(w.error)}</p>
{:else if lines.length === 0}
	<p class="empty muted">
		{w.terminal ? 'This build produced no output.' : 'Waiting for the first lines…'}
	</p>
{:else}
	<!-- A scrollable region must be focusable so keyboard users can scroll it. -->
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div
		class="log mono"
		class:following={follow}
		bind:this={box}
		{onscroll}
		role="log"
		aria-label="Build Log"
		aria-live="off"
		tabindex="0"
	>
		{#each lines as line (line.seq)}
			<div class="line" class:warning={line.warning}>
				<span class="at" title={formatDateTime(line.at)}>{clockTime(line.at)}</span><span
					class="text">{line.message}</span
				>
			</div>
		{/each}
	</div>
{/if}

<style>
	.log-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		margin-bottom: var(--space-3);
	}

	.count {
		font-size: var(--text-caption);
	}

	.log {
		max-height: 480px;
		overflow: auto;
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--code-bg);
		font-size: 12.5px;
		line-height: 20px;
	}

	/* Dropping the oldest lines must not move the view (scroll anchoring),
	   or the scroll reads as the user's and turns Follow off. */
	.log.following {
		overflow-anchor: none;
	}

	.line {
		display: grid;
		grid-template-columns: 76px 1fr;
		gap: var(--space-3);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.at {
		color: var(--text-muted);
	}

	.text {
		color: var(--text-default);
	}

	.warning .text {
		color: var(--warn);
	}

	.empty {
		padding: var(--space-4) 0;
	}
</style>
