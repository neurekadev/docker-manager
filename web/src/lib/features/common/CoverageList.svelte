<script lang="ts" module>
	export interface CoverageItem {
		/** The value stored in the exclusion list. */
		key: string;
		label: string;
		description?: string;
		/**
		 * Left out by something outside the policy (e.g. a label): shown
		 * unchecked and disabled, with this reason behind an (i).
		 */
		locked?: string;
	}
</script>

<script lang="ts">
	// What a policy covers (#10, #20): everything in scope is included by
	// default, so the list shows included items checked and unchecking one
	// adds it to the policy's exclusions. The count says how much is left
	// out; "Include all" clears the exclusions of this list. Locked items
	// are never included and can't be toggled; an (i) says why.
	import { Button, Checkbox, InfoTip } from '$lib/ui';
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
	const includable = $derived(items.some((i) => !i.locked && excluded.includes(i.key)));
</script>

<div class="coverage" role="group" aria-label={label}>
	<div class="head">
		<span class="count num">{count.included} of {count.total} included</span>
		{#if includable}
			<Button
				size="sm"
				variant="ghost"
				onclick={() =>
					onchange(
						setExcluded(
							excluded,
							items.filter((i) => !i.locked).map((i) => i.key),
							false
						)
					)}>Include all</Button
			>
		{/if}
	</div>
	<ChoiceGrid {min}>
		{#each items as item (item.key)}
			{#if item.locked}
				<span class="locked">
					<Checkbox
						label={item.label}
						description={item.description}
						checked={false}
						disabled
					/>
					<InfoTip text={item.locked} />
				</span>
			{:else}
				<Checkbox
					label={item.label}
					description={item.description}
					checked={!excluded.includes(item.key)}
					onchange={(e) =>
						onchange(setExcluded(excluded, [item.key], !e.currentTarget.checked))}
				/>
			{/if}
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

	.locked {
		display: inline-flex;
		align-items: flex-start;
		gap: var(--space-1);
		min-width: 0;
	}

	.locked :global(.info-tip) {
		margin-top: 2px;
	}

	.count {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}
</style>
