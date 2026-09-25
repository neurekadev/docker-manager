<script lang="ts">
	// Two cards side by side on wide screens (a narrow facts column and a
	// wide table, or two equal halves); stacked below 1024 px.
	import type { Snippet } from 'svelte';

	let {
		ratio = 'narrow-wide',
		children
	}: { ratio?: 'narrow-wide' | 'equal'; children: Snippet } = $props();
</script>

<div class="columns {ratio}">{@render children()}</div>

<style>
	.columns {
		display: grid;
		gap: var(--space-4);
		align-items: start;
		min-width: 0;
	}

	.narrow-wide {
		grid-template-columns: minmax(280px, 1fr) minmax(0, 2fr);
	}

	.equal {
		grid-template-columns: repeat(2, minmax(0, 1fr));
	}

	.columns > :global(*) {
		min-width: 0;
	}

	@media (max-width: 1023px) {
		.narrow-wide,
		.equal {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
