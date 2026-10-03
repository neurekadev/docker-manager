<script lang="ts">
	// An (i) beside a label or control that explains it (#22): the
	// explanation is both the tooltip and the accessible name, so it never
	// depends on hovering. The tooltip is TooltipLayer's info tip
	// (`data-dy-info`): hover, keyboard focus, and a tap on touch screens.
	import Info from '@lucide/svelte/icons/info';

	// `id` lets a control list the tip in its aria-describedby.
	let { text, id }: { text: string; id?: string } = $props();
</script>

<span class="info-tip" {id} role="img" tabindex="0" aria-label={text} title={text} data-dy-info>
	<Info size={14} strokeWidth={1.75} aria-hidden="true" />
</span>

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
