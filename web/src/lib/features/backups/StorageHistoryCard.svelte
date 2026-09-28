<script lang="ts">
	// Storage over time (#10): what the repositories stored (solid, filled)
	// and the same data before compression (dashed), over a chosen range
	// (the last 30 days by default). Every point is the latest measurement
	// of each location at that time, so a flat line means no backup or
	// prune measured anything new. A sentence states the latest figure and
	// the change; the figures are also a table (the chart's text
	// alternative). Refreshes with the other backup data (topic backups).
	import { createQuery } from '@tanstack/svelte-query';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import { TILE_HEX } from '$lib/design/hue';
	import {
		Card,
		EmptyState,
		ErrorState,
		Select,
		Skeleton,
		Table,
		TimeSeriesChart,
		type Column
	} from '$lib/ui';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import { storageHistoryQuery } from './queries';
	import {
		DEFAULT_STORAGE_RANGE,
		STORAGE_RANGES,
		storageRangeLabel,
		storageRows,
		storageSeries,
		storageSummary,
		type StorageRow
	} from './storageHistory';

	interface Props {
		/** Only this environment's data (null: every location). */
		environmentId?: string | null;
		range?: string;
	}

	let { environmentId = null, range = $bindable(DEFAULT_STORAGE_RANGE) }: Props = $props();

	const history = createQuery(() => storageHistoryQuery(range, environmentId));
	const series = $derived(storageSeries(history.data));
	const rangeLabel = $derived(storageRangeLabel(range));
	const rangeOptions = STORAGE_RANGES.map((r) => ({ value: r.id, label: r.label }));
	const columns: Column<StorageRow>[] = [
		{ id: 'when', header: 'Time', stack: 'title' },
		{ id: 'stored', header: 'Stored', numeric: true },
		{ id: 'beforeCompression', header: 'Before compression', numeric: true }
	];
</script>

<Card
	title="Storage over time"
	subtitle="Stored after deduplication and compression, and the same data before compression."
>
	{#snippet actions()}
		<div class="range">
			<Select label="Range" hideLabel options={rangeOptions} bind:value={range} />
		</div>
	{/snippet}
	{#if history.isError}
		<ErrorState
			error={history.error}
			title="The storage history could not be loaded."
			onretry={() => history.refetch()}
			bare
			compact
		/>
	{:else if !history.data}
		<Skeleton height="220px" radius="md" />
	{:else if !series}
		<EmptyState
			icon={HardDrive}
			color="blue"
			title="No history yet."
			description="The chart fills in after the next backup or prune of a repository."
			level={3}
			compact
		/>
	{:else}
		<div class="history">
			<TimeSeriesChart
				title="Storage"
				unit="bytes"
				headline={false}
				detail={rangeLabel}
				height="220px"
				timestamps={series.timestamps}
				from={history.data.from}
				to={history.data.to}
				lines={[
					{
						name: 'Stored',
						values: series.stored,
						color: TILE_HEX.blue.fg,
						area: true
					},
					{
						name: 'Before compression',
						values: series.beforeCompression,
						color: TILE_HEX.slate.fg,
						dashed: true
					}
				]}
			/>
			<p class="summary">{storageSummary(series, rangeLabel)}</p>
			<Disclosure summary="Show the figures as a table">
				<Table
					label="Storage over time"
					rows={storageRows(series)}
					{columns}
					rowKey={(r) => r.at}
					maxHeight="280px"
				/>
			</Disclosure>
		</div>
	{/if}
</Card>

<style>
	.range {
		width: 170px;
	}

	.history {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}

	.summary {
		margin: 0;
		color: var(--text-default);
		font-size: var(--text-caption);
	}
</style>
