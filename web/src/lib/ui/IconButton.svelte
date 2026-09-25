<script lang="ts">
	// Icon-only button (#22). `label` is required: it is the accessible name
	// and the tooltip, so no icon-only action is ever unlabeled. `pressed`
	// makes it a toggle (aria-pressed).
	import type { HTMLButtonAttributes } from 'svelte/elements';
	import { mergeProps } from 'bits-ui';
	import type { IconComponent } from '$lib/design/icons';
	import Tooltip from './Tooltip.svelte';

	interface Props extends Omit<HTMLButtonAttributes, 'children' | 'aria-label'> {
		label: string;
		icon: IconComponent;
		variant?: 'ghost' | 'secondary' | 'danger-soft';
		size?: 'sm' | 'md';
		pressed?: boolean;
		tooltip?: boolean;
		tooltipSide?: 'top' | 'right' | 'bottom' | 'left';
		badge?: number;
		ref?: HTMLButtonElement | null;
	}

	let {
		label,
		icon: Icon,
		variant = 'ghost',
		size = 'md',
		pressed,
		tooltip = true,
		tooltipSide = 'top',
		badge,
		type = 'button',
		ref = $bindable(null),
		class: cls = '',
		...rest
	}: Props = $props();

	// No tooltip while the menu or popover this button opens is showing
	// (Escape should close that, not a leftover tooltip).
	const popupOpen = $derived(
		(rest as Record<string, unknown>)['aria-expanded'] === true ||
			(rest as Record<string, unknown>)['aria-expanded'] === 'true'
	);
</script>

{#snippet button(props: Record<string, unknown>)}
	<button
		bind:this={ref}
		{...mergeProps(rest, props)}
		{type}
		class="icon-btn {variant} {size} {cls}"
		aria-label={label}
		aria-pressed={pressed}
	>
		<Icon size={size === 'sm' ? 16 : 18} strokeWidth={1.75} aria-hidden="true" />
		{#if badge}
			<span class="badge num" aria-hidden="true">{badge > 99 ? '99+' : badge}</span>
		{/if}
	</button>
{/snippet}

{#if tooltip}
	<Tooltip text={label} side={tooltipSide} trigger={button} disabled={popupOpen} />
{:else}
	{@render button({})}
{/if}

<style>
	.icon-btn {
		position: relative;
		display: inline-grid;
		place-items: center;
		width: var(--control-height);
		height: var(--control-height);
		padding: 0;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		background: transparent;
		color: var(--text-muted);
		transition:
			background-color var(--duration-fast) var(--ease-out),
			color var(--duration-fast) var(--ease-out);
	}

	.sm {
		width: var(--control-height-sm);
		height: var(--control-height-sm);
		border-radius: var(--radius-sm);
	}

	.icon-btn:hover:not(:disabled),
	.icon-btn[aria-pressed='true'],
	.icon-btn[data-state='open'] {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.secondary {
		background: var(--surface-raised);
		border-color: var(--border-strong);
		color: var(--text-default);
	}

	.danger-soft {
		background: var(--danger-soft);
		border-color: var(--danger-border);
		color: var(--danger);
	}

	.icon-btn:disabled {
		opacity: 0.45;
	}

	.badge {
		position: absolute;
		top: 3px;
		right: 2px;
		min-width: 16px;
		height: 16px;
		padding: 0 4px;
		border-radius: var(--radius-full);
		background: var(--accent);
		color: var(--text-on-accent);
		font-size: 10px;
		line-height: 16px;
		font-weight: var(--weight-semibold);
	}

	@media (pointer: coarse) {
		.icon-btn {
			min-width: var(--touch-target);
			min-height: var(--touch-target);
		}
	}
</style>
