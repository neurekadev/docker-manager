<script lang="ts">
	// Many items of one type on one chart (#5; every container of an
	// environment), drawn like Beszel: smoothed stacked areas in the order
	// of `items` (the first on top), one colour per item, no legend.
	// Hovering lists every shown item with a value at that time, largest
	// first, in its colour, under their total; items `shown` leaves out stay
	// in their place greyed out and are absent from the tooltip and the
	// headline total. Values that do not add up (temperatures, #146) set
	// `stacked={false}`: plain lines side by side, the headline and the
	// summary name the largest value instead of a total, and the tooltip
	// has no total. On phones and touch screens a floating list of every
	// item would not fit the screen: a tap moves the pointer and the details
	// of that moment show under the chart instead. Nulls are gaps (a
	// container that was not running), never zero. The canvas is decorative
	// for assistive technology: the summary names the total and the largest
	// items as text.
	import { onDestroy, onMount } from 'svelte';
	import X from '@lucide/svelte/icons/x';
	import { mountTimeSeries, type TimeSeriesChart, type TimeSeriesOptions } from '$lib/lazy';
	import IconButton from './IconButton.svelte';
	import { formatValue, type ValueUnit } from './timeseries';
	import {
		lastIndex,
		maxAt,
		nearestIndex,
		tipTimeText,
		tooltipHtml,
		tooltipRows,
		totalAt,
		type SeriesItem
	} from './multiseries';

	interface Props {
		/** Accessible name and visible title, e.g. "Docker CPU". */
		title: string;
		/** Bucket start times (ISO strings), ascending. */
		timestamps: string[];
		/** In stacking order, the first on top (e.g. the largest first). */
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
		/**
		 * Stacked areas whose total means something (default); false draws
		 * plain lines for values that do not add up (temperatures) and
		 * shows the largest value instead of a total.
		 */
		stacked?: boolean;
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
		detail,
		stacked = true
	}: Props = $props();

	let el = $state<HTMLElement>();
	let chart: TimeSeriesChart | null = null;
	let observer: ResizeObserver | null = null;
	let destroyed = false;

	/** Phones and touch screens: details under the chart, no floating tooltip. */
	const COMPACT = '(max-width: 640px), (pointer: coarse)';
	let compact = $state(false);
	/** The bucket tapped on a phone (its details show under the chart). */
	let picked = $state<number | null>(null);

	const ts = $derived(timestamps.map((t) => Date.parse(t)));
	const start = $derived(from ? Date.parse(from) : (ts[0] ?? 0));
	const end = $derived(to ? Date.parse(to) : (ts[ts.length - 1] ?? 0));
	// The newest bucket of the shown items: a hidden item's newer sample
	// would leave their total empty.
	const last = $derived(lastIndex(items.filter((i) => shown(i.name))));
	/** The headline: the total of a bucket, or its largest value when unstacked. */
	const headlineAt = (index: number) =>
		stacked ? totalAt(items, index, shown) : maxAt(items, index, shown);
	const total = $derived(last < 0 ? null : headlineAt(last));
	const summary = $derived.by(() => {
		if (last < 0) return `${title}: no samples in this range.`;
		const top = tooltipRows(items, last, shown)
			.slice(0, 5)
			.map((r) => `${r.name} ${formatValue(r.value, unit)}`);
		return (
			`${title}: latest ${stacked ? 'total' : 'highest'} ${formatValue(total, unit)}.` +
			(top.length ? ` ${stacked ? 'Largest' : 'Highest'}: ${top.join(', ')}.` : '')
		);
	});

	function options(): TimeSeriesOptions {
		const list = items;
		const show = shown;
		const times = ts;
		const small = compact;
		const stack = stacked;
		// ECharts stacks (and draws) from the bottom: the last item first,
		// the first on top.
		return {
			timestamps: times,
			lines: [...list].reverse().map((i) => ({
				name: i.name,
				values: i.values,
				color: i.color,
				muted: !show(i.name)
			})),
			format: (v) => formatValue(v, unit),
			from: start,
			to: end,
			yMin: stack ? 0 : undefined,
			stacked: stack,
			hideTooltip: small,
			tooltip: small
				? undefined
				: (index) =>
						tooltipHtml(
							times[index] ?? 0,
							tooltipRows(list, index, show),
							unit,
							stack ? totalAt(list, index, show) : undefined
						),
			onPointer: (t) => {
				if (small) picked = nearestIndex(times, t);
			}
		};
	}

	onMount(() => {
		if (typeof matchMedia !== 'function') return;
		const mq = matchMedia(COMPACT);
		const follow = () => (compact = mq.matches);
		follow();
		mq.addEventListener('change', follow);
		return () => mq.removeEventListener('change', follow);
	});

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
	{#if compact && picked !== null && picked >= 0 && picked < ts.length}
		{@const at = tipTimeText(ts[picked])}
		{@const rows = tooltipRows(items, picked, shown)}
		<div class="details" role="region" aria-label="{title} at {at}">
			<div class="details-head">
				<span class="muted">{at}</span>
				{#if stacked && rows.length > 1}<span
						>Total <b class="num">{formatValue(totalAt(items, picked, shown), unit)}</b
						></span
					>{/if}
				<IconButton icon={X} label="Close" size="sm" onclick={() => (picked = null)} />
			</div>
			{#if rows.length}
				<ul class="rows" role="list">
					{#each rows as r (r.name)}
						<li>
							<span class="dot" style:background={r.color} aria-hidden="true"></span>
							<span class="name">{r.name}</span>
							<span class="num val">{formatValue(r.value, unit)}</span>
							{#if r.parts.length}<span class="parts num"
									>{r.parts
										.map((p) => `${formatValue(p.value, unit)} ${p.label}`)
										.join(', ')}</span
								>{/if}
						</li>
					{/each}
				</ul>
			{:else}
				<p class="muted none">No samples</p>
			{/if}
		</div>
	{/if}
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

	.details {
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
		font-size: var(--text-caption);
	}

	.details-head {
		display: flex;
		align-items: center;
		gap: var(--space-3);
	}

	.details-head .muted {
		flex: 1;
	}

	.muted {
		color: var(--text-muted);
	}

	.rows {
		display: grid;
		grid-template-columns: 8px minmax(0, 1fr) auto;
		align-items: center;
		gap: var(--space-1) var(--space-2);
		max-height: 280px;
		margin: 0;
		padding: 0;
		overflow-y: auto;
		list-style: none;
	}

	.rows li {
		display: contents;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
	}

	.name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.val {
		color: var(--text-strong);
		font-weight: var(--weight-semibold);
		text-align: right;
	}

	.parts {
		grid-column: 2 / -1;
		margin-top: calc(-1 * var(--space-1));
		color: var(--text-muted);
	}

	.none {
		margin: 0;
	}
</style>
