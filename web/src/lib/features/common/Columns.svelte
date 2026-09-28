<script lang="ts">
	// Cards side by side on wide screens (a narrow facts column and a wide
	// table, two equal halves or three thirds); stacked below 1024 px. Each
	// card keeps its own height (a one-line card beside a long table is not
	// stretched to the table's height).
	import type { Snippet } from 'svelte';

	let {
		ratio = 'narrow-wide',
		children
	}: { ratio?: 'narrow-wide' | 'equal' | 'thirds'; children: Snippet } = $props();
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

	.thirds {
		grid-template-columns: repeat(3, minmax(0, 1fr));
	}

	.columns > :global(*) {
		min-width: 0;
	}

	@media (max-width: 1023px) {
		.narrow-wide,
		.equal,
		.thirds {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
