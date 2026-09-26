<script lang="ts">
	// The seven prune rules of a policy or of the defaults, in order from the
	// least to the most destructive category; two columns in wide dialogs.
	import RuleEditor from './RuleEditor.svelte';
	import type { CategoryInfo, MaintenanceRule } from './model';

	interface Props {
		rules: MaintenanceRule[];
		info?: CategoryInfo[];
		suggested?: MaintenanceRule[];
		disabled?: boolean;
		columns?: 1 | 2;
		onchange: (index: number, rule: MaintenanceRule) => void;
	}

	let { rules, info, suggested, disabled = false, columns = 1, onchange }: Props = $props();
</script>

<div class="rules" class:two={columns === 2}>
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

	.two {
		grid-template-columns: repeat(2, minmax(0, 1fr));
	}

	@media (max-width: 1023px) {
		.two {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
