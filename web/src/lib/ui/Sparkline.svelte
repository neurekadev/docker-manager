<script lang="ts">
	// Sparkline (#22 KPI cards): ECharts loaded lazily; null values are gaps.
	// Decorative next to the KPI's exact value, so it is hidden from
	// assistive technology; `label` describes the trend in text.
	import { onDestroy, onMount } from 'svelte';
	import { mountSparkline, type Sparkline } from '$lib/lazy';

	interface Props {
		values: (number | null)[];
		color?: string;
		label?: string;
	}

	let { values, color, label }: Props = $props();
	let el = $state<HTMLElement>();
	// Reactive, so the effect below re-runs once the chart exists.
	let chart = $state.raw<Sparkline | null>(null);
	let destroyed = false;

	onMount(() => {
		if (!el) return;
		mountSparkline(el, values, color)
			.then((c) => {
				if (destroyed) return c.destroy();
				chart = c;
			})
			.catch(() => {
				// The chart chunk failed to load: the KPI's value still shows.
			});
	});
	$effect(() => {
		// Read the values first: the effect must track them even while the
		// chart is still loading, or it never runs again.
		const v = values;
		chart?.update(v);
	});
	onDestroy(() => {
		destroyed = true;
		chart?.destroy();
	});
</script>

<div class="spark" bind:this={el} aria-hidden="true"></div>
{#if label}<span class="sr-only">{label}</span>{/if}

<style>
	.spark {
		width: 100%;
		height: 100%;
		min-height: 32px;
	}
</style>
