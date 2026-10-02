<script lang="ts">
	// Time-series chart (#5, #22): ECharts loaded lazily, themed with the
	// tokens. Nulls are gaps (a break in the line, never zero); runs of
	// missing samples are shaded and listed in text under the chart, so an
	// offline interval is visible without reading the canvas. The canvas is
	// decorative for assistive technology: the figure is labelled with the
	// latest value and range, and the gaps are text.
	import { onDestroy, onMount } from 'svelte';
	import { mountTimeSeries, type TimeSeriesChart, type TimeSeriesOptions } from '$lib/lazy';
	import {
		formatTimeRange,
		formatValue,
		gapIntervals,
		latestValue,
		type ChartLine,
		type ValueUnit
	} from './timeseries';

	interface Props {
		/** Accessible name and visible title, e.g. "CPU". */
		title: string;
		/** Bucket start times (ISO strings), ascending. */
		timestamps: string[];
		lines: ChartLine[];
		unit: ValueUnit;
		/** Range of the x axis (ISO); defaults to the first and last bucket. */
		from?: string;
		to?: string;
		yMax?: number;
		height?: string;
		/** Text after the title, e.g. "of 8 GB". */
		detail?: string;
		/**
		 * Show the first line's latest value after the title. Turn it off
		 * when the legend already names each line's value (received and
		 * sent network traffic), so the value does not show twice.
		 */
		headline?: boolean;
		/**
		 * Stack the lines as filled areas, the first at the bottom (parts of
		 * a whole: used memory, ZFS ARC, cache); the headline stays the
		 * first line's value.
		 */
		stacked?: boolean;
		now?: number;
	}

	let {
		title,
		timestamps,
		lines,
		unit,
		from,
		to,
		yMax,
		height = '180px',
		detail,
		headline = true,
		stacked = false,
		now
	}: Props = $props();

	let el = $state<HTMLElement>();
	let chart: TimeSeriesChart | null = null;
	let observer: ResizeObserver | null = null;
	let destroyed = false;

	const ts = $derived(timestamps.map((t) => Date.parse(t)));
	const start = $derived(from ? Date.parse(from) : (ts[0] ?? 0));
	const end = $derived(to ? Date.parse(to) : (ts[ts.length - 1] ?? 0));
	// Gaps: buckets where the first line (the primary series) has no sample.
	const gaps = $derived(gapIntervals(ts, lines[0]?.values ?? [], end));
	const latest = $derived(latestValue(lines[0]?.values ?? []));
	const empty = $derived(lines.every((l) => latestValue(l.values) === null));
	const summary = $derived(
		empty
			? `${title}: no samples in this range.`
			: `${title}: latest ${formatValue(latest, unit)}.` +
					(gaps.length
						? ` ${gaps.length} ${gaps.length === 1 ? 'gap' : 'gaps'} without samples.`
						: '')
	);

	function options(): TimeSeriesOptions {
		return {
			timestamps: ts,
			lines: lines.map((l) => ({ ...l })),
			format: (v) => formatValue(v, unit),
			from: start,
			to: end,
			yMin: 0,
			yMax,
			gaps,
			stacked
		};
	}

	onMount(() => {
		if (!el) return;
		const target = el;
		mountTimeSeries(target, options())
			.then((c) => {
				if (destroyed) return c.destroy();
				chart = c;
				// Data that arrived while ECharts was loading.
				c.update(options());
				if (typeof ResizeObserver !== 'undefined') {
					observer = new ResizeObserver(() => chart?.resize());
					observer.observe(target);
				}
			})
			.catch(() => {
				// The chart chunk failed to load: the caption, latest value
				// and gap list still describe the data.
			});
	});
	$effect(() => {
		const o = options();
		chart?.update(o);
	});
	onDestroy(() => {
		destroyed = true;
		observer?.disconnect();
		chart?.destroy();
	});
</script>

<figure class="ts" aria-label={title}>
	<figcaption class="caption">
		<span class="title">{title}</span>
		{#if headline}<span class="value num">{formatValue(latest, unit)}</span>{/if}
		{#if detail}<span class="detail">{detail}</span>{/if}
	</figcaption>
	{#if lines.length > 1}
		<ul class="legend" role="list">
			{#each lines as l (l.name)}
				<li>
					<span
						class="key"
						class:dashed={l.dashed}
						style:--key-color={l.color}
						aria-hidden="true"
					></span>{l.name}
					<span class="num">{formatValue(latestValue(l.values), unit)}</span>
				</li>
			{/each}
		</ul>
	{/if}
	<div class="canvas" style:height bind:this={el} aria-hidden="true"></div>
	<p class="sr-only">{summary}</p>
	{#if gaps.length}
		<ul class="gaps" role="list" aria-label="{title}: time without samples">
			{#each gaps.slice(-3) as g (g.from)}
				<li>
					<span class="swatch" aria-hidden="true"></span>No samples {formatTimeRange(
						g,
						now ?? Date.now()
					)}
				</li>
			{/each}
			{#if gaps.length > 3}<li class="more">and {gaps.length - 3} earlier</li>{/if}
		</ul>
	{/if}
</figure>

<style>
	.ts {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		min-width: 0;
		margin: 0;
	}

	.caption {
		display: flex;
		align-items: baseline;
		gap: var(--space-2);
		min-width: 0;
	}

	.title {
		color: var(--text-muted);
		font-weight: var(--weight-medium);
	}

	.value {
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
	}

	.detail {
		color: var(--text-muted);
	}

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-4);
		margin: 0;
		padding: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		list-style: none;
	}

	.legend li {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
	}

	.legend .num {
		color: var(--text-default);
	}

	.key {
		width: 10px;
		height: 3px;
		border-radius: var(--radius-full);
		background: var(--key-color, var(--text-muted));
	}

	.key.dashed {
		width: 12px;
		border-radius: 0;
		background: linear-gradient(
			to right,
			var(--key-color, var(--text-muted)) 0 4px,
			transparent 4px 8px,
			var(--key-color, var(--text-muted)) 8px 12px
		);
	}

	.canvas {
		width: 100%;
		min-width: 0;
	}

	.gaps {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-4);
		margin: 0;
		padding: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		list-style: none;
	}

	.gaps li {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
	}

	.swatch {
		width: 10px;
		height: 10px;
		border: 1px dashed var(--offline);
		border-radius: 2px;
	}
</style>
