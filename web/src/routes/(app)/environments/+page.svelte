<script lang="ts">
	// Environments (#3, #5, #34): every Docker host with a DockYard agent,
	// with status, Engine, agent version and compatibility, containers and
	// usage; pending enrollment tokens; archived environments to re-attach.
	import { createQuery } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import Plus from '@lucide/svelte/icons/plus';
	import Server from '@lucide/svelte/icons/server';
	import type { Environment, Schema } from '$lib/api/client';
	import {
		archivedEnvironmentsQuery,
		environmentsQuery,
		myPermissionsQuery,
		overviewQuery
	} from '$lib/api/queries';
	import EngineVersion from '$lib/features/environments/EngineVersion.svelte';
	import EnrollmentsCard from '$lib/features/environments/EnrollmentsCard.svelte';
	import { COMPATIBILITY, environmentStatus } from '$lib/features/environments/model';
	import { routes } from '$lib/routes';
	import { accessOf, isRestricted } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		DeniedState,
		Card,
		EmptyState,
		ErrorState,
		Meter,
		PageHeader,
		Skeleton,
		StatusBadge,
		Table,
		Tabs,
		formatBytes,
		formatDateTime,
		formatPercent,
		formatRelative,
		type Column
	} from '$lib/ui';

	usePage({ title: 'Environments', crumbs: [{ label: 'Environments' }] });

	type Row = Environment & { overview?: Schema<'OverviewEnvironment'> };

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const canEnroll = $derived(access.owner || access.allowed.has('agent.enroll'));

	const envs = createQuery(() => environmentsQuery());
	const overview = createQuery(() => overviewQuery());
	const archived = createQuery(() => archivedEnvironmentsQuery());

	const byId = $derived(new Map((overview.data?.environments ?? []).map((e) => [e.id, e])));
	const rows = $derived<Row[]>(
		(envs.data ?? []).map((e) => ({ ...e, overview: byId.get(e.id) }))
	);
	const archivedRows = $derived(archived.data ?? []);

	let tab = $state('active');
	const tabs = $derived([
		{ id: 'active', label: 'Active', count: rows.length },
		{ id: 'archived', label: 'Archived', count: archivedRows.length }
	]);

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
			width: '120px',
			stack: 'status'
		},
		{ id: 'engine', header: 'Engine', cell: engineCell, width: '150px' },
		{
			id: 'agent',
			header: 'Agent',
			cell: agentCell,
			sortValue: (r) => r.agentVersion,
			width: '190px'
		},
		{
			id: 'containers',
			header: 'Containers',
			cell: containersCell,
			sortValue: (r) => r.overview?.docker?.containersRunning ?? null,
			numeric: true,
			width: '110px'
		},
		{
			id: 'cpu',
			header: 'CPU',
			cell: cpuCell,
			sortValue: (r) => r.overview?.usage?.cpuPercent ?? null,
			numeric: true,
			width: '80px'
		},
		{
			id: 'memory',
			header: 'Memory',
			cell: memoryCell,
			sortValue: (r) => r.overview?.usage?.memoryUsedBytes ?? null,
			width: '200px'
		}
	];

	const archivedColumns: Column<Environment>[] = [
		{
			id: 'name',
			header: 'Environment',
			cell: archivedNameCell,
			sortValue: (r) => r.name,
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: archivedStatusCell,
			width: '120px',
			stack: 'status'
		},
		{ id: 'archivedAt', header: 'Archived', cell: archivedAtCell, width: '180px' },
		{
			id: 'actions',
			header: 'Actions',
			cell: archivedActionsCell,
			hideHeader: true,
			align: 'end',
			width: '140px',
			stack: 'actions'
		}
	];
</script>

