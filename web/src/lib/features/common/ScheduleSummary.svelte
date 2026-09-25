<script lang="ts">
	// A saved schedule (#13) at a glance: enabled or off, the cron
	// expression and zone, and the next run (or why it cannot run).
	import Badge from '$lib/ui/Badge.svelte';
	import { formatDateTime } from '$lib/ui/format';

	interface Props {
		cron: string;
		timeZone: string;
		enabled: boolean;
		/** ScheduleRunTime.at or an ISO instant. */
		nextRun?: string | { at: string } | null;
		invalidReason?: string;
		compact?: boolean;
	}

	let { cron, timeZone, enabled, nextRun, invalidReason, compact = false }: Props = $props();
	const next = $derived(typeof nextRun === 'string' ? nextRun : nextRun?.at);
</script>

<span class="schedule" class:compact>
	{#if invalidReason}
		<Badge tone="danger" dot>Invalid</Badge>
	{:else if enabled}
		<Badge tone="ok" dot>On</Badge>
	{:else}
		<Badge tone="neutral" dot>Off</Badge>
	{/if}
	<span class="expr"><span class="mono">{cron}</span> <span class="muted">{timeZone}</span></span>
	{#if invalidReason}
		<span class="note danger">{invalidReason}</span>
	{:else if enabled && next}
		<span class="note num">Next {formatDateTime(next, timeZone)}</span>
	{:else if !compact && !enabled}
		<span class="note muted">Runs only when you start it</span>
	{/if}
</span>

<style>
	.schedule {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-2);
		min-width: 0;
	}

	.expr {
		white-space: nowrap;
	}

	.note {
		font-size: var(--text-caption);
		color: var(--text-muted);
	}

	.compact .note {
		width: 100%;
	}

	.danger {
		color: var(--danger);
	}
</style>
