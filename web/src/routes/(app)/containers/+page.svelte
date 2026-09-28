<script lang="ts">
	// Containers (#6): every container of the selected environment (or of
	// all visible ones), searched by name, image, digest, label, network
	// and address, and filtered by status, stack, available updates and
	// environment (kept per list and browser tab, ListCard). Columns follow
	// what people scan for: the container and its image (with the image's
	// update state as an icon that checks again, #20), status, stack, the
	// live figures (CPU and memory from the newest samples, #5, refreshed by
	// metrics events; uptime ticking every second), its networks (linked,
	// with its addresses) and published ports.
	// Docker Manager's own containers carry the shield mark (#32),
	// containers of a Compose project their stack. Row actions follow the
	// container's state and granted actions (#17); refusals show the
	// server's reason. Selected rows get bulk actions (start, stop,
	// restart, remove; ContainerBulk). Wide content is capped (name and
	// image, networks) and the row actions stay pinned at the right edge;
	// phones show the name, image and status only.
	import { createQueries, createQuery } from '@tanstack/svelte-query';
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
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import IconCell from '$lib/features/common/IconCell.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import ContainerActionHost from '$lib/features/resources/ContainerActionHost.svelte';
	import ContainerBulk from '$lib/features/resources/ContainerBulk.svelte';
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import EnvironmentGaps from '$lib/features/resources/EnvironmentGaps.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NetworkList from '$lib/features/resources/NetworkList.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionMark from '$lib/features/resources/ProtectionMark.svelte';
	import StackBadge from '$lib/features/resources/StackBadge.svelte';
	import { ChangeTracker } from '$lib/features/resources/changes.svelte';
	import {
		containerActions,
		type ContainerVerb
	} from '$lib/features/resources/container-actions';
	import ImageUpdateBadge from '$lib/features/updates/ImageUpdateBadge.svelte';
	import { containerPolicy, policiesByTarget } from '$lib/features/updates/model';
	import { updatePoliciesQuery } from '$lib/features/updates/queries';
	import {
		applyListFilters,
		containerFilters,
		containerSearch,
		isFiltering,
		listSummary
	} from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import {
		containerStatus,
		networkEntries,
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
	const networks = (c: Container) => networkEntries([c.networks]);

	// The update policies behind the image update icons (checks need one).
	const policies = createQuery(() => ({
		...updatePoliciesQuery(scope.single ? (scope.targets[0]?.id ?? null) : null),
		enabled: scope.ready && scope.hasAny('update_policy.read')
	}));
	const policyIndex = $derived(policiesByTarget(policies.data ?? []));

	const filters = new ListFilters('containers');
	let host = $state<ContainerActionHost>();

	const all = $derived(list.data?.items ?? []);
	const defs = $derived(containerFilters(all, { envs: scope.single ? [] : scope.targets }));
	const rows = $derived(applyListFilters(all, defs, filters.state, containerSearch));
	const filtered = $derived(isFiltering(defs, filters.state));
	const creatable = $derived(scope.creatable('container.create'));
	const key = (c: Container) => `${c.environmentId}/${c.name}`;
	let selected = $state<string[]>([]);
	// Bulk actions run on the selected rows the filters still show.
	const chosen = $derived(rows.filter((c) => selected.includes(key(c))));

	const tracker = new ChangeTracker<Container>(key, (c) => `${c.state}/${c.health ?? ''}`);
	$effect(() => {
		if (list.data) tracker.update(list.data.items);
	});

	const envName = (id: string) => scope.name(id);

	const MENU_ORDER: ContainerVerb[] = ['start', 'restart', 'stop', 'pause', 'unpause'];

	function menu(c: Container): MenuEntry[] {
		const entries: MenuEntry[] = [
			{ label: 'Open', href: routes.container(c.environmentId, c.name) }
		];
		const acts = containerActions(c);
		// Start, Restart, Stop (the order of the header's lifecycle menu),
		// then Pause or Unpause.
		const lifecycle = acts
			.filter((a) => a.verb !== 'remove')
			.sort((a, b) => MENU_ORDER.indexOf(a.verb) - MENU_ORDER.indexOf(b.verb));
		if (lifecycle.length) entries.push({ separator: true });
		for (const a of lifecycle)
			entries.push({
				label: a.verb === 'stop' ? 'Stop…' : a.label,
				tone: a.verb === 'stop' ? 'danger' : undefined,
				onSelect: () => host?.request(c, a.verb)
			});
		// Destructive last, after a separator (one rule on every page).
		if (acts.some((a) => a.verb === 'remove'))
			entries.push(
				{ separator: true },
				{ label: 'Remove…', tone: 'danger', onSelect: () => host?.request(c, 'remove') }
			);
		return entries;
	}

	const columns: Column<Container>[] = $derived([
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (c) => c.name,
			maxWidth: '280px',
			title: (c) => c.name,
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
			id: 'stack',
			header: 'Stack',
			cell: stackCell,
			sortValue: (c) => c.stack?.project ?? '',
			maxWidth: '180px',
			stack: 'hidden'
		},
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (c: Container) => envName(c.environmentId),
						stack: 'hidden'
					} satisfies Column<Container>
				]),
		{
			id: 'cpu',
			header: 'CPU',
			cell: cpuCell,
			sortValue: (c) => sample(c)?.cpuPercent,
			numeric: true,
			width: '72px',
			stack: 'hidden'
		},
		{
			id: 'memory',
			header: 'Memory',
			cell: memoryCell,
			sortValue: (c) => sample(c)?.memoryUsedBytes,
			numeric: true,
			width: '88px',
			stack: 'hidden'
		},
		{
			id: 'uptime',
			header: 'Uptime',
			cell: uptimeCell,
			sortValue: (c) => uptimeSortValue(upSince(c)),
			numeric: true,
			width: '112px',
			stack: 'hidden'
		},
		{
			id: 'networks',
			header: 'Networks',
			cell: networksCell,
			sortValue: (c) => networks(c)[0]?.name,
			maxWidth: '220px',
			stack: 'hidden'
		},
		{
			id: 'ports',
			header: 'Ports',
			cell: portsCell,
			sortValue: (c) => uniquePorts(c.ports).find((p) => p.hostPort)?.hostPort,
			stack: 'hidden'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '56px',
			align: 'end',
			pin: 'end',
			stack: 'head'
		}
	]);
