<script lang="ts">
	// A row of KpiCards (#22 layout rule: repeat(auto-fit, minmax(210px, 1fr))
	// with the 16 px card gap; 2-up and 1-up on narrow screens by itself).
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();
</script>

<div class="kpi-row">{@render children()}</div>

<style>
	.kpi-row {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(210px, 100%), 1fr));
		gap: var(--space-4);
	}
</style>
