<script lang="ts">
	// KPI card (#22 stack overview): tile, label, value (20/600, tabular),
	// secondary line, and optional sparkline or bar slots. `status` renders
	// the value with a status dot (never colour alone: the value is text).
	//
	// The card is a size container: narrow cards (the mockup's six-up row,
	// about 180-230 px each) switch to a compact layout with a smaller tile
	// and value, so six fit in one row on a 1440 px wide screen. Label,
	// value and a text secondary line stay on one line each (cut with an
	// ellipsis, the full text as tooltip), so cards in a row keep one height.
	// With `href` the label is a link whose hit area covers the card (links
	// inside a snippet secondary stay clickable above it).
	import type { Snippet } from 'svelte';
	import type { TileColor } from '$lib/design/hue';
	import type { IconComponent } from '$lib/design/icons';
	import IconTile from './IconTile.svelte';

	interface Props {
		label: string;
		value: string;
		/** Small unit or total after the value, e.g. "/ 8 GB". */
		unit?: string;
		secondary?: string | Snippet;
		icon?: IconComponent;
		color?: TileColor;
		/** ok, warn, danger: colours the value and adds a dot. */
		tone?: 'ok' | 'warn' | 'danger';
		sparkline?: Snippet;
		bar?: Snippet;
		changed?: boolean;
		/** The list behind the figure: the label becomes a link covering the card. */
		href?: string;
		/** Runs before following `href` (e.g. to preset the list's filters). */
		onclick?: (e: MouseEvent) => void;
	}

	let {
		label,
		value,
		unit,
		secondary,
		icon,
		color = 'blue',
		tone,
		sparkline,
		bar,
		changed = false,
		href,
		onclick
	}: Props = $props();
</script>

<div class="kpi-box" class:linked={!!href} role="group" aria-label={label}>
	<div class="kpi">
		{#if icon}<IconTile {icon} {color} size="lg" />{/if}
		<div class="text">
			{#if href}
				<a class="label cover" title={label} {href} {onclick}>{label}</a>
			{:else}
				<span class="label" title={label}>{label}</span>
			{/if}
			<span class="value-row">
				<span
					class="value num {tone ?? ''}"
					data-changed={changed || undefined}
					title={unit ? `${value} ${unit}` : value}
					>{#if tone}<span class="dot {tone}" aria-hidden="true"></span>{/if}{value}</span
				>
				{#if unit}<span class="unit num">{unit}</span>{/if}
			</span>
			{#if typeof secondary === 'string'}
				<span class="secondary" title={secondary}>{secondary}</span>
			{:else if secondary}
				<span class="secondary">{@render secondary()}</span>
			{/if}
			{#if bar}<div class="bar">{@render bar()}</div>{/if}
			{#if sparkline}<div class="spark">{@render sparkline()}</div>{/if}
		</div>
	</div>
</div>

<style>
	.kpi-box {
		container-type: inline-size;
		min-width: 0;
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.kpi-box.linked {
		position: relative;
		transition: border-color var(--duration-fast) var(--ease-out);
	}

	.kpi-box.linked:hover {
		border-color: var(--border-strong);
	}

	/* The label's hit area covers the card; links in the secondary line
	   stay above it. */
	.cover {
		color: var(--text-default);
		text-decoration: none;
	}

	.cover::after {
		content: '';
		position: absolute;
		inset: 0;
		border-radius: var(--radius-lg);
	}

	.cover:focus-visible {
		outline: none;
	}

	.kpi-box.linked:has(.cover:focus-visible) {
		outline: var(--focus-ring);
		outline-offset: 2px;
	}

	.linked .secondary :global(a) {
		position: relative;
		z-index: 1;
	}

	.kpi {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
		height: 100%;
		min-height: 96px;
		padding: var(--space-4);
	}

	.text {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
	}

	.label,
	.secondary {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.label {
		color: var(--text-default);
		font-size: var(--text-body);
		line-height: var(--leading-body);
	}

	/* A unit that does not fit beside the value goes below it rather than
	   cutting the value ("188.84… / 30.3 GB"). */
	.value-row {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		column-gap: 6px;
		min-width: 0;
	}

	.value {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--text-strong);
		font-size: var(--text-kpi);
		line-height: var(--leading-kpi);
		font-weight: var(--weight-semibold);
		white-space: nowrap;
		border-radius: var(--radius-sm);
	}

	.value.ok {
		color: var(--ok);
	}
	.value.warn {
		color: var(--warn);
	}
	.value.danger {
		color: var(--danger);
	}

	.dot {
		display: inline-block;
		vertical-align: 0.05em;
		width: 10px;
		height: 10px;
		border-radius: var(--radius-full);
		/* Room for the ring inside the value's clip box. */
		margin: 0 8px 0 3px;
	}
	/* One dot style for every tone: the colour with its soft ring. */
	.dot.ok {
		background: var(--ok);
		box-shadow: 0 0 0 3px var(--ok-soft);
	}
	.dot.warn {
		background: var(--warn);
		box-shadow: 0 0 0 3px var(--warn-soft);
	}
	.dot.danger {
		background: var(--danger);
		box-shadow: 0 0 0 3px var(--danger-soft);
	}

	.unit {
		flex-shrink: 0;
		color: var(--text-muted);
		font-size: var(--text-body);
		white-space: nowrap;
	}

	.secondary {
		color: var(--text-muted);
		font-size: var(--text-body);
	}

	/* A snippet secondary (links, badges) may hold several inline parts. */
	.secondary > :global(*) {
		vertical-align: baseline;
	}

	.bar {
		margin-top: var(--space-2);
	}

	.spark {
		width: 100%;
		max-width: 160px;
		height: 28px;
		margin-top: 2px;
	}

	/* Compact card: the six-up KPI row of the stack overview. The tile
	   shrinks to 36 px and the value to 18 px; long values are cut with an
	   ellipsis (the full value is the tooltip). */
	@container (max-width: 230px) {
		.kpi {
			gap: 10px;
			padding: 14px;
		}

		.kpi :global(.tile) {
			width: 36px;
			height: 36px;
		}

		.kpi :global(.tile svg) {
			width: 20px;
			height: 20px;
		}

		.value {
			font-size: var(--text-kpi-sm);
			line-height: var(--leading-kpi-sm);
		}

		.label,
		.secondary,
		.unit {
			font-size: var(--text-caption);
			line-height: var(--leading-caption);
		}

		.spark {
			max-width: none;
		}
	}

	/* Narrow card (two per row on a phone): beside the tile the value
	   would keep a few letters ("1…"), so the tile shrinks to the label's
	   line and the value, unit and secondary line take the card's width.
	   Texts wrap rather than being cut: a phone has no tooltip. */
	@container (max-width: 200px) {
		.kpi {
			display: grid;
			grid-template-columns: auto minmax(0, 1fr);
			align-content: center;
			align-items: center;
			gap: 2px var(--space-2);
			padding: var(--space-3);
		}

		.kpi :global(.tile) {
			width: 28px;
			height: 28px;
		}

		.kpi :global(.tile svg) {
			width: 16px;
			height: 16px;
		}

		.text {
			display: contents;
		}

		.label,
		.secondary {
			white-space: normal;
			overflow-wrap: anywhere;
		}

		/* "10 minutes ago" breaks at its spaces on the smallest phones. */
		.value {
			white-space: normal;
		}

		.value-row,
		.secondary,
		.bar,
		.spark {
			grid-column: 1 / -1;
		}

		.value-row {
			margin-top: var(--space-1);
		}
	}
</style>
