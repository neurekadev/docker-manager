<script lang="ts">
	// A row of KpiCards (#22 layout rule: repeat(auto-fit, minmax(210px, 1fr))
	// with the 16 px card gap; two compact cards per row on phones). Cards in
	// a row share its height. A row never leaves one card beside empty
	// space: on phones an odd last card spans the row, and where not all
	// cards fit one row they split evenly (four as two and two, five as
	// three and two, six as three and three), judged by the row's own width.
	import type { Snippet } from 'svelte';

	let { children }: { children: Snippet } = $props();
</script>

<div class="kpi-frame"><div class="kpi-row">{@render children()}</div></div>

<style>
	.kpi-frame {
		container-type: inline-size;
		min-width: 0;
	}

	.kpi-row {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(210px, 100%), 1fr));
		gap: var(--space-4);
	}

	.kpi-row > :global(*) {
		min-width: 0;
	}

	/* Below the width one row of 210 px cards needs: even rows. */
	@container (min-width: 560px) and (max-width: 899px) {
		.kpi-row:has(> :global(:nth-child(4):last-child)) {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}

	@container (min-width: 560px) and (max-width: 1119px) {
		.kpi-row:has(> :global(:nth-child(5):last-child)) {
			grid-template-columns: repeat(6, minmax(0, 1fr));
		}

		.kpi-row:has(> :global(:nth-child(5):last-child)) > :global(*) {
			grid-column: span 2;
		}

		.kpi-row:has(> :global(:nth-child(5):last-child)) > :global(:nth-child(n + 4)) {
			grid-column: span 3;
		}
	}

	@container (min-width: 560px) and (max-width: 1339px) {
		.kpi-row:has(> :global(:nth-child(6):last-child)) {
			grid-template-columns: repeat(3, minmax(0, 1fr));
		}
	}

	/* Phones: two compact KPI cards per row (KpiCard's compact layout). */
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
