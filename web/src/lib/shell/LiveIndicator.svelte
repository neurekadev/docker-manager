<script lang="ts">
	// Live-connection indicator (#22 top bar), fed by the #23 live client
	// through `liveStatus`: nothing while live (or signed out), "Connecting…",
	// "Reconnecting…" while the stream retries, "Live updates paused" while
	// it polls. Announced politely.
	import { liveStatus as defaultStatus, type LiveStatus } from '$lib/live/status.svelte';
	import { indicatorText } from './live-banner';

	let { status = defaultStatus }: { status?: Pick<LiveStatus, 'state'> } = $props();
	const text = $derived(indicatorText(status.state));
</script>

<span
	class="live"
	role="status"
	aria-label="Live Updates"
	data-state={status.state}
	title={text || undefined}
>
	{#if text}
		<span class="dot" aria-hidden="true"></span>
		<span class="text">{text}</span>
	{/if}
</span>

<style>
	.live {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		color: var(--text-muted);
		font-size: var(--text-caption);
		white-space: nowrap;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		background: var(--warn);
		animation: breathe 1.6s ease-in-out infinite;
	}

	[data-state='connecting'] .dot {
		background: var(--info);
	}

	[data-state='polling'] .dot {
		background: var(--offline);
		animation: none;
	}

	@keyframes breathe {
		50% {
			opacity: 0.35;
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.dot {
			animation: none;
		}
	}

	/* Phones: the dot alone, so the breadcrumbs keep the top bar; the text
	   stays for assistive technology and the tooltip, and the offline
	   banner says it in words. */
	@media (max-width: 767px) {
		.text {
			position: absolute;
			width: 1px;
			height: 1px;
			overflow: hidden;
			clip: rect(0, 0, 0, 0);
			white-space: nowrap;
		}
	}
</style>
