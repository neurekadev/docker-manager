<script lang="ts">
	// Form layout: fields stacked with the form gap, or two columns on wide
	// screens (one below 768 px). Buttons go in FormFooter.
	import type { Snippet } from 'svelte';

	let { columns = 1, children }: { columns?: 1 | 2; children: Snippet } = $props();
</script>

<div class="fields cols-{columns}">
	{@render children()}
</div>

<style>
	.fields {
		display: grid;
		gap: var(--space-4);
		min-width: 0;
	}

	.cols-2 {
		grid-template-columns: repeat(2, minmax(0, 1fr));
		align-items: start;
	}

	@media (max-width: 767px) {
		.cols-2 {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
