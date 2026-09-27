<script lang="ts">
	// A live uptime (#22): the time since `since` in the compact form of
	// formatUptime ("5m 03s", "3h 12m 08s", "4d 3h 12m"), ticking once a
	// second through the shared clock. The absolute start time is the
	// title and the machine-readable datetime. Without a start (not running,
	// unknown or not permitted) it shows a muted dash.
	import { clock } from './clock.svelte';
	import { formatDateTime, formatUptime, secondsSince } from './format';

	interface Props {
		/** ISO start time; absent when the thing is not running. */
		since?: string | null;
		/** Text before the absolute time in the title (default "Started"). */
		prefix?: string;
	}

	let { since, prefix = 'Started' }: Props = $props();
	const seconds = $derived(secondsSince(since, clock.now));
</script>

{#if since && seconds !== null}
	<time class="uptime" datetime={since} title="{prefix} {formatDateTime(since)}"
		>{formatUptime(seconds)}</time
	>
{:else}
	<span class="none">—</span>
{/if}

<style>
	.uptime {
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}

	.none {
		color: var(--text-muted);
	}
</style>