{#snippet nameCell(r: Row)}
	<div class="name">
		<a href={routes.environment(r.id)} class="strong">{r.name}</a>
		{#if r.serviceAddress}<span class="muted small mono">{r.serviceAddress}</span>{/if}
	</div>
{/snippet}
{#snippet statusCell(r: Row)}
	<StatusBadge status={environmentStatus(r)} />
{/snippet}
{#snippet engineCell(r: Row)}
	<EngineVersion id={r.id} enabled={r.actions.includes('environment.system.read')} />
{/snippet}
{#snippet agentCell(r: Row)}
	{#if r.agentVersion}
		<div class="agent">
			<span class="num">{r.agentVersion}</span>
			{#if r.compatibility && r.compatibility !== 'current'}
				<Badge tone={COMPATIBILITY[r.compatibility]?.tone ?? 'warn'} dot
					>{COMPATIBILITY[r.compatibility]?.label ?? r.compatibility}</Badge
				>
			{/if}
		</div>
	{:else if r.view === 'full' && !r.agentId}
		<span class="muted">No agent</span>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet containersCell(r: Row)}
	{#if r.overview?.docker}
		<span class="num"
			>{r.overview.docker.containersRunning}
			<span class="muted">/ {r.overview.docker.containers}</span></span
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet cpuCell(r: Row)}
	<span class="num">{formatPercent(r.overview?.usage?.cpuPercent)}</span>
{/snippet}
{#snippet memoryCell(r: Row)}
	{@const u = r.overview?.usage}
	{#if u?.memoryUsedBytes !== undefined && u?.memoryTotalBytes}
		<div class="mem">
			<span class="num"
				>{formatBytes(u.memoryUsedBytes)}
				<span class="muted">/ {formatBytes(u.memoryTotalBytes)}</span></span
			>
			<Meter
				value={u.memoryUsedBytes}
				max={u.memoryTotalBytes}
				label="Memory of {r.name}"
				valueText="{formatBytes(u.memoryUsedBytes)} of {formatBytes(u.memoryTotalBytes)}"
			/>
		</div>
	{:else}<span class="muted">—</span>{/if}
{/snippet}

{#snippet archivedNameCell(r: Environment)}
	<a href={routes.environment(r.id)} class="strong">{r.name}</a>
{/snippet}
{#snippet archivedStatusCell(r: Environment)}
	<StatusBadge status={r.status} />
{/snippet}
{#snippet archivedAtCell(r: Environment)}
	{#if r.archivedAt}
		<time datetime={r.archivedAt} title={formatDateTime(r.archivedAt)}
			>{formatRelative(r.archivedAt)}</time
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet archivedActionsCell(r: Environment)}
	{#if canEnroll}
		<Button size="sm" href={routes.addEnvironment(r.id)}>Re-attach</Button>
	{/if}
{/snippet}

{#if perms.data && isRestricted(accessOf(perms.data))}
	<DeniedState level={1} />
{:else}
	<div class="page">
		<PageHeader
			title="Environments"
			description="Docker hosts with a connected DockYard agent. Each agent dials out to this DockYard; hosts open no ports."
		>
			{#snippet actions()}
				{#if canEnroll}
					<Button variant="primary" icon={Plus} href={routes.addEnvironment()}
						>Add environment</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if canEnroll}<EnrollmentsCard />{/if}

		{#if envs.isError}
			<ErrorState
				error={envs.error}
				title="The environments could not be loaded."
				onretry={() => envs.refetch()}
			/>
		{:else}
			<Tabs items={tabs} bind:value={tab} label="Environments by status">
				{#snippet panel(id)}
					{#if id === 'active'}
						<Card padding="none">
							{#if envs.isPending}
								<div class="skeleton" aria-busy="true">
									<Skeleton lines={4} height="20px" />
								</div>
							{:else}
								<Table
									label="Active environments"
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
											description="Add an environment: run the DockYard agent on a Docker host and enroll it with a one-time token."
											level={3}
											compact
										>
											{#snippet actions()}
												{#if canEnroll}<Button
														variant="primary"
														icon={Plus}
														href={routes.addEnvironment()}
														>Add environment</Button
													>{/if}
											{/snippet}
										</EmptyState>
									{/snippet}
								</Table>
							{/if}
						</Card>
					{:else}
						<Card padding="none">
							{#if archived.isError}
								<div class="skeleton">
									<ErrorState
										error={archived.error}
										title="Archived environments could not be loaded."
										onretry={() => archived.refetch()}
										compact
									/>
								</div>
							{:else}
								<Table
									label="Archived environments"
									rows={archivedRows}
									columns={archivedColumns}
									rowKey={(r) => r.id}
									sort={{ column: 'name', direction: 'asc' }}
								>
									{#snippet empty()}
										<EmptyState
											icon={Archive}
											title="No archived environments."
											description="Archiving hides an environment from operations and keeps its stacks, history and backups. Re-attach it later with a new agent."
											level={3}
											compact
										/>
									{/snippet}
								</Table>
							{/if}
						</Card>
					{/if}
				{/snippet}
			</Tabs>
		{/if}
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.name,
	.mem {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.strong {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.small {
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.agent {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.skeleton {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}
</style>
