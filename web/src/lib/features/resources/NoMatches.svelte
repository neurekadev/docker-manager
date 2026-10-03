<script lang="ts">
	// The empty state of a list whose search or filters match nothing
	// (inside ListCard's table), with the same "Clear Filters" action.
	import type { IconComponent } from '$lib/design/icons';
	import { Button, EmptyState } from '$lib/ui';

	interface Props {
		/** Plural noun: "containers". */
		what: string;
		icon: IconComponent;
		onclear: () => void;
		level?: 2 | 3;
	}

	let { what, icon, onclear, level = 3 }: Props = $props();
</script>

<EmptyState {icon} color="slate" title="No {what} match the search and filters." {level} compact>
	{#snippet actions()}
		<Button variant="secondary" onclick={onclear}>Clear Filters</Button>
	{/snippet}
</EmptyState>
