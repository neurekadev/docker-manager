<script lang="ts">
	// A row of KpiCards (#22 layout rule: repeat(auto-fit, minmax(210px, 1fr))
	// with the 16 px card gap; two compact cards per row on phones).
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
	@media (max-width: 767px) {
		.kpi-row {
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: var(--space-3);
		}
	}
</style>
