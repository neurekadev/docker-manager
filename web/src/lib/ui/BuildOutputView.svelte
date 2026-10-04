<script lang="ts">
	// A build job's output in a read-only terminal (#264): the BuildKit steps
	// as "=> [2/5] RUN make" headings with their output below, written as the
	// job's events arrive (OutputFormatter), and the job's state and step
	// above it. xterm.js keeps the end in view unless the user scrolls up.
	import { onDestroy, untrack } from 'svelte';
	import { JobWatcher } from '$lib/api/jobs.svelte';
	import type { TerminalHandle } from '$lib/lazy';
	import { buildProgress, OutputFormatter, stepText } from './build-progress';
	import StatusBadge from './StatusBadge.svelte';
	import TerminalView from './TerminalView.svelte';

	let { jobId, label }: { jobId: string; label: string } = $props();

	const w = untrack(() => new JobWatcher(jobId));
	onDestroy(w.start());

	let terminal = $state<TerminalHandle | null>(null);
	const formatter = new OutputFormatter();
	// The newest event written (events replayed after a reconnect are skipped).
	let written = 0;
	$effect(() => {
		const t = terminal;
		const lines = w.output;
		if (!t) return;
		for (const line of lines) {
			if (line.seq <= written) continue;
			t.write(formatter.format(line.message));
			written = line.seq;
		}
	});

	const progress = $derived(buildProgress(w.output.map((l) => l.message)));
</script>

<div class="status">
	<StatusBadge status={w.state} kind="job" />
	{#if progress.step && !w.terminal}<span class="step">{stepText(progress.step)}</span>{/if}
	{#if w.terminal && !w.output.length}<span class="step">This job reported no build output.</span
		>{/if}
</div>
<div class="screen">
	<TerminalView {label} readOnly fit bind:terminal />
</div>

<style>
	.status {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
		margin-bottom: var(--space-3);
	}

	.step {
		min-width: 0;
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.screen {
		height: min(60vh, 560px);
	}
</style>
