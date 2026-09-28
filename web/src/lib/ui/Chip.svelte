<script lang="ts">
	// Chip (#22): a small pill for a tag or a filter. With `href` it is a
	// link, with `onclick` or `selected` a button (`selected` makes it a
	// toggle: aria-pressed), otherwise a static tag. `count` adds a number
	// after the label; `hue` a colour swatch (the service hue from
	// $lib/design/hue, so a service keeps its colour in filter chips).
	import type { IconComponent } from '$lib/design/icons';

	interface Props {
		label: string;
		/** Toggle state (aria-pressed); leave undefined for a plain chip. */
		selected?: boolean;
		onclick?: (e: MouseEvent) => void;
		href?: string;
		count?: number;
		size?: 'sm' | 'md';
		icon?: IconComponent;
		/** Swatch colour, e.g. serviceSeriesColor(stackId, service). */
		hue?: string;
		title?: string;
		disabled?: boolean;
	}

	let {
		label,
		selected,
		onclick,
		href,
		count,
		size = 'md',
		icon: Icon,
		hue,
		title,
		disabled = false
	}: Props = $props();

	const style = $derived(hue ? `--chip-hue: ${hue}` : undefined);
</script>

{#snippet content()}
	{#if hue}<span class="swatch" aria-hidden="true"></span>{/if}
	{#if Icon}<Icon size={14} strokeWidth={1.75} aria-hidden="true" />{/if}
	<span class="label">{label}</span>
	{#if count !== undefined}<span class="count num">{count}</span>{/if}
{/snippet}

{#if href && !disabled}
	<a
		class="chip {size}"
		class:selected
		{href}
		{title}
		{style}
		aria-current={selected || undefined}>{@render content()}</a
	>
{:else if onclick || selected !== undefined}
	<button
		type="button"
		class="chip interactive {size}"
		class:selected
		aria-pressed={selected}
		{onclick}
		{title}
		{style}
		{disabled}>{@render content()}</button
	>
{:else}
	<span class="chip {size}" {title} {style}>{@render content()}</span>
{/if}

<style>
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		max-width: 100%;
		height: 28px;
		padding: 0 var(--space-3);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--text-default);
		font: inherit;
		font-size: var(--text-body);
		line-height: var(--leading-body);
		font-weight: var(--weight-medium);
		text-decoration: none;
		white-space: nowrap;
		transition:
			background-color var(--duration-fast) var(--ease-out),
			border-color var(--duration-fast) var(--ease-out),
			color var(--duration-fast) var(--ease-out);
	}

	.chip.sm {
		height: 24px;
		padding: 0 var(--space-2);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	a.chip,
	.chip.interactive {
		cursor: pointer;
	}

	a.chip:hover,
	.chip.interactive:hover:not(:disabled) {
		border-color: var(--text-faint);
		background: var(--surface-hover);
		color: var(--text-strong);
		text-decoration: none;
	}

	.chip.selected {
		border-color: color-mix(in srgb, var(--chip-hue, var(--accent-text)) 55%, transparent);
		background: color-mix(in srgb, var(--chip-hue, var(--accent-text)) 14%, transparent);
		color: var(--text-strong);
	}

	.chip:disabled {
		opacity: 0.55;
		cursor: not-allowed;
	}

	.chip :global(svg) {
		color: var(--text-muted);
	}

	.label {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.count {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.swatch {
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		background: var(--chip-hue);
	}

	/* A toggle chip that is off shows its hue as a ring only. */
	.chip[aria-pressed='false'] .swatch {
		background: transparent;
		box-shadow: inset 0 0 0 1.5px var(--chip-hue);
	}

	@media (pointer: coarse) {
		a.chip,
		.chip.interactive {
			height: auto;
			min-height: var(--touch-target);
		}
	}
</style>
