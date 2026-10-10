<script lang="ts">
	// Host metrics of one environment (#5) in Beszel's order, colours and
	// style (METRIC_COLORS): CPU, I/O wait (#309), memory (used, ZFS ARC and cache stacked),
	// disk usage, disk I/O, network, swap (only when the host has swap),
	// load and the temperature sensors (#146, only when the host reports
	// any in the range) over a chosen range, explained in plain words
	// (averages on long ranges, shaded gaps); charts of rates with a legend
	// leave out the headline value the legend already shows. Charts of
	// values older agents do not send (I/O wait, disk I/O) are left out while the
	// range has none. The per-container charts follow over
	// the same range (ContainerCharts). Values come from GET …/metrics (downsampled
	// by the manager; nulls are gaps: the agent was offline or a value was
	// unknown) and refresh live through `metrics` events (liveKeys.metrics).
	import { createQuery } from '@tanstack/svelte-query';
	import { environmentMetricsQuery } from '$lib/api/queries';
	import { METRIC_COLORS } from '$lib/design/hue';
	import {
		Card,
		ErrorState,
		MultiSeriesChart,
		Notice,
		Select,
		Skeleton,
		TimeSeriesChart,
		formatBytes
	} from '$lib/ui';
	import ContainerCharts from './ContainerCharts.svelte';
	import {
		METRIC_RANGES,
		diskMounts,
		hasValues,
		memoryLines,
		mountLabel,
		rangeSeconds,
		seriesValues,
		swapTotal
	} from './model';
	import { temperatureItems } from './temperatures';

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
	const temperatures = $derived(temperatureItems(m));
	const ioWait = $derived(hasValues(m, 'cpu.iowait_percent'));
	const diskIO = $derived(
		hasValues(m, 'block.read_bytes_per_second') || hasValues(m, 'block.write_bytes_per_second')
	);
	const swap = $derived(swapTotal(m));
	const rangeOptions = METRIC_RANGES.map((r) => ({ value: r.id, label: `Last ${r.label}` }));
</script>

<Card
	title="Metrics"
	id="metrics"
	info="Longer ranges show averages. Shaded spans have no data: the environment was offline or not reporting."
>
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
		{#if m.skewCorrected}
			<div class="skew">
				<Notice tone="info" title="Some timestamps were corrected" live="none">
					The host's clock is off; its samples were moved to the right time.
				</Notice>
			</div>
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
						color: METRIC_COLORS.cpu,
						area: true
					}
				]}
			/>
			{#if ioWait}
				<TimeSeriesChart
					title="I/O Wait"
					unit="percent"
					timestamps={m.timestamps}
					from={m.from}
					to={m.to}
					yMax={100}
					detail="of CPU time"
					lines={[
						{
							name: 'I/O Wait',
							values: seriesValues(m, 'cpu.iowait_percent'),
							color: METRIC_COLORS.ioWait,
							area: true
						}
					]}
				/>
			{/if}
			<TimeSeriesChart
				title="Memory"
				unit="bytes"
				stacked
				timestamps={m.timestamps}
				from={m.from}
				to={m.to}
				yMax={memoryTotal}
				detail={memoryTotal ? `of ${formatBytes(memoryTotal)}` : undefined}
				lines={memoryLines(m)}
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
							color: METRIC_COLORS.disk,
							area: true
						}
					]}
				/>
			{/each}
			{#if diskIO}
				<TimeSeriesChart
					title="Disk I/O"
					unit="bytes_per_second"
					headline={false}
					timestamps={m.timestamps}
					from={m.from}
					to={m.to}
					lines={[
						{
							name: 'Read',
							values: seriesValues(m, 'block.read_bytes_per_second'),
							color: METRIC_COLORS.diskRead,
							area: true,
							fill: 0.3
						},
						{
							name: 'Write',
							values: seriesValues(m, 'block.write_bytes_per_second'),
							color: METRIC_COLORS.diskWrite,
							area: true,
							fill: 0.3
						}
					]}
				/>
			{/if}
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
						color: METRIC_COLORS.networkReceived,
						area: true,
						fill: 0.2
					},
					{
						name: 'Sent',
						values: seriesValues(m, 'network.tx_bytes_per_second'),
						color: METRIC_COLORS.networkSent,
						area: true,
						fill: 0.2
					}
				]}
			/>
			{#if swap}
				<TimeSeriesChart
					title="Swap"
					unit="bytes"
					timestamps={m.timestamps}
					from={m.from}
					to={m.to}
					yMax={swap}
					detail="of {formatBytes(swap)}"
					lines={[
						{
							name: 'Used',
							values: seriesValues(m, 'swap.used_bytes'),
							color: METRIC_COLORS.swap,
							area: true
						}
					]}
				/>
			{/if}
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
						color: METRIC_COLORS.load1
					},
					{
						name: '5 min',
						values: seriesValues(m, 'load.5'),
						color: METRIC_COLORS.load5
					},
					{
						name: '15 min',
						values: seriesValues(m, 'load.15'),
						color: METRIC_COLORS.load15
					}
				]}
			/>
			{#if temperatures.length}
				<MultiSeriesChart
					title="Temperature"
					unit="celsius"
					stacked={false}
					timestamps={m.timestamps}
					from={m.from}
					to={m.to}
					items={temperatures}
					detail="hottest sensor"
				/>
			{/if}
		</div>
	{/if}
	<ContainerCharts {environmentId} {name} seconds={rangeSeconds(range)} />
</Card>

<style>
	.range {
		width: 170px;
	}

	.skew {
		margin-bottom: var(--space-4);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
		gap: var(--space-5) var(--space-6);
		margin-top: var(--space-2);
	}
</style>
