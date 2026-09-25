<script lang="ts">
	// KPI card (#22 stack overview): tile, label, value (20/600, tabular),
	// secondary line, and optional sparkline or bar slots. `status` renders
	// the value with a status dot (never colour alone: the value is text).
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
		changed = false
	}: Props = $props();
</script>

<div class="kpi" role="group" aria-label={label}>
	{#if icon}<IconTile {icon} {color} size="lg" />{/if}
	<div class="text">
		<span class="label">{label}</span>
		<span class="value-row">
			{#if tone}<span class="dot {tone}" aria-hidden="true"></span>{/if}
			<span class="value num {tone ?? ''}" data-changed={changed || undefined}>{value}</span>
			{#if unit}<span class="unit num">{unit}</span>{/if}
		</span>
		{#if typeof secondary === 'string'}
			<span class="secondary">{secondary}</span>
		{:else if secondary}
			<span class="secondary">{@render secondary()}</span>
		{/if}
		{#if bar}<div class="bar">{@render bar()}</div>{/if}
		{#if sparkline}<div class="spark">{@render sparkline()}</div>{/if}
	</div>
</div>

<style>
	.kpi {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
		min-height: 96px;
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.text {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
	}

	.label {
		color: var(--text-default);
		font-size: var(--text-body);
		line-height: var(--leading-body);
	}

	.value-row {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}

	.value {
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
		width: 10px;
		height: 10px;
		border-radius: var(--radius-full);
		margin-right: 2px;
	}
	.dot.ok {
		background: var(--ok);
		box-shadow: 0 0 0 3px var(--ok-soft);
	}
	.dot.warn {
		background: var(--warn);
	}
	.dot.danger {
		background: var(--danger);
	}

	.unit {
		color: var(--text-muted);
		font-size: var(--text-body);
	}

	.secondary {
		color: var(--text-muted);
		font-size: var(--text-body);
		overflow-wrap: anywhere;
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
</style>