</script>

{#snippet nameCell(c: Container)}
	{@const policy = containerPolicy(policyIndex, c)}
	<IconCell icon="container" color={c.protection ? 'violet' : undefined}
		><div class="name-cell">
			<span class="title-line">
				<a class="name" href={routes.container(c.environmentId, c.name)}>{c.name}</a>
				{#if c.protection}<ProtectionMark protection={c.protection} />{/if}
			</span>
			{#if c.image}
				<span class="image">
					<span class="sub mono" title={c.image}>{c.image}</span>
					<ImageUpdateBadge
						status={c.update}
						image={c.image}
						policyId={policy?.id}
						canCheck={policy?.canCheck}
					/>
				</span>
			{/if}
		</div></IconCell
	>
{/snippet}
{#snippet statusCell(c: Container)}
	<StatusBadge status={containerStatus(c)} />
{/snippet}
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
{#snippet networksCell(c: Container)}
	<NetworkList environmentId={c.environmentId} networks={networks(c)} />
{/snippet}
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
				placeholder="Search name, image or address"
				filters={defs}
				store={filters}
			>
				<ContainerBulk
					selected={chosen}
					environmentName={envName}
					onclear={() => (selected = [])}
				/>
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
						selectable
						bind:selected
						rowLabel={(c) => `Select ${c.name}`}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="containers"
									icon={resourceIcon('container').icon}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									{...resourceIcon('container')}
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

	.title-line {
		display: flex;
		align-items: center;
		gap: var(--space-1);
		min-width: 0;
	}

	.name {
		overflow: hidden;
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		text-decoration: none;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.name:hover {
		color: var(--accent-text);
	}

	.image {
		display: flex;
		align-items: center;
		gap: var(--space-1);
		min-width: 0;
	}

	.sub {
		min-width: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.ports {
		font-size: var(--text-caption);
		white-space: nowrap;
	}

	.loading {
		padding: var(--space-5);
	}
</style>
