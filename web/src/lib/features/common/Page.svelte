<script lang="ts">
	// Page body of the admin and automation screens: one column of sections
	// with the design's 16 px card gap. `narrow` caps forms and wizards at a
	// readable width (#22 layout rules); lists and tables use the full width.
	import type { Snippet } from 'svelte';

	let { narrow = false, children }: { narrow?: boolean; children: Snippet } = $props();
</script>

<div class="page" class:narrow>
	{@render children()}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}

	.narrow {
		max-width: 880px;
	}
</style>
