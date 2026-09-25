<script lang="ts">
	// The seven prune rules of a policy or of the defaults, in order from the
	// least to the most destructive category.
	import RuleEditor from './RuleEditor.svelte';
	import type { CategoryInfo, MaintenanceRule } from './model';

	interface Props {
		rules: MaintenanceRule[];
		info?: CategoryInfo[];
		suggested?: MaintenanceRule[];
		disabled?: boolean;
		onchange: (index: number, rule: MaintenanceRule) => void;
	}

	let { rules, info, suggested, disabled = false, onchange }: Props = $props();
</script>

<div class="rules">
	{#each rules as r, i (r.category)}
		<RuleEditor
			rule={r}
			{info}
			{disabled}
			suggested={suggested?.find((d) => d.category === r.category)}
			onchange={(nr) => onchange(i, nr)}
		/>
	{/each}
</div>

<style>
	.rules {
		display: grid;
		gap: var(--space-3);
	}
</style>
