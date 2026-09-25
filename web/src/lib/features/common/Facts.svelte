<script lang="ts" module>
	import type { Snippet } from 'svelte';

	export interface Fact {
		label: string;
		/** Plain value, or a snippet for links, badges and digests. */
		value?: string | number | null;
		render?: Snippet;
		mono?: boolean;
		/** Full value on hover. */
		title?: string;
	}
</script>

<script lang="ts">
	// Label/value pairs of a detail card (a <dl>): labels muted, values in
	// the body colour, identifiers in mono. Two columns on wide cards, one
	// on narrow screens.
	let { items, columns = 2 }: { items: Fact[]; columns?: 1 | 2 | 3 } = $props();
</script>

<dl class="facts cols-{columns}">
	{#each items as f (f.label)}
		<div class="fact">
			<dt>{f.label}</dt>
			<dd class:mono={f.mono} title={f.title}>
				{#if f.render}{@render f.render()}{:else if f.value === null || f.value === undefined || f.value === ''}<span
						class="muted">—</span
					>{:else}{f.value}{/if}
			</dd>
		</div>
	{/each}
</dl>

<style>
	.facts {
		display: grid;
		gap: var(--space-3) var(--space-6);
		margin: 0;
	}

	.cols-2 {
		grid-template-columns: repeat(2, minmax(0, 1fr));
	}

	.cols-3 {
		grid-template-columns: repeat(3, minmax(0, 1fr));
	}

	.fact {
		min-width: 0;
	}

	dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	dd {
		margin: 2px 0 0;
		color: var(--text-default);
		overflow-wrap: anywhere;
	}

	@media (max-width: 767px) {
		.cols-2,
		.cols-3 {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
