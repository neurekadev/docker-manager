<script lang="ts">
	// Networks (#6): every network of the selected environment (or all),
	// searched by name, subnet, gateway, driver or label and filtered by
	// driver, stack and environment, with "Unused" (no container attached,
	// from the containers list; network lists do not report attachments)
	// and "Managed" switches (ListCard); how many containers use it, its
	// driver and subnets; its flags (internal, attachable, IPv6),
	// predefined networks (bridge, host, none) and Docker Manager's own
	// (#32) are marks on the name's line, and their removal is refused by
	// the server with the reason. Selected networks can be removed in bulk
	// (predefined, used and Docker Manager's own ones are left out).
	import { createQuery } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Plus from '@lucide/svelte/icons/plus';
	import { containersQuery, networksQuery, type Network as Net } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		DeniedState,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		PageHeader,
		Skeleton,
		Table,
		formatDateTime,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import CreateObjectDialog from '$lib/features/resources/CreateObjectDialog.svelte';
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import EnvironmentGaps from '$lib/features/resources/EnvironmentGaps.svelte';
	import ObjectRemoveHost from '$lib/features/resources/ObjectRemoveHost.svelte';
	import ObjectBulk from '$lib/features/resources/ObjectBulk.svelte';
	import IconCell from '$lib/features/common/IconCell.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionMark from '$lib/features/resources/ProtectionMark.svelte';
	import StackBadge from '$lib/features/resources/StackBadge.svelte';
	import {
		applyListFilters,
		isFiltering,
		listSummary,
		networkFilters,
		networkSearch
	} from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { sameEverywhere } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Networks', crumbs: [{ label: 'Networks' }], environmentScoped: true });

	const scope = useEnvironmentScope();
	const list = createQuery(() => ({
		...networksQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0
	}));

	// Attachments come from the containers (when the user may list them).
	const readContainers = $derived(scope.hasAny('container.details.read'));
	const containers = createQuery(() => ({
		...containersQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0 && readContainers
	}));
	// Attached containers by `<environment>/<network>`.
	const attached = $derived.by(() => {
		const counts: Record<string, number> = {};
		for (const c of containers.data?.items ?? [])
			for (const n of c.networks ?? []) {
				const k = `${c.environmentId}/${n.name}`;
				counts[k] = (counts[k] ?? 0) + 1;
			}
		return counts;
	});
	const usersOf = (n: Net) => attached[`${n.environmentId}/${n.name}`] ?? 0;

	const filters = new ListFilters('networks');
	let createOpen = $state(false);
	let remover = $state<ObjectRemoveHost>();

	const all = $derived(list.data?.items ?? []);
	const usedKeys = $derived(containers.data ? new Set(Object.keys(attached)) : undefined);
	const defs = $derived(
		networkFilters(all, {
			envs: scope.single ? [] : scope.targets,
			used: usedKeys
		})
	);
	const rows = $derived(applyListFilters(all, defs, filters.state, networkSearch));
	const filtered = $derived(isFiltering(defs, filters.state));
	const creatable = $derived(scope.creatable('network.create'));
	const key = (n: Net) => `${n.environmentId}/${n.id}`;
	let selected = $state<string[]>([]);
	// Bulk removal runs on the selected rows the filters still show.
	const chosen = $derived(rows.filter((n) => selected.includes(key(n))));
	const oneDriver = $derived(sameEverywhere(all, (n) => n.driver ?? ''));

	function menu(n: Net): MenuEntry[] {
		const out: MenuEntry[] = [{ label: 'Open', href: routes.network(n.environmentId, n.name) }];
		if (can(n.actions, 'network.remove'))
			out.push(
				{ separator: true },
				{
					label: 'Remove…',
					tone: 'danger',
					onSelect: () =>
						remover?.request({
							kind: 'network',
							environmentId: n.environmentId,
							name: n.name,
							protection: n.protection
						})
				}
			);
		return out;
	}

	const columns: Column<Net>[] = $derived([
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (n) => n.name,
			maxWidth: '320px',
			title: (n) => n.name,
			stack: 'title'
		},
		...(containers.data
			? [
					{
						id: 'use',
						header: 'Used By',
						cell: useCell,
						sortValue: usersOf,
						width: '150px',
						stack: 'status'
					} satisfies Column<Net>
				]
			: []),
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (n: Net) => scope.name(n.environmentId),
						stack: 'hidden'
					} satisfies Column<Net>
				]),
		{
			id: 'stack',
			header: 'Stack',
			cell: stackCell,
			sortValue: (n) => n.stack?.project ?? '',
			maxWidth: '180px',
			stack: 'hidden'
		},
		...(oneDriver
			? []
			: [
					{
						id: 'driver',
						header: 'Driver',
						cell: driverCell,
						sortValue: (n: Net) => n.driver ?? '',
						width: '120px'
					} satisfies Column<Net>
				]),
		{
			id: 'subnet',
			header: 'Subnet',
			cell: subnetCell,
			sortValue: (n) => n.subnets?.[0] ?? '',
			maxWidth: '220px',
			title: (n) => n.subnets?.join(', '),
			truncate: true,
			stack: 'hidden'
		},
		{
			id: 'created',
			header: 'Created',
			cell: createdCell,
			sortValue: (n) => n.createdAt ?? '',
			width: '130px',
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

{#snippet nameCell(n: Net)}
	<IconCell icon="network"
		><div class="name-cell">
			<a class="name mono" href={routes.network(n.environmentId, n.name)}>{n.name}</a>
			{#if n.protection}<ProtectionMark protection={n.protection} />{/if}
			{#if n.builtin}<span class="tag"><Badge>Predefined</Badge></span>{/if}
			{#if n.internal}<span class="tag" title="No traffic to or from outside"
					><Badge tone="warn">Internal</Badge></span
				>{/if}
		</div></IconCell
	>
{/snippet}
{#snippet stackCell(n: Net)}
	{#if n.stack}<StackBadge stack={n.stack} />{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet driverCell(n: Net)}<span class="mono">{n.driver ?? '—'}</span>{/snippet}
{#snippet subnetCell(n: Net)}
	{#if n.subnets?.length}<span class="mono">{n.subnets.join(', ')}</span>{:else}<span
			class="muted">—</span
		>{/if}
{/snippet}
{#snippet useCell(n: Net)}
	{@const count = usersOf(n)}
	{#if count}<Badge tone="ok" dot>{count} container{count === 1 ? '' : 's'}</Badge>
	{:else}<Badge>Unused</Badge>{/if}
{/snippet}
{#snippet envCell(n: Net)}{scope.name(n.environmentId)}{/snippet}
{#snippet createdCell(n: Net)}
	{#if n.createdAt}<span class="muted" title={formatDateTime(n.createdAt)}
			>{formatRelative(n.createdAt)}</span
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionsCell(n: Net)}
	<Menu items={menu(n)} label="Actions for {n.name}" align="end">
		{#snippet trigger(props)}
			<IconButton {...props} icon={Ellipsis} label="Actions for {n.name}" size="sm" />
		{/snippet}
	</Menu>
{/snippet}

<ObjectRemoveHost bind:this={remover} environmentName={(id) => scope.name(id)} />
<CreateObjectDialog
	bind:open={createOpen}
	kind="network"
	environments={creatable}
	environmentId={scope.single ? scope.targets[0]?.id : undefined}
/>

{#if scope.restricted}
	<DeniedState level={1} />
{:else if scope.perms.data && !scope.hasAny('network.')}
	<DeniedState
		level={1}
		title="You don't have access to networks."
		description="Ask the owner of this Docker Manager to grant access."
	/>
{:else}
	<Page>
		<PageHeader
			title="Networks"
			description="How containers on {scope.single
				? scope.targets[0]?.name
				: 'your environments'} reach each other."
		>
			{#snippet actions()}
				<PruneButton target="networks" {scope} />
				{#if creatable.length}
					<Button variant="primary" icon={Plus} onclick={() => (createOpen = true)}
						>Create Network</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if list.data}
			<EnvironmentGaps
				unavailable={list.data.unavailable}
				what="networks"
				single={scope.single}
			/>
		{/if}

		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The networks could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			<ListCard
				title="All Networks"
				id="networks"
				summary={list.data
					? listSummary(rows.length, all.length, filtered, 'network', 'networks')
					: undefined}
				label="Filter Networks"
				searchLabel="Search Networks"
				placeholder="Search name, subnet or driver"
				filters={defs}
				store={filters}
			>
				<ObjectBulk
					selected={{ kind: 'network', items: chosen, attached: usedKeys }}
					environmentName={(id) => scope.name(id)}
					onclear={() => (selected = [])}
				/>
				{#if !list.data}
					<div class="loading" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
				{:else}
					<Table
						label="Networks"
						{rows}
						{columns}
						rowKey={key}
						sort={{ column: 'name', direction: 'asc' }}
						selectable
						bind:selected
						rowLabel={(n) => `Select ${n.name}`}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="networks"
									icon={resourceIcon('network').icon}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									{...resourceIcon('network')}
									title="No networks to show."
									description="Create a network to connect standalone containers, or let a stack create its own."
									level={3}
									compact
								>
									{#snippet actions()}
										{#if creatable.length}
											<Button
												variant="primary"
												icon={Plus}
												onclick={() => (createOpen = true)}
												>Create Network</Button
											>
										{/if}
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
	/* One line: marks sit beside the name, so rows keep one height. */
	.name-cell {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
	}

	.name {
		overflow: hidden;
		color: var(--text-strong);
		text-decoration: none;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.tag {
		flex: none;
	}

	.name:hover {
		color: var(--accent-text);
	}

	.loading {
		padding: var(--space-5);
	}
</style>
