<script lang="ts" module>
	export interface CoverageItem {
		/** The value stored in the exclusion list. */
		key: string;
		label: string;
		description?: string;
	}
</script>

<script lang="ts">
	// What a policy covers (#10, #20): everything in scope is included by
	// default, so the list shows included items checked and unchecking one
	// adds it to the policy's exclusions. The count says how much is left
	// out; "Include all" clears the exclusions of this list.
	import { Button, Checkbox } from '$lib/ui';
	import ChoiceGrid from './ChoiceGrid.svelte';
	import { coverageCount, setExcluded } from './coverage';

	let {
		label,
		items,
		excluded,
		onchange,
		min = '220px'
	}: {
		/** Accessible name of the group, e.g. "Stacks on prod". */
		label: string;
		items: CoverageItem[];
		excluded: string[];
		onchange: (excluded: string[]) => void;
		min?: string;
	} = $props();

	const count = $derived(coverageCount(items, excluded));
</script>

<div class="coverage" role="group" aria-label={label}>
	<div class="head">
		<span class="count num">{count.included} of {count.total} included</span>
		{#if count.included < count.total}
			<Button
				size="sm"
				variant="ghost"
				onclick={() =>
					onchange(
						setExcluded(
							excluded,
							items.map((i) => i.key),
							false
						)
					)}>Include all</Button
			>
		{/if}
	</div>
	<ChoiceGrid {min}>
		{#each items as item (item.key)}
			<Checkbox
				label={item.label}
				description={item.description}
				checked={!excluded.includes(item.key)}
				onchange={(e) =>
					onchange(setExcluded(excluded, [item.key], !e.currentTarget.checked))}
			/>
		{/each}
	</ChoiceGrid>
</div>

<style>
	.coverage {
		display: grid;
		gap: var(--space-2);
		min-width: 0;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
		min-height: 28px;
	}

	.count {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}
</style>
