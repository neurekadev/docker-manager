<script lang="ts">
	// Host metrics of one environment (#5): CPU, memory, network, load and
	// disks over a chosen range, explained in plain words (averages on long
	// ranges, shaded gaps); charts with a legend leave out the headline
	// value the legend already shows. The per-container charts follow over
	// the same range (ContainerCharts). Values come from GET …/metrics (downsampled
	// by the manager; nulls are gaps: the agent was offline or a value was
	// unknown) and refresh live through `metrics` events (liveKeys.metrics).
	import { createQuery } from '@tanstack/svelte-query';
	import { environmentMetricsQuery } from '$lib/api/queries';
	import { CHART_COLORS } from '$lib/lazy/palette';
	import { TILE_HEX } from '$lib/design/hue';
	import {
		Card,
		ErrorState,
		Notice,
		Select,
		Skeleton,
		TimeSeriesChart,
		formatBytes
	} from '$lib/ui';
	import ContainerCharts from './ContainerCharts.svelte';
	import { METRIC_RANGES, diskMounts, mountLabel, rangeSeconds, seriesValues } from './model';

	interface Props {
		environmentId: string;
		name: string;
		memoryTotal?: number;
		range?: string;
	}

	let { environmentId, name, memoryTotal, range = $bindable('1h') }: Props = $props();

	const metrics = createQuery(() => environmentMetricsQuery(environmentId, rangeSeconds(range)));
	const m = $derived(metrics.data);
	const mounts = $derived(diskMounts(m));
	const rangeOptions = METRIC_RANGES.map((r) => ({ value: r.id, label: `Last ${r.label}` }));
</script>

<Card title="Metrics" id="metrics">
	{#snippet actions()}
		<div class="range">
			<Select label="Range" hideLabel options={rangeOptions} bind:value={range} />
		</div>
	{/snippet}
	{#if metrics.isError}
		<ErrorState
			error={metrics.error}
			title="The metrics of {name} could not be loaded."
			onretry={() => metrics.refetch()}
			bare
			compact
		/>
	{:else if !m}
		<div class="grid" aria-busy="true">
			{#each [0, 1, 2, 3] as i (i)}<Skeleton height="200px" radius="md" />{/each}
		</div>
	{:else}
		<p class="meta muted">
			{m.resolution === 'raw' ? '' : 'Longer ranges show averages. '}Shaded spans have no
			data: the environment was offline or not reporting.
		</p>
		{#if m.skewCorrected}
			<Notice tone="info" title="Some timestamps were corrected" live="none">
				The host's clock is off, so Docker Manager moved its samples to the right time.
			</Notice>
		{/if}
		<div class="grid">
			<TimeSeriesChart
				title="CPU"
				unit="percent"
				timestamps={m.timestamps}
				from={m.from}
				to={m.to}
				yMax={100}
				detail="of all cores"
				lines={[
					{
						name: 'CPU',
						values: seriesValues(m, 'cpu.percent'),
						color: TILE_HEX.cyan.fg,
						area: true
					}
				]}
			/>
			<TimeSeriesChart
				title="Memory"
				unit="bytes"
				timestamps={m.timestamps}
				from={m.from}
				to={m.to}
				yMax={memoryTotal}
				detail={memoryTotal ? `of ${formatBytes(memoryTotal)}` : undefined}
				lines={[
					{
						name: 'Memory used',
						values: seriesValues(m, 'memory.used_bytes'),
						color: TILE_HEX.indigo.fg,
						area: true
					}
				]}
			/>
			<TimeSeriesChart
				title="Network"
				unit="bytes_per_second"
				headline={false}
				timestamps={m.timestamps}
				from={m.from}
				to={m.to}
				lines={[
					{
						name: 'Received',
						values: seriesValues(m, 'network.rx_bytes_per_second'),
						color: TILE_HEX.green.fg
					},
					{
						name: 'Sent',
						values: seriesValues(m, 'network.tx_bytes_per_second'),
						color: TILE_HEX.violet.fg
					}
				]}
			/>
			<TimeSeriesChart
				title="Load"
				unit="load"
				headline={false}
				timestamps={m.timestamps}
				from={m.from}
				to={m.to}
				lines={[
					{
						name: '1 min',
						values: seriesValues(m, 'load.1'),
						color: CHART_COLORS.series[0]
					},
					{ name: '5 min', values: seriesValues(m, 'load.5'), color: TILE_HEX.slate.fg },
					{ name: '15 min', values: seriesValues(m, 'load.15'), color: TILE_HEX.blue.fg }
				]}
			/>
			{#each mounts as mount (mount)}
				{@const total =
					seriesValues(m, 'disk.total_bytes', mount).findLast((v) => v !== null) ??
					undefined}
				<TimeSeriesChart
					title="Disk: {mountLabel(mount)}"
					unit="bytes"
					timestamps={m.timestamps}
					from={m.from}
					to={m.to}
					yMax={total}
					detail={total ? `of ${formatBytes(total)}` : undefined}
					lines={[
						{
							name: 'Used',
							values: seriesValues(m, 'disk.used_bytes', mount),
							color: TILE_HEX.teal.fg,
							area: true
						}
					]}
				/>
			{/each}
		</div>
	{/if}
	<ContainerCharts {environmentId} {name} seconds={rangeSeconds(range)} />
</Card>

<style>
	.range {
		width: 170px;
	}

	.meta {
		margin-bottom: var(--space-4);
		font-size: var(--text-caption);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
		gap: var(--space-5) var(--space-6);
		margin-top: var(--space-2);
	}
</style>
