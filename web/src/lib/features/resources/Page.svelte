<script lang="ts">
	// Vertical rhythm of a resource page: header, notices, cards, 16 px apart
	// (the canvas shows between cards, #22).
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();
</script>

<div class="page">{@render children()}</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}
</style>
