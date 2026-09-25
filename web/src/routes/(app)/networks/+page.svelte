<script lang="ts">
	// Networks (#6): every network of the selected environment (or all),
	// its driver, subnets and flags; predefined networks (bridge, host,
	// none) and DockYard's own (#32) are marked, and their removal is
	// refused by the server with the reason.
	import { createQuery } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Network from '@lucide/svelte/icons/network';
	import Plus from '@lucide/svelte/icons/plus';
	import { networksQuery, type Network as Net } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		DeniedState,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		PageHeader,
		Select,
		Skeleton,
		Table,
		TextField,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import CreateObjectDialog from '$lib/features/resources/CreateObjectDialog.svelte';
	import EnvironmentGaps from '$lib/features/resources/EnvironmentGaps.svelte';
	import ObjectRemoveHost from '$lib/features/resources/ObjectRemoveHost.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import StackBadge from '$lib/features/resources/StackBadge.svelte';
	import Toolbar from '$lib/features/resources/Toolbar.svelte';
	import { can } from '$lib/features/resources/permissions';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Networks', crumbs: [{ label: 'Networks' }], environmentScoped: true });

	const scope = useEnvironmentScope();
	const list = createQuery(() => ({
		...networksQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0
	}));

	let q = $state('');
	let kind = $state('');
	let createOpen = $state(false);
	let remover = $state<ObjectRemoveHost>();

	const all = $derived(list.data?.items ?? []);
	const rows = $derived(
		all.filter((n) => {
			if (q.trim() && !n.name.toLowerCase().includes(q.trim().toLowerCase())) return false;
			if (kind === 'user' && (n.builtin || n.stack)) return false;
			if (kind === 'stack' && !n.stack) return false;
			if (kind === 'builtin' && !n.builtin) return false;
			return true;
		})
	);
	const filtered = $derived(!!(q || kind));
	const creatable = $derived(scope.creatable('network.create'));
	const key = (n: Net) => `${n.environmentId}/${n.id}`;

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
		{ id: 'name', header: 'Name', cell: nameCell, sortValue: (n) => n.name, stack: 'title' },
		{
			id: 'driver',
			header: 'Driver',
			cell: driverCell,
			sortValue: (n) => n.driver ?? '',
			width: '120px',
			stack: 'status'
		},
		{
			id: 'subnet',
			header: 'Subnet',
			cell: subnetCell,
			sortValue: (n) => n.subnets?.[0] ?? ''
		},
		{ id: 'flags', header: 'Access', cell: flagsCell, width: '170px' },
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (n: Net) => scope.name(n.environmentId)
					} satisfies Column<Net>
				]),
		{
			id: 'created',
			header: 'Created',
			cell: createdCell,
			sortValue: (n) => n.createdAt ?? '',
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

{#snippet nameCell(n: Net)}
	<div class="name-cell">
		<a class="name mono" href={routes.network(n.environmentId, n.name)}>{n.name}</a>
		{#if n.builtin || n.protection || n.stack}
			<span class="tags">
				{#if n.builtin}<Badge>Predefined</Badge>{/if}
				{#if n.protection}<ProtectionBadge protection={n.protection} />{/if}
				{#if n.stack}<StackBadge stack={n.stack} />{/if}
			</span>
		{/if}
	</div>
{/snippet}
{#snippet driverCell(n: Net)}<span class="mono">{n.driver ?? '—'}</span>{/snippet}
{#snippet subnetCell(n: Net)}
	{#if n.subnets?.length}<span class="mono">{n.subnets.join(', ')}</span>{:else}<span
			class="muted">—</span
		>{/if}
{/snippet}
{#snippet flagsCell(n: Net)}
	<span class="flags">
		{#if n.internal}<Badge tone="warn">Internal</Badge>{/if}
		{#if n.attachable}<Badge>Attachable</Badge>{/if}
		{#if n.enableIpv6}<Badge>IPv6</Badge>{/if}
		{#if !n.internal && !n.attachable && !n.enableIpv6}<span class="muted">Default</span>{/if}
	</span>
{/snippet}
{#snippet envCell(n: Net)}{scope.name(n.environmentId)}{/snippet}
{#snippet createdCell(n: Net)}
	{#if n.createdAt}<span class="muted" title={n.createdAt}>{formatRelative(n.createdAt)}</span
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
		description="Ask the owner of this DockYard to grant access."
	/>
{:else}
	<Page>
		<PageHeader
			title="Networks"
			description="How containers on {scope.single
				? scope.targets[0]?.name
				: 'your environments'} reach each other."
			icon={Network}
			color="indigo"
		>
			{#snippet actions()}
				{#if creatable.length}
					<Button variant="primary" icon={Plus} onclick={() => (createOpen = true)}
						>Create network</Button
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

		<Toolbar
			label="Filter networks"
			summary={list.data
				? filtered
					? `${rows.length} of ${all.length} networks`
					: `${all.length} networks`
				: undefined}
		>
			<TextField
				label="Search networks"
				hideLabel
				type="search"
				placeholder="Search by name"
				bind:value={q}
			/>
			<Select
				label="Show"
				bind:value={kind}
				options={[
					{ value: '', label: 'All networks' },
					{ value: 'user', label: 'Created by you' },
					{ value: 'stack', label: 'Of Compose projects' },
					{ value: 'builtin', label: 'Predefined' }
				]}
			/>
		</Toolbar>

		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The networks could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			<Card padding="none">
				{#if !list.data}
					<div class="loading" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
				{:else}
					<Table
						label="Networks"
						{rows}
						{columns}
						rowKey={key}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<EmptyState
									icon={Network}
									color="slate"
									title="No networks match these filters."
									level={2}
									compact
								>
									{#snippet actions()}
										<Button
											variant="secondary"
											onclick={() => ((q = ''), (kind = ''))}
											>Clear filters</Button
										>
									{/snippet}
								</EmptyState>
							{:else}
								<EmptyState
									icon={Network}
									color="indigo"
									title="No networks to show."
									description="Create a network to connect standalone containers, or let a stack create its own."
									level={2}
									compact
								>
									{#snippet actions()}
										{#if creatable.length}
											<Button
												variant="primary"
												icon={Plus}
												onclick={() => (createOpen = true)}
												>Create network</Button
											>
										{/if}
									{/snippet}
								</EmptyState>
							{/if}
						{/snippet}
					</Table>
				{/if}
			</Card>
		{/if}
	</Page>
{/if}

<style>
	.name-cell {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		text-decoration: none;
	}

	.name:hover {
		color: var(--accent-text);
	}

	.tags,
	.flags {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1);
	}

	.loading {
		padding: var(--space-5);
	}
</style>
