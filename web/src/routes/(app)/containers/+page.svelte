<script lang="ts">
	// Containers (#6): every container of the selected environment (or of
	// all visible ones), searched by name/image and filtered by status,
	// stack, image update, Docker Manager system, label and environment
	// (kept per list and browser tab, ListCard),
	// with a live uptime (ticking every second), CPU and memory from the
	// newest 10 s samples (#5, refreshed by metrics events) and addresses.
	// Docker Manager's own containers carry the "Docker Manager" badge (#32),
	// containers of a Compose project their stack. Row actions follow the
	// container's state and granted actions (#17); refusals show the
	// server's reason.
	import { createQueries, createQuery } from '@tanstack/svelte-query';
	import ContainerIcon from '@lucide/svelte/icons/container';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Plus from '@lucide/svelte/icons/plus';
	import Layers from '@lucide/svelte/icons/layers';
	import { containersQuery, latestContainerMetricsQuery, type Container } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		DeniedState,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		PageHeader,
		Skeleton,
		StatusBadge,
		Table,
		Uptime,
		formatBytes,
		formatPercent,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import AddressList from '$lib/features/resources/AddressList.svelte';
	import ContainerActionHost from '$lib/features/resources/ContainerActionHost.svelte';
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import EnvironmentGaps from '$lib/features/resources/EnvironmentGaps.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import StackBadge from '$lib/features/resources/StackBadge.svelte';
	import { ChangeTracker } from '$lib/features/resources/changes.svelte';
	import { containerActions } from '$lib/features/resources/container-actions';
	import UpdateStatusBadge from '$lib/features/updates/UpdateStatusBadge.svelte';
	import {
		applyListFilters,
		containerFilters,
		containerSearch,
		isFiltering,
		listSummary
	} from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import {
		containerAddresses,
		containerStatus,
		portText,
		uniquePorts,
		upSince,
		uptimeSortValue
	} from '$lib/features/resources/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Containers', crumbs: [{ label: 'Containers' }], environmentScoped: true });

	const scope = useEnvironmentScope();
	const list = createQuery(() => ({
		...containersQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0
	}));

	// Current CPU and memory per environment (only for callers who may
	// chart containers; the server filters per container).
	const latest = createQueries(() => ({
		queries: scope.hasAny('container.metrics.read')
			? scope.targets.map((t) => latestContainerMetricsQuery(t.id))
			: []
	}));
	const samples = $derived(
		new Map(
			scope.targets.flatMap((t, i) =>
				Object.values(latest[i]?.data ?? {}).map(
					(m) => [`${t.id}/${m.container}`, m] as const
				)
			)
		)
	);
	/** The newest sample of a running container (none while stopped). */
	const sample = (c: Container) =>
		c.state === 'running' ? samples.get(`${c.environmentId}/${c.name}`) : undefined;
	const addresses = (c: Container) => containerAddresses([c.networks]);

	const filters = new ListFilters('containers');
	let host = $state<ContainerActionHost>();

	const all = $derived(list.data?.items ?? []);
	const defs = $derived(containerFilters(all, { envs: scope.single ? [] : scope.targets }));
	const rows = $derived(applyListFilters(all, defs, filters.state, containerSearch));
	const filtered = $derived(isFiltering(defs, filters.state));
	const creatable = $derived(scope.creatable('container.create'));
	const key = (c: Container) => `${c.environmentId}/${c.name}`;

	const tracker = new ChangeTracker<Container>(key, (c) => `${c.state}/${c.health ?? ''}`);
	$effect(() => {
		if (list.data) tracker.update(list.data.items);
	});

	const envName = (id: string) => scope.name(id);

	function menu(c: Container): MenuEntry[] {
		const entries: MenuEntry[] = [
			{ label: 'Open', href: routes.container(c.environmentId, c.name) }
		];
		const acts = containerActions(c);
		if (acts.length) entries.push({ separator: true });
		for (const a of acts)
			entries.push({
				label: a.verb === 'remove' ? 'Remove…' : a.verb === 'stop' ? 'Stop…' : a.label,
				tone: a.danger && a.verb === 'remove' ? 'danger' : 'default',
				onSelect: () => host?.request(c, a.verb)
			});
		return entries;
	}

	const columns: Column<Container>[] = $derived([
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (c) => c.name,
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (c) => containerStatus(c),
			width: '140px',
			stack: 'status'
		},
		{
			id: 'uptime',
			header: 'Uptime',
			cell: uptimeCell,
			sortValue: (c) => uptimeSortValue(upSince(c)),
			numeric: true,
			width: '112px'
		},
		{
			id: 'cpu',
			header: 'CPU',
			cell: cpuCell,
			sortValue: (c) => sample(c)?.cpuPercent,
			numeric: true,
			width: '72px'
		},
		{
			id: 'memory',
			header: 'Memory',
			cell: memoryCell,
			sortValue: (c) => sample(c)?.memoryUsedBytes,
			numeric: true,
			width: '88px'
		},
		{ id: 'stack', header: 'Stack', cell: stackCell, sortValue: (c) => c.stack?.project ?? '' },
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (c: Container) => envName(c.environmentId)
					} satisfies Column<Container>
				]),
		{
			id: 'addresses',
			header: 'IP addresses',
			cell: addressesCell,
			sortValue: (c) => addresses(c)[0]?.address,
			width: '140px'
		},
		{
			id: 'ports',
			header: 'Ports',
			cell: portsCell,
			sortValue: (c) => uniquePorts(c.ports).find((p) => p.hostPort)?.hostPort
		},
		{
			id: 'update',
			header: 'Image update',
			cell: updateCell,
			sortValue: (c) => c.update ?? '',
			width: '150px'
		},
		{
			id: 'created',
			header: 'Created',
			cell: createdCell,
			sortValue: (c) => c.createdAt ?? '',
			width: '130px'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '56px',
			align: 'end',
			stack: 'actions'
		}
	]);
