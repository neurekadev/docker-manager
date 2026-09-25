<script lang="ts">
	// Filter row above a resource table: search first, then filters, then
	// the count. Wraps on narrow screens (fields go full width).
	import type { Snippet } from 'svelte';

	interface Props {
		label: string;
		children: Snippet;
		/** Right-aligned summary, e.g. "12 of 40 containers". */
		summary?: string;
	}

	let { label, children, summary }: Props = $props();
</script>

<div class="toolbar" role="search" aria-label={label}>
	{@render children()}
	{#if summary}<p class="summary muted num" aria-live="polite">{summary}</p>{/if}
</div>

<style>
	.toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: var(--space-3);
	}

	.toolbar > :global(*) {
		flex: 0 1 200px;
		min-width: 160px;
	}

	.toolbar > :global(:first-child) {
		flex: 1 1 260px;
		max-width: 360px;
	}

	.summary {
		flex: 1 0 auto !important;
		margin-left: auto;
		text-align: right;
		font-size: var(--text-caption);
		line-height: var(--control-height);
	}

	@media (max-width: 767px) {
		.toolbar > :global(*),
		.toolbar > :global(:first-child) {
			flex: 1 1 100%;
			max-width: none;
		}

		.summary {
			text-align: left;
			line-height: var(--leading-caption);
		}
	}
</style>
