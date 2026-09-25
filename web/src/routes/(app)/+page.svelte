<script lang="ts">
	// Dashboard (#22 placeholder for the #5 overview): a Restricted user sees
	// the calm denied state (#17); everyone else sees the environments they
	// can reach with their status and usage (GET /overview). Feature work
	// extends this page; keep it built from $lib/ui components.
	import { createQuery } from '@tanstack/svelte-query';
	import Container from '@lucide/svelte/icons/container';
	import Server from '@lucide/svelte/icons/server';
	import MemoryStick from '@lucide/svelte/icons/memory-stick';
	import type { Schema } from '$lib/api/client';
	import { myPermissionsQuery, overviewQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf, isRestricted } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Card,
		DeniedState,
		EmptyState,
		ErrorState,
		KpiCard,
		Meter,
		Skeleton,
		StatusBadge,
		Table,
		formatBytes,
		formatPercent,
		type Column
	} from '$lib/ui';

	type Row = Schema<'OverviewEnvironment'>;

	usePage({ title: 'Dashboard', crumbs: [{ label: 'Dashboard' }] });

	const perms = createQuery(() => myPermissionsQuery());
	const restricted = $derived(perms.data ? isRestricted(accessOf(perms.data)) : false);
	const overview = createQuery(() => ({
		...overviewQuery(),
		enabled: !!perms.data && !restricted
	}));

	const rows = $derived(
		(overview.data?.environments ?? []).filter(
			(e) => !environmentSelection.id || e.id === environmentSelection.id
		)
	);
	const totals = $derived.by(() => {
		const online = rows.filter((r) => r.online).length;
		const containers = rows.reduce((n, r) => n + Math.max(0, r.docker?.containers ?? 0), 0);
		const running = rows.reduce((n, r) => n + Math.max(0, r.docker?.containersRunning ?? 0), 0);
		const memUsed = rows.reduce((n, r) => n + (r.usage?.memoryUsedBytes ?? 0), 0);
		const memTotal = rows.reduce(
			(n, r) =>
				n + (r.usage?.memoryUsedBytes !== undefined ? (r.usage?.memoryTotalBytes ?? 0) : 0),
			0
		);
		return { online, offline: rows.length - online, containers, running, memUsed, memTotal };
	});

	const columns: Column<Row>[] = [
		{
			id: 'name',
			header: 'Environment',
			cell: nameCell,
			sortValue: (r) => r.name,
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (r) => (r.online ? 0 : 1),
			stack: 'status',
			width: '140px'
		},
		{
			id: 'containers',
			header: 'Containers',
			cell: containersCell,
			sortValue: (r) => r.docker?.containersRunning ?? null,
			numeric: true,
			width: '130px'
		},
		{
			id: 'cpu',
			header: 'CPU',
			cell: cpuCell,
			sortValue: (r) => r.usage?.cpuPercent ?? null,
			numeric: true,
			width: '100px'
		},
		{
			id: 'memory',
			header: 'Memory',
			cell: memoryCell,
			sortValue: (r) => r.usage?.memoryUsedBytes ?? null,
			width: '240px'
		}
	];
</script>

{#snippet nameCell(r: Row)}
	<a class="env-link" href={routes.environment(r.id)}>{r.name}</a>
{/snippet}
{#snippet statusCell(r: Row)}
	<StatusBadge status={r.online ? 'online' : 'offline'} />
{/snippet}
{#snippet containersCell(r: Row)}
	{#if r.docker}
		<span class="num">{r.docker.containersRunning} / {r.docker.containers}</span>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet cpuCell(r: Row)}
	<span class="num">{formatPercent(r.usage?.cpuPercent)}</span>
{/snippet}
{#snippet memoryCell(r: Row)}
	{#if r.usage?.memoryUsedBytes !== undefined && r.usage?.memoryTotalBytes}
		<div class="mem">
			<span class="num"
				>{formatBytes(r.usage.memoryUsedBytes)}
				<span class="muted">/ {formatBytes(r.usage.memoryTotalBytes)}</span></span
			>
			<Meter
				value={r.usage.memoryUsedBytes}
				max={r.usage.memoryTotalBytes}
				label="Memory of {r.name}"
				valueText="{formatBytes(r.usage.memoryUsedBytes)} of {formatBytes(
					r.usage.memoryTotalBytes
				)}"
			/>
		</div>
	{:else}<span class="muted">—</span>{/if}
{/snippet}

{#if restricted}
	<DeniedState level={1} />
{:else}
	<div class="page">
		<header class="head">
			<h1>Dashboard</h1>
			<p class="muted">Every environment you can reach, with what runs on it.</p>
		</header>

		{#if overview.isError}
			<ErrorState
				error={overview.error}
				title="The overview could not be loaded."
				onretry={() => overview.refetch()}
			/>
		{:else}
			<div class="kpis" aria-busy={overview.isPending}>
				{#if overview.isPending}
					{#each [0, 1, 2] as i (i)}<div class="kpi-skeleton">
							<Skeleton height="64px" radius="lg" />
						</div>{/each}
				{:else}
					<KpiCard
						label="Environments online"
						value="{totals.online} / {rows.length}"
						icon={Server}
						color="blue"
						secondary={totals.offline ? `${totals.offline} offline` : 'All connected'}
						tone={totals.offline ? 'warn' : 'ok'}
					/>
					<KpiCard
						label="Containers running"
						value="{totals.running} / {totals.containers}"
						icon={Container}
						color="green"
						secondary="{totals.containers - totals.running} stopped"
					/>
					<KpiCard
						label="Memory in use"
						value={formatBytes(totals.memUsed)}
						unit="/ {formatBytes(totals.memTotal)}"
						icon={MemoryStick}
						color="indigo"
					>
						{#snippet bar()}
							<Meter
								value={totals.memUsed}
								max={totals.memTotal || 1}
								label="Memory in use"
								valueText="{formatBytes(totals.memUsed)} of {formatBytes(
									totals.memTotal
								)}"
							/>
						{/snippet}
					</KpiCard>
				{/if}
			</div>

			<Card title="Environments" padding="none" id="environments">
				{#if overview.isPending}
					<div class="table-skeleton"><Skeleton lines={4} height="20px" /></div>
				{:else}
					<Table
						label="Environments"
						{rows}
						{columns}
						rowKey={(r) => r.id}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							<EmptyState
								icon={Server}
								color="blue"
								title="No environments yet."
								description="Add an environment: run the DockYard agent on a Docker host and enroll it."
								level={3}
								compact
							/>
						{/snippet}
					</Table>
				{/if}
			</Card>
		{/if}
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.head h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.head p {
		margin-top: 2px;
		font-size: var(--text-control);
	}

	.kpis {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
		gap: var(--space-4);
	}

	.kpi-skeleton {
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-lg);
		background: var(--surface-panel);
	}

	.table-skeleton {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.env-link {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.mem {
		display: grid;
		gap: 4px;
	}
</style>
