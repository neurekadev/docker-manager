<script lang="ts">
	// Many items of one type on one chart (#5; every container of an
	// environment): stacked areas, one colour per item, no legend. Hovering
	// lists every shown item with a value at that time, largest first, in its
	// colour; items `shown` leaves out are greyed out behind the others and
	// absent from the tooltip and the headline total. Nulls are gaps (a
	// container that was not running), never zero. The canvas is decorative
	// for assistive technology: the summary names the total and the largest
	// items as text.
	import { onDestroy, onMount } from 'svelte';
	import { mountTimeSeries, type TimeSeriesChart, type TimeSeriesOptions } from '$lib/lazy';
	import { formatValue, type ValueUnit } from './timeseries';
	import { lastIndex, tooltipHtml, tooltipRows, totalAt, type SeriesItem } from './multiseries';

	interface Props {
		/** Accessible name and visible title, e.g. "Docker CPU". */
		title: string;
		/** Bucket start times (ISO strings), ascending. */
		timestamps: string[];
		items: SeriesItem[];
		unit: ValueUnit;
		/** Which items are shown (a name filter); default all. */
		shown?: (name: string) => boolean;
		/** Range of the x axis (ISO); defaults to the first and last bucket. */
		from?: string;
		to?: string;
		height?: string;
		/** Text after the title, e.g. "of all cores". */
		detail?: string;
	}

	let {
		title,
		timestamps,
		items,
		unit,
		shown = () => true,
		from,
		to,
		height = '180px',
		detail
	}: Props = $props();

	let el = $state<HTMLElement>();
	let chart: TimeSeriesChart | null = null;
	let observer: ResizeObserver | null = null;
	let destroyed = false;

	const ts = $derived(timestamps.map((t) => Date.parse(t)));
	const start = $derived(from ? Date.parse(from) : (ts[0] ?? 0));
	const end = $derived(to ? Date.parse(to) : (ts[ts.length - 1] ?? 0));
	const last = $derived(lastIndex(items));
	const total = $derived(last < 0 ? null : totalAt(items, last, shown));
	const summary = $derived.by(() => {
		if (last < 0) return `${title}: no samples in this range.`;
		const top = tooltipRows(items, last, shown)
			.slice(0, 5)
			.map((r) => `${r.name} ${formatValue(r.value, unit)}`);
		return (
			`${title}: latest total ${formatValue(total, unit)}.` +
			(top.length ? ` Largest: ${top.join(', ')}.` : '')
		);
	});

	function options(): TimeSeriesOptions {
		const list = items;
		const show = shown;
		const times = ts;
		// Shown items at the bottom of the stack, greyed ones above them.
		const ordered = [...list.filter((i) => show(i.name)), ...list.filter((i) => !show(i.name))];
		return {
			timestamps: times,
			lines: ordered.map((i) => ({
				name: i.name,
				values: i.values,
				color: i.color,
				muted: !show(i.name)
			})),
			format: (v) => formatValue(v, unit),
			from: start,
			to: end,
			yMin: 0,
			stacked: true,
			tooltip: (index) => tooltipHtml(times[index] ?? 0, tooltipRows(list, index, show), unit)
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
				// The chart chunk failed to load: the caption and summary
				// still describe the data.
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

<figure class="ms" aria-label={title}>
	<figcaption class="caption">
		<span class="title">{title}</span>
		<span class="value num">{formatValue(total, unit)}</span>
		{#if detail}<span class="detail">{detail}</span>{/if}
	</figcaption>
	<div class="canvas" style:height bind:this={el} aria-hidden="true"></div>
	<p class="sr-only">{summary}</p>
</figure>

<style>
	.ms {
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

	.canvas {
		width: 100%;
		min-width: 0;
	}
</style>
