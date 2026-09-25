<script lang="ts">
	// A time-series chart of container metrics (#5, #6): ECharts through
	// $lib/lazy, gaps (null) drawn as breaks, never as zero. The chart is
	// decorative for assistive technology; `summary` states the latest
	// value in text.
	import { onDestroy, onMount } from 'svelte';
	import { mountLineChart, type LineChart, type Series } from '$lib/lazy';

	interface Props {
		/** Accessible name, e.g. "CPU of pihole". */
		label: string;
		series: Series[];
		/** Suffix of axis and tooltip values ("%", " MB"). */
		unit?: string;
		summary?: string;
		height?: string;
	}

	let { label, series, unit = '', summary, height = '180px' }: Props = $props();
	let el = $state<HTMLElement>();
	let chart: LineChart | null = null;
	let ro: ResizeObserver | null = null;

	onMount(() => {
		if (!el) return;
		void mountLineChart(el, series[0]?.name ?? label, series[0]?.points ?? [], unit).then(
			(c) => {
				chart = c;
				c.update(series);
				ro = new ResizeObserver(() => c.resize());
				if (el) ro.observe(el);
			}
		);
	});
	$effect(() => {
		chart?.update(series);
	});
	onDestroy(() => {
		ro?.disconnect();
		chart?.destroy();
	});
</script>

<figure class="chart">
	<div class="canvas" style="height: {height}" bind:this={el} aria-hidden="true"></div>
	<figcaption class="sr-only">{label}{summary ? `: ${summary}` : ''}</figcaption>
</figure>

<style>
	.chart {
		margin: 0;
		min-width: 0;
	}

	.canvas {
		width: 100%;
	}
</style>