</script>

{#snippet nameCell(c: Container)}
	<div class="name-cell">
		<a class="name" href={routes.container(c.environmentId, c.name)}>{c.name}</a>
		{#if c.image}<span class="sub mono" title={c.image}>{c.image}</span>{/if}
		{#if c.protection}<span class="tag"><ProtectionBadge protection={c.protection} /></span
			>{/if}
	</div>
{/snippet}
{#snippet statusCell(c: Container)}
	<StatusBadge status={containerStatus(c)} />
{/snippet}
{#snippet updateCell(c: Container)}<UpdateStatusBadge status={c.update} />{/snippet}
{#snippet uptimeCell(c: Container)}<Uptime since={upSince(c)} />{/snippet}
{#snippet cpuCell(c: Container)}{formatPercent(sample(c)?.cpuPercent)}{/snippet}
{#snippet memoryCell(c: Container)}
	{@const m = sample(c)}
	<span
		title={m?.memoryUsedBytes !== undefined && m.memoryLimitBytes
			? `${formatBytes(m.memoryUsedBytes)} of the ${formatBytes(m.memoryLimitBytes)} limit`
			: undefined}>{formatBytes(m?.memoryUsedBytes)}</span
	>
{/snippet}
{#snippet addressesCell(c: Container)}<AddressList addresses={addresses(c)} />{/snippet}
{#snippet stackCell(c: Container)}
	{#if c.stack}<StackBadge stack={c.stack} />{:else}<span class="muted">Standalone</span>{/if}
{/snippet}
{#snippet envCell(c: Container)}
	<span>{envName(c.environmentId)}</span>
{/snippet}
{#snippet portsCell(c: Container)}
	{@const ports = uniquePorts(c.ports).filter((p) => p.hostPort)}
	{#if ports.length}
		<span class="mono ports"
			>{ports.slice(0, 2).map(portText).join(', ')}{#if ports.length > 2}<span class="muted">
					+{ports.length - 2}</span
				>{/if}</span
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet createdCell(c: Container)}
	{#if c.createdAt}<span class="muted nowrap" title={c.createdAt}
			>{formatRelative(c.createdAt)}</span
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionsCell(c: Container)}
	<Menu items={menu(c)} label="Actions for {c.name}" align="end">
		{#snippet trigger(props)}
			<IconButton {...props} icon={Ellipsis} label="Actions for {c.name}" size="sm" />
		{/snippet}
	</Menu>
{/snippet}

<ContainerActionHost bind:this={host} environmentName={envName} />

{#if scope.restricted}
	<DeniedState level={1} />
{:else if scope.perms.data && !scope.hasAny('container.')}
	<DeniedState
		level={1}
		title="You don't have access to containers."
		description="Ask the owner of this Docker Manager to grant access."
	/>
{:else}
	<Page>
		<PageHeader
			title="Containers"
			description="Every container on {scope.single
				? scope.targets[0]?.name
				: 'your environments'}, running or not."
		>
			{#snippet actions()}
				<PruneButton target="containers" {scope} />
				{#if creatable.length}
					<Button
						variant="primary"
						icon={Plus}
						href={routes.newContainer(scope.single ? scope.targets[0]?.id : undefined)}
						>Create container</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if list.data}
			<EnvironmentGaps
				unavailable={list.data.unavailable}
				what="containers"
				single={scope.single}
			/>
		{/if}

		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The containers could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			<ListCard
				title="All containers"
				id="containers"
				summary={list.data
					? listSummary(rows.length, all.length, filtered, 'container', 'containers')
					: undefined}
				label="Filter containers"
				searchLabel="Search containers"
				placeholder="Search by name or image"
				filters={defs}
				store={filters}
			>
				{#if !list.data && (list.isPending || !scope.ready)}
					<div class="loading" aria-busy="true">
						<Skeleton lines={6} height="20px" />
					</div>
				{:else}
					<Table
						label="Containers"
						{rows}
						{columns}
						rowKey={key}
						changed={tracker.changed}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="containers"
									icon={ContainerIcon}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									icon={ContainerIcon}
									color="blue"
									title="No containers on {scope.single
										? scope.targets[0]?.name
										: 'your environments'} yet."
									description="Create a container from an image, or deploy a Compose stack for anything with several services."
									level={3}
									compact
								>
									{#snippet actions()}
										{#if creatable.length}
											<Button
												variant="primary"
												icon={Plus}
												href={routes.newContainer(
													scope.single ? scope.targets[0]?.id : undefined
												)}>Create container</Button
											>
										{/if}
										<Button
											variant="secondary"
											icon={Layers}
											href={routes.stacks()}>Go to stacks</Button
										>
									{/snippet}
								</EmptyState>
							{/if}
						{/snippet}
					</Table>
				{/if}
			</ListCard>
		{/if}
	</Page>
{/if}

<style>
	.name-cell {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		text-decoration: none;
	}

	.name:hover {
		color: var(--accent-text);
	}

	.sub {
		max-width: 60ch;
		color: var(--text-muted);
		font-size: var(--text-caption);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.tag {
		margin-top: 2px;
	}

	.ports {
		font-size: var(--text-caption);
	}

	.nowrap {
		white-space: nowrap;
	}

	.loading {
		padding: var(--space-5);
	}
</style>
