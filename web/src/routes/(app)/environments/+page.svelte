<script lang="ts">
	// Environments (#3, #5, #34): every Docker host with a Docker Agent,
	// with status, Engine, agent version and compatibility, containers and
	// usage; archived environments to re-attach (a tab only while there are
	// any; the tab lives in the URL, ?tab=archived); pending enrollment
	// tokens below the list. A whole row opens its environment.
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { createQuery } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import Plus from '@lucide/svelte/icons/plus';
	import type { Environment, Schema } from '$lib/api/client';
	import {
		archivedEnvironmentsQuery,
		environmentsQuery,
		myPermissionsQuery,
		overviewQuery
	} from '$lib/api/queries';
	import IconCell from '$lib/features/common/IconCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import { environmentIcon, resourceIcon } from '$lib/features/common/resourceIcons';
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
	// The Archived tab shows only while there is something in it (or it
	// failed to load and says so).
	const showTabs = $derived(archivedRows.length > 0 || archived.isError);

	const tab = $derived(
		showTabs && page.url.searchParams.get('tab') === 'archived' ? 'archived' : 'active'
	);
	const tabs = $derived([
		{ id: 'active', label: 'Active', count: rows.length },
		{ id: 'archived', label: 'Archived', count: archivedRows.length }
	]);
	function selectTab(t: string) {
		const url = new URL(page.url);
		if (t === 'active') url.searchParams.delete('tab');
		else url.searchParams.set('tab', t);
		void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
	}

	const columns: Column<Row>[] = [
		{
			id: 'name',
			header: 'Environment',
			cell: nameCell,
			sortValue: (r) => r.name,
			maxWidth: '280px',
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
			width: '120px'
		},
		{
			id: 'cpu',
			header: 'CPU',
			cell: cpuCell,
			sortValue: (r) => r.overview?.usage?.cpuPercent ?? null,
			numeric: true,
			width: '100px'
		},
		{
			id: 'memory',
			header: 'Memory',
			cell: memoryCell,
			sortValue: (r) => r.overview?.usage?.memoryUsedBytes ?? null,
			numeric: true,
			width: '200px'
		}
	];

	const archivedColumns: Column<Environment>[] = [
		{
			id: 'name',
			header: 'Environment',
			cell: archivedNameCell,
			sortValue: (r) => r.name,
			maxWidth: '280px',
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: archivedStatusCell,
			width: '120px',
			stack: 'status'
		},
		{
			id: 'archivedAt',
			header: 'Archived',
			cell: archivedAtCell,
			sortValue: (r) => r.archivedAt,
			width: '180px'
		},
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
	<IconCell icon={environmentIcon(r.online)}>
		<div class="name">
			<a href={routes.environment(r.id)} class="strong row-link">{r.name}</a>
			{#if r.serviceAddress}<span class="muted small mono">{r.serviceAddress}</span>{/if}
		</div>
	</IconCell>
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
		<span class="muted">No Agent</span>
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
	<IconCell icon={environmentIcon(r.online)}>
		<a href={routes.environment(r.id)} class="strong row-link">{r.name}</a>
	</IconCell>
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
		<span class="above"
			><Button size="sm" href={routes.addEnvironment(r.id)}>Re-Attach</Button></span
		>
	{/if}
{/snippet}

{#snippet activeList()}
	<Card padding="none">
		{#if envs.isPending}
			<div class="skeleton" aria-busy="true">
				<Skeleton lines={4} height="20px" />
			</div>
		{:else}
			<div class="rows">
				<Table
					label="Active Environments"
					{rows}
					{columns}
					rowKey={(r) => r.id}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}
						<EmptyState
							{...resourceIcon('environment')}
							title="No environments yet."
							description="Run the Docker Agent on a Docker host to add it."
							level={3}
							compact
						>
							{#snippet actions()}
								{#if canEnroll}<Button
										variant="primary"
										icon={Plus}
										href={routes.addEnvironment()}>Add Environment</Button
									>{/if}
							{/snippet}
						</EmptyState>
					{/snippet}
				</Table>
			</div>
		{/if}
	</Card>
{/snippet}

{#if perms.data && isRestricted(accessOf(perms.data))}
	<DeniedState level={1} />
{:else}
	<Page>
		<PageHeader title="Environments">
			{#snippet actions()}
				{#if canEnroll}
					<Button variant="primary" icon={Plus} href={routes.addEnvironment()}
						>Add Environment</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if envs.isError}
			<ErrorState
				error={envs.error}
				title="The environments could not be loaded."
				onretry={() => envs.refetch()}
			/>
		{:else if !showTabs}
			{@render activeList()}
		{:else}
			<Tabs items={tabs} value={tab} label="Environments by Status" onchange={selectTab}>
				{#snippet panel(id)}
					{#if id === 'active'}
						{@render activeList()}
					{:else}
						<Card padding="none">
							{#if archived.isError}
								<div class="skeleton">
									<ErrorState
										error={archived.error}
										title="Archived environments could not be loaded."
										onretry={() => archived.refetch()}
										bare
										compact
									/>
								</div>
							{:else}
								<div class="rows">
									<Table
										label="Archived Environments"
										rows={archivedRows}
										columns={archivedColumns}
										rowKey={(r) => r.id}
										sort={{ column: 'name', direction: 'asc' }}
									>
										{#snippet empty()}
											<EmptyState
												icon={Archive}
												title="No archived environments."
												level={3}
												compact
											/>
										{/snippet}
									</Table>
								</div>
							{/if}
						</Card>
					{/if}
				{/snippet}
			</Tabs>
		{/if}

		{#if canEnroll}<EnrollmentsCard />{/if}
	</Page>
{/if}

<style>
	.name,
	.mem {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	/* Numbers and the memory meter line up at the right like the other
	   figures. */
	.mem {
		align-items: flex-end;
	}

	.mem :global(.meter-row) {
		width: 100%;
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

	/* A whole row opens its environment: the name link covers the row
	   (and the phone card); buttons in the row stay above it. */
	.rows :global(tbody tr),
	.rows :global(li.card) {
		position: relative;
	}

	.rows :global(.row-link::after) {
		content: '';
		position: absolute;
		inset: 0;
	}

	.rows :global(tbody tr:has(.row-link)) {
		cursor: pointer;
	}

	.above {
		position: relative;
		z-index: 2;
	}
</style>
