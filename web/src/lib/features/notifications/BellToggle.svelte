<script lang="ts">
	// One bell of "What to Send" (#142): a toggle button (aria-pressed;
	// mixed for a kind with only some outcomes on). An empty bell is not
	// sent, a filled one is, a half-filled one is some of a kind's outcomes.
	// The label names the event and the channel; the title adds the state
	// on hover. `dim`: the channel is off (its bells still change).
	import Bell from '@lucide/svelte/icons/bell';
	import type { BellState } from './model';

	interface Props {
		value: BellState;
		label: string;
		dim?: boolean;
		onclick: () => void;
	}

	let { value, label, dim = false, onclick }: Props = $props();

	const word = $derived(value === 'on' ? 'On' : value === 'some' ? 'Some' : 'Off');
</script>

<button
	type="button"
	class="bell {value}"
	class:dim
	aria-label={label}
	aria-pressed={value === 'on' ? 'true' : value === 'some' ? 'mixed' : 'false'}
	title="{label}: {word}"
	{onclick}
>
	<Bell size={18} strokeWidth={1.75} aria-hidden="true" />
</button>

<style>
	.bell {
		display: inline-grid;
		place-items: center;
		width: 32px;
		height: 32px;
		padding: 0;
		border: 0;
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--text-muted);
		cursor: pointer;
		transition:
			background-color var(--duration-fast) var(--ease-out),
			color var(--duration-fast) var(--ease-out);
	}

	.bell:hover {
		background: var(--surface-hover);
		color: var(--text-default);
	}

	.bell:focus-visible {
		outline: var(--focus-ring);
		outline-offset: 1px;
	}

	.bell.on,
	.bell.some {
		color: var(--accent-text);
	}

	.bell.on :global(svg) {
		fill: currentColor;
	}

	.bell.some :global(svg) {
		fill: currentColor;
		fill-opacity: 0.35;
	}

	.dim {
		opacity: 0.55;
	}

	@media (pointer: coarse) {
		.bell {
			width: 40px;
			height: 40px;
		}
	}
</style>
