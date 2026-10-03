<script lang="ts">
	// Chip (#22): a small pill for a tag or a filter. With `href` it is a
	// link, with `onclick` or `selected` a button (`selected` makes it a
	// toggle: aria-pressed), otherwise a static tag. `count` adds a number
	// after the label; `hue` a colour swatch (e.g. the service colour from
	// $lib/design/hue). `onremove` makes a static tag removable: an ×
	// button after the label (`TagInput`); `invalid` outlines it in danger.
	import X from '@lucide/svelte/icons/x';
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
		/** Swatch colour, e.g. SERVICE_HEX. */
		hue?: string;
		title?: string;
		disabled?: boolean;
		/** A static tag with an × button that calls this. */
		onremove?: () => void;
		/** The × button's accessible name (default "Remove <label>"). */
		removeLabel?: string;
		/** Marks a tag that is refused (danger outline). */
		invalid?: boolean;
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
		disabled = false,
		onremove,
		removeLabel,
		invalid = false
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
	<span class="chip {size}" class:removable={!!onremove} class:invalid {title} {style}
		>{@render content()}{#if onremove}<button
				type="button"
				class="remove"
				aria-label={removeLabel ?? `Remove ${label}`}
				{disabled}
				onclick={(e) => {
					e.stopPropagation();
					onremove();
				}}><X size={size === 'sm' ? 12 : 14} strokeWidth={2} aria-hidden="true" /></button
			>{/if}</span
	>
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

	.chip.invalid {
		border-color: var(--danger);
		color: var(--danger);
	}

	.chip.removable {
		padding-right: 3px;
		gap: 4px;
	}

	.chip.sm.removable {
		padding-right: 2px;
	}

	.remove {
		display: inline-grid;
		flex-shrink: 0;
		place-items: center;
		width: 20px;
		height: 20px;
		padding: 0;
		border: 0;
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--text-muted);
		cursor: pointer;
	}

	.sm .remove {
		width: 18px;
		height: 18px;
	}

	.remove:hover:not(:disabled) {
		background: var(--surface-hover);
		color: var(--text-strong);
	}

	.remove:hover:not(:disabled) :global(svg) {
		color: var(--text-strong);
	}

	.remove:focus-visible {
		outline: var(--focus-ring);
		outline-offset: 0;
	}

	.remove:disabled {
		cursor: not-allowed;
		opacity: 0.55;
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

		/* A larger hit area for the × without a taller chip. */
		.remove {
			position: relative;
		}

		.remove::after {
			content: '';
			position: absolute;
			inset: -10px -8px;
		}
	}
</style>
