<script lang="ts">
	// Secondary detail behind a summary line (native <details>: keyboard
	// and screen readers for free): explanations, less common options.
	import type { Snippet } from 'svelte';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';

	let {
		summary,
		open = false,
		children
	}: { summary: string; open?: boolean; children: Snippet } = $props();
</script>

<details class="disclosure" {open}>
	<summary><ChevronRight size={14} aria-hidden="true" class="chev" />{summary}</summary>
	<div class="body">{@render children()}</div>
</details>

<style>
	summary {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		width: fit-content;
		list-style: none;
		color: var(--accent-text);
		font-size: var(--text-caption);
		cursor: pointer;
		border-radius: var(--radius-sm);
	}

	summary::-webkit-details-marker {
		display: none;
	}

	summary :global(.chev) {
		transition: transform var(--duration-fast) var(--ease-out);
	}

	details[open] > summary :global(.chev) {
		transform: rotate(90deg);
	}

	.body {
		display: grid;
		gap: var(--space-2);
		margin-top: var(--space-2);
	}
</style>
