<script lang="ts">
	// A wrapping grid of checkboxes or switches (services of a stack, days
	// of a window, volumes of a policy). `min` is the narrowest column.
	import type { Snippet } from 'svelte';

	let { min = '220px', children }: { min?: string; children: Snippet } = $props();
</script>

<div class="choices" style:--choice-min={min}>{@render children()}</div>

<style>
	.choices {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(var(--choice-min), 100%), 1fr));
		gap: var(--space-3) var(--space-4);
		min-width: 0;
	}
</style>
