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
	let chart: Sparkline | null = null;

	onMount(() => {
		if (!el) return;
		void mountSparkline(el, values, color).then((c) => (chart = c));
	});
	$effect(() => {
		chart?.update(values);
	});
	onDestroy(() => chart?.destroy());
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
