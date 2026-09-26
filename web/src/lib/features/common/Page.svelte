<script lang="ts">
	// Page body of the admin and automation screens: one column of sections
	// with the design's 16 px card gap. `narrow` caps long single-purpose
	// flows (restore, token creation) at a readable width (#22 layout
	// rules); lists, tables and detail pages use the full width. Create and
	// edit forms open as dialogs instead of narrow pages.
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
		max-width: 1120px;
	}
</style>
