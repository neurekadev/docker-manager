<script lang="ts">
	// A row of KpiCards (#22 layout rule: repeat(auto-fit, minmax(210px, 1fr))
	// with the 16 px card gap; two compact cards per row on phones). Cards in
	// a row share its height; on phones an odd last card spans the row, so
	// a row of five never leaves one card beside empty space.
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

	/* Phones: two compact KPI cards per row (KpiCard's compact layout). */
	.kpi-row > :global(*) {
		min-width: 0;
	}

	@media (max-width: 767px) {
		.kpi-row {
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: var(--space-3);
		}

		.kpi-row > :global(:last-child:nth-child(odd)) {
			grid-column: 1 / -1;
		}
	}
</style>
