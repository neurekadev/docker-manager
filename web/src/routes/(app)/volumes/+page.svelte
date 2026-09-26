<script lang="ts">
	// Volumes (#6): every volume of the selected environment (or all), who
	// uses it, its driver and whether DockYard can open its files (#28:
	// non-local drivers and NFS/CIFS-backed volumes are read-only, with the
	// reason). DockYard's own volumes (#32) are marked and never removed.
	import { createQuery } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Plus from '@lucide/svelte/icons/plus';
	import { volumesQuery, type Volume } from '$lib/api/queries';
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
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import EnvironmentGaps from '$lib/features/resources/EnvironmentGaps.svelte';
	import ObjectRemoveHost from '$lib/features/resources/ObjectRemoveHost.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import StackBadge from '$lib/features/resources/StackBadge.svelte';
	import Toolbar from '$lib/features/resources/Toolbar.svelte';
	import { ChangeTracker } from '$lib/features/resources/changes.svelte';
	import { volumeAccess } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Volumes', crumbs: [{ label: 'Volumes' }], environmentScoped: true });

	const scope = useEnvironmentScope();
	const list = createQuery(() => ({
		...volumesQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0
	}));

	let q = $state('');
	let use = $state('');
	let access = $state('');
	let createOpen = $state(false);
	let remover = $state<ObjectRemoveHost>();

	const all = $derived(list.data?.items ?? []);
	const rows = $derived(
		all.filter((v) => {
			if (q.trim() && !v.name.toLowerCase().includes(q.trim().toLowerCase())) return false;
			if (use === 'used' && !v.inUse) return false;
			if (use === 'unused' && v.inUse) return false;
			const local = volumeAccess(v).local;
			if (access === 'local' && !local) return false;
			if (access === 'readonly' && local) return false;
			return true;
		})
	);
	const filtered = $derived(!!(q || use || access));
	const creatable = $derived(scope.creatable('volume.create'));
	const key = (v: Volume) => `${v.environmentId}/${v.name}`;
	const tracker = new ChangeTracker<Volume>(key, (v) => `${v.inUse}/${v.usedBy?.length ?? 0}`);
	$effect(() => {
		if (list.data) tracker.update(list.data.items);
	});

	function menu(v: Volume): MenuEntry[] {
		const out: MenuEntry[] = [{ label: 'Open', href: routes.volume(v.environmentId, v.name) }];
		if (
			volumeAccess(v).local &&
			!v.protection &&
			!v.stack?.managed &&
			can(v.actions, 'volume.files.read')
		)
			out.push({
				label: 'Browse files',
				href: routes.volume(v.environmentId, v.name, 'files')
			});
		if (can(v.actions, 'volume.migrate') && !v.protection)
			out.push({
				label: 'Migrate…',
				href: routes.volume(v.environmentId, v.name, 'migrate')
			});
		if (can(v.actions, 'volume.remove'))
			out.push(
				{ separator: true },
				{
					label: 'Remove…',
					tone: 'danger',
					onSelect: () =>
						remover?.request({
							kind: 'volume',
							environmentId: v.environmentId,
							name: v.name,
							protection: v.protection,
							usedBy: v.usedBy
						})
				}
			);
		return out;
	}

	const columns: Column<Volume>[] = $derived([
		{ id: 'name', header: 'Name', cell: nameCell, sortValue: (v) => v.name, stack: 'title' },
		{
			id: 'use',
			header: 'Used by',
			cell: useCell,
			sortValue: (v) => v.usedBy?.length ?? (v.inUse ? 1 : 0),
			width: '170px',
			stack: 'status'
		},
		{
			id: 'access',
			header: 'Files',
			cell: accessCell,
			sortValue: (v) => (volumeAccess(v).local ? 0 : 1),
			width: '150px'
		},
		{
			id: 'driver',
			header: 'Driver',
			cell: driverCell,
			sortValue: (v) => v.driver ?? '',
			width: '110px'
		},
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (v: Volume) => scope.name(v.environmentId)
					} satisfies Column<Volume>
				]),
		{
			id: 'created',
			header: 'Created',
			cell: createdCell,
			sortValue: (v) => v.createdAt ?? '',
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

{#snippet nameCell(v: Volume)}
	<div class="name-cell">
		<a class="name mono" href={routes.volume(v.environmentId, v.name)}>{v.name}</a>
		{#if v.protection || v.stack}
			<span class="tags">
				{#if v.protection}<ProtectionBadge protection={v.protection} />{/if}
				{#if v.stack}<StackBadge stack={v.stack} />{/if}
			</span>
		{/if}
	</div>
{/snippet}
{#snippet useCell(v: Volume)}
	{#if v.usedBy?.length}
		<span class="users" title={v.usedBy.map((c) => c.name).join(', ')}>
			<Badge tone="ok" dot
				>{v.usedBy.length} container{v.usedBy.length === 1 ? '' : 's'}</Badge
			>
		</span>
	{:else if v.inUse}<Badge tone="ok" dot>In use</Badge>
	{:else}<Badge>Unused</Badge>{/if}
{/snippet}
{#snippet accessCell(v: Volume)}
	{@const a = volumeAccess(v)}
	{#if a.local}<span class="muted">Local</span>
	{:else}<span title={a.reason}
			><Badge tone="warn">Read-only</Badge><span class="sr-only">: {a.reason}</span></span
		>{/if}
{/snippet}
{#snippet driverCell(v: Volume)}<span class="mono">{v.driver ?? '—'}</span>{/snippet}
{#snippet envCell(v: Volume)}{scope.name(v.environmentId)}{/snippet}
{#snippet createdCell(v: Volume)}
	{#if v.createdAt}<span class="muted" title={v.createdAt}>{formatRelative(v.createdAt)}</span
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionsCell(v: Volume)}
	<Menu items={menu(v)} label="Actions for {v.name}" align="end">
		{#snippet trigger(props)}
			<IconButton {...props} icon={Ellipsis} label="Actions for {v.name}" size="sm" />
		{/snippet}
	</Menu>
{/snippet}

<ObjectRemoveHost bind:this={remover} environmentName={(id) => scope.name(id)} />
<CreateObjectDialog
	bind:open={createOpen}
	kind="volume"
	environments={creatable}
	environmentId={scope.single ? scope.targets[0]?.id : undefined}
/>

{#if scope.restricted}
	<DeniedState level={1} />
{:else if scope.perms.data && !scope.hasAny('volume.')}
	<DeniedState
		level={1}
		title="You don't have access to volumes."
		description="Ask the owner of this DockYard to grant access."
	/>
{:else}
	<Page>
		<PageHeader
			title="Volumes"
			description="Persistent data on {scope.single
				? scope.targets[0]?.name
				: 'your environments'}, and the containers that use it."
		>
			{#snippet actions()}
				<PruneButton target="volumes" {scope} />
				{#if creatable.length}
					<Button variant="primary" icon={Plus} onclick={() => (createOpen = true)}
						>Create volume</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if list.data}
			<EnvironmentGaps
				unavailable={list.data.unavailable}
				what="volumes"
				single={scope.single}
			/>
		{/if}

		<Toolbar
			label="Filter volumes"
			summary={list.data
				? filtered
					? `${rows.length} of ${all.length} volumes`
					: `${all.length} volumes`
				: undefined}
		>
			<TextField
				label="Search volumes"
				hideLabel
				type="search"
				placeholder="Search by name"
				bind:value={q}
			/>
			<Select
				label="Usage"
				bind:value={use}
				options={[
					{ value: '', label: 'All volumes' },
					{ value: 'used', label: 'Used by containers' },
					{ value: 'unused', label: 'Unused' }
				]}
			/>
			<Select
				label="Files"
				bind:value={access}
				options={[
					{ value: '', label: 'Local and read-only' },
					{ value: 'local', label: 'Local only' },
					{ value: 'readonly', label: 'Read-only only' }
				]}
			/>
		</Toolbar>

		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The volumes could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			<Card padding="none">
				{#if !list.data}
					<div class="loading" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
				{:else}
					<Table
						label="Volumes"
						{rows}
						{columns}
						rowKey={key}
						changed={tracker.changed}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<EmptyState
									icon={HardDrive}
									color="slate"
									title="No volumes match these filters."
									level={2}
									compact
								>
									{#snippet actions()}
										<Button
											variant="secondary"
											onclick={() => ((q = ''), (use = ''), (access = ''))}
											>Clear filters</Button
										>
									{/snippet}
								</EmptyState>
							{:else}
								<EmptyState
									icon={HardDrive}
									color="teal"
									title="No volumes on {scope.single
										? scope.targets[0]?.name
										: 'your environments'} yet."
									description="Create a volume for data that must outlive its containers, or let a stack create its own."
									level={2}
									compact
								>
									{#snippet actions()}
										{#if creatable.length}
											<Button
												variant="primary"
												icon={Plus}
												onclick={() => (createOpen = true)}
												>Create volume</Button
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
		overflow-wrap: anywhere;
	}

	.name:hover {
		color: var(--accent-text);
	}

	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1);
	}

	.loading {
		padding: var(--space-5);
	}
</style>
