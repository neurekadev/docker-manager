<script lang="ts">
	// An (i) beside a label or control that explains it (#22): the
	// explanation is both the tooltip (hover, keyboard focus) and the
	// accessible name, so it never depends on hovering.
	import Info from '@lucide/svelte/icons/info';
	import Tooltip from './Tooltip.svelte';

	let { text }: { text: string } = $props();
</script>

<Tooltip {text} delay={150}>
	{#snippet trigger(props)}
		<span {...props} class="info-tip" role="img" tabindex="0" aria-label={text}>
			<Info size={14} strokeWidth={1.75} aria-hidden="true" />
		</span>
	{/snippet}
</Tooltip>

<style>
	.info-tip {
		display: inline-flex;
		align-items: center;
		flex: none;
		color: var(--text-muted);
		border-radius: var(--radius-sm);
		cursor: help;
	}

	.info-tip:hover,
	.info-tip:focus-visible {
		color: var(--text-default);
	}
</style>
