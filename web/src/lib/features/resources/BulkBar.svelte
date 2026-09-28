<script lang="ts" module>
	import type { IconComponent } from '$lib/design/icons';

	export interface BulkAction {
		label: string;
		icon?: IconComponent;
		danger?: boolean;
		onclick: () => void;
	}
</script>

<script lang="ts">
	// The selection bar of a resource list (#22 polish): how many rows are
	// selected, the bulk actions and "Clear selection". Shown above the
	// table while rows are selected; the actions open a confirmation that
	// lists what runs and what is left out.
	import X from '@lucide/svelte/icons/x';
	import { Button } from '$lib/ui';

	interface Props {
		count: number;
		/** "containers". */
		noun: string;
		actions: BulkAction[];
		onclear: () => void;
	}

	let { count, noun, actions, onclear }: Props = $props();
</script>

<div class="bulk" role="region" aria-label="Selected {noun}">
	<span class="count" aria-live="polite">{count} selected</span>
	<div class="actions">
		{#each actions as a (a.label)}
			<Button
				size="sm"
				variant={a.danger ? 'danger-soft' : 'secondary'}
				icon={a.icon}
				onclick={a.onclick}>{a.label}</Button
			>
		{/each}
		<Button size="sm" variant="ghost" icon={X} onclick={onclear}>Clear selection</Button>
	</div>
</div>

<style>
	.bulk {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2) var(--space-4);
		padding: var(--space-2) var(--space-4);
		border-bottom: 1px solid var(--border-subtle);
		background: var(--accent-soft);
	}

	.count {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}
</style>
