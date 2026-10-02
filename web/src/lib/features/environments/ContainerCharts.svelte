<script lang="ts">
	// Per-container charts of an environment (#5), after the host charts:
	// Docker CPU, memory, network and disk I/O with every container the
	// caller may chart stacked in its own colour. Hovering lists every
	// container with a value at that time; the name filter greys out the
	// others and leaves them out of the tooltip and the totals. One request
	// for all containers (GET …/metrics/containers/history), refreshed by
	// `metrics` live events like the host charts. Hidden when the caller may
	// chart no container here (403).
	import { createQuery } from '@tanstack/svelte-query';
	import { containerMetricsHistoryQuery } from '$lib/api/queries';
	import { isDenied } from '$lib/features/common/access';
	import { EmptyState, ErrorState, MultiSeriesChart, Skeleton, TextField } from '$lib/ui';
	import {
		CONTAINER_CHART_SERIES,
		containerChartStep,
		containerCharts,
		nameFilter
	} from './containers';

	interface Props {
		environmentId: string;
		name: string;
		/** The range of the host charts, in seconds. */
		seconds: number;
	}

	let { environmentId, name, seconds }: Props = $props();

	const history = createQuery(() =>
		containerMetricsHistoryQuery(environmentId, seconds, {
			series: CONTAINER_CHART_SERIES,
			stepSeconds: containerChartStep(seconds)
		})
	);
	const h = $derived(history.data);
	const charts = $derived(containerCharts(h));
	let filter = $state('');
	const shown = $derived(nameFilter(filter));
	const matches = $derived(charts.cpu.filter((i) => shown(i.name)).length);
</script>

{#if !isDenied(history.error)}
	<section class="containers" aria-label="Containers">
		<div class="head">
			<div>
				<h3 class="subsection-title">Containers</h3>
				<p class="meta muted">
					Each container in its own colour. Hover to compare them; filter by name to
					highlight some.
				</p>
			</div>
			{#if charts.cpu.length}
				<div class="filter">
					<TextField
						label="Filter Containers"
						hideLabel
						placeholder="Filter by name"
						type="search"
						bind:value={filter}
					/>
				</div>
			{/if}
		</div>
		{#if history.isError}
			<ErrorState
				error={history.error}
				title="The container metrics of {name} could not be loaded."
				onretry={() => history.refetch()}
				bare
				compact
			/>
		{:else if !h}
			<div class="grid" aria-busy="true">
				{#each [0, 1, 2, 3] as i (i)}<Skeleton height="200px" radius="md" />{/each}
			</div>
		{:else if !charts.cpu.length}
			<EmptyState
				title="No container samples in this range."
				description="Running containers show up here once the agent samples them."
				level={3}
				compact
			/>
		{:else}
			{#if !matches}
				<p class="meta muted" role="status">No containers match “{filter.trim()}”.</p>
			{/if}
			<div class="grid">
				<MultiSeriesChart
					title="Docker CPU"
					unit="percent"
					detail="of all cores"
					timestamps={h.timestamps}
					from={h.from}
					to={h.to}
					items={charts.cpu}
					{shown}
				/>
				<MultiSeriesChart
					title="Docker Memory"
					unit="bytes"
					timestamps={h.timestamps}
					from={h.from}
					to={h.to}
					items={charts.memory}
					{shown}
				/>
				<MultiSeriesChart
					title="Docker Network"
					unit="bytes_per_second"
					timestamps={h.timestamps}
					from={h.from}
					to={h.to}
					items={charts.network}
					{shown}
				/>
				<MultiSeriesChart
					title="Docker Disk I/O"
					unit="bytes_per_second"
					timestamps={h.timestamps}
					from={h.from}
					to={h.to}
					items={charts.disk}
					{shown}
				/>
			</div>
		{/if}
	</section>
{/if}

<style>
	.containers {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		margin-top: var(--space-6);
		padding-top: var(--space-5);
		border-top: 1px solid var(--border-subtle);
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		justify-content: space-between;
		gap: var(--space-3);
	}

	.filter {
		width: min(100%, 240px);
	}

	.meta {
		margin: 0;
		font-size: var(--text-caption);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 420px), 1fr));
		gap: var(--space-5) var(--space-6);
		margin-top: var(--space-2);
	}
</style>
