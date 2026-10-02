<script lang="ts">
	// Secondary detail behind a summary line (native <details>: keyboard
	// and screen readers for free): explanations, less common options.
	// `hint` adds an (i) to the summary that explains the group: a themed
	// title tooltip (never a focusable control inside <summary>) that is
	// also part of the summary's accessible name; as an info tip
	// (`data-dy-info`) a tap shows it instead of toggling the group.
	import type { Snippet } from 'svelte';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import Info from '@lucide/svelte/icons/info';

	let {
		summary,
		hint,
		open = false,
		children
	}: { summary: string; hint?: string; open?: boolean; children: Snippet } = $props();
</script>

<details class="disclosure" {open}>
	<summary
		><ChevronRight size={14} aria-hidden="true" class="chev" />{summary}{#if hint}<span
				class="hint"
				role="img"
				aria-label={hint}
				title={hint}
				data-dy-info><Info size={13} strokeWidth={1.75} aria-hidden="true" /></span
			>{/if}</summary
	>
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

	.hint {
		display: inline-flex;
		margin-left: var(--space-1);
		color: var(--text-muted);
		cursor: help;
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
