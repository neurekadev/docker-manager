<script lang="ts">
	// A group of related controls (days of a window, retention rules, a
	// rule's filters): a fieldset with a visible legend and a hint.
	import type { Snippet } from 'svelte';

	let { legend, hint, children }: { legend: string; hint?: string; children: Snippet } = $props();
</script>

<fieldset class="group">
	<legend>{legend}</legend>
	{#if hint}<p class="hint">{hint}</p>{/if}
	<div class="body">{@render children()}</div>
</fieldset>

<style>
	.group {
		margin: 0;
		padding: var(--space-3) var(--space-4) var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		min-width: 0;
	}

	legend {
		padding: 0 var(--space-1);
		color: var(--text-default);
		font-weight: var(--weight-medium);
	}

	.hint {
		margin-bottom: var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.body {
		display: grid;
		gap: var(--space-3);
		min-width: 0;
	}
</style>
