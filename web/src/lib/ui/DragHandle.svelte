<script lang="ts">
	// Grip handle of a reorderable row (#22, sortable.svelte.ts): the only
	// place a drag starts (mouse, pen or touch); focused, ArrowUp/ArrowDown
	// move the row one place and Home/End to the ends. `label` is the
	// accessible name ("Reorder Link 2"), `name` the row in the announcement
	// ("Moved Link 2 to position 1 of 3.").
	import GripVertical from '@lucide/svelte/icons/grip-vertical';
	import type { Sortable } from './sortable.svelte';

	interface Props {
		sortable: Sortable;
		index: number;
		/** The row as announced, e.g. "Link 2". */
		name: string;
		/** Accessible name; default "Reorder <name>". */
		label?: string;
		disabled?: boolean;
	}

	let { sortable, index, name, label, disabled = false }: Props = $props();
</script>

<button
	type="button"
	class="handle"
	class:active={sortable.dragging === index}
	aria-label={label ?? `Reorder ${name}`}
	aria-keyshortcuts="ArrowUp ArrowDown Home End"
	{disabled}
	{@attach sortable.handle(index, name)}
>
	<GripVertical size={16} strokeWidth={1.75} aria-hidden="true" />
</button>

<style>
	.handle {
		display: inline-grid;
		flex-shrink: 0;
		place-items: center;
		width: var(--control-height-sm);
		height: var(--control-height);
		padding: 0;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--text-muted);
		cursor: grab;
		/* The pointer drags the row instead of scrolling the page. */
		touch-action: none;
		transition:
			background-color var(--duration-fast) var(--ease-out),
			color var(--duration-fast) var(--ease-out);
	}

	.handle:hover:not(:disabled),
	.handle.active {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.handle.active {
		cursor: grabbing;
	}

	.handle:focus-visible {
		outline: var(--focus-ring);
		outline-offset: 0;
	}

	.handle:disabled {
		opacity: 0.45;
		cursor: not-allowed;
	}

	@media (pointer: coarse) {
		.handle {
			min-width: var(--touch-target);
			min-height: var(--touch-target);
		}
	}
</style>
