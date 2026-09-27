<script lang="ts">
	// Volumes (#6): every volume of the selected environment (or all),
	// searched by name or stack and filtered by usage, stack, file access,
	// driver, Docker Manager system and environment (ListCard); who uses
	// it, its driver and whether Docker Manager can open its files (#28:
	// non-local drivers and NFS/CIFS-backed volumes are read-only, with the
	// reason). Docker Manager's own volumes (#32) are marked and never removed.
	// Sizes load separately (the Engine walks the volumes; the manager
	// reuses the answer for a minute), so the list never waits for them.
	import { createQueries, createQuery } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Plus from '@lucide/svelte/icons/plus';
	import { volumeUsageQuery, volumesQuery, type Volume } from '$lib/api/queries';
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
		formatBytes,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import CreateObjectDialog from '$lib/features/resources/CreateObjectDialog.svelte';
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import EnvironmentGaps from '$lib/features/resources/EnvironmentGaps.svelte';
	import ObjectRemoveHost from '$lib/features/resources/ObjectRemoveHost.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import StackBadge from '$lib/features/resources/StackBadge.svelte';
	import {
		applyListFilters,
		isFiltering,
		listSummary,
		volumeFilters,
		volumeSearch
	} from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
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

	// Sizes per environment (volume.read only; the server filters).
	const usage = createQueries(() => ({
		queries: scope.hasAny('volume.read') ? scope.targets.map((t) => volumeUsageQuery(t.id)) : []
	}));
	type Size =
		| { state: 'loading' }
		| { state: 'known'; bytes: number }
		| { state: 'unknown'; reason: string };
	// By environment ID (the whole environment: loading, failed or an
	// older agent) and by `<environment>/<volume>` (a known size).
	const sizes = $derived(
		new Map<string, Size>(
			scope.targets.flatMap((t, i): [string, Size][] => {
				const u = usage[i];
				if (!u) return [];
				if (u.isPending) return [[t.id, { state: 'loading' }]];
				if (u.isError)
					return [
						[
							t.id,
							{
								state: 'unknown',
								reason: 'The sizes could not be computed. They are retried in a minute.'
							}
						]
					];
				if (u.data && !u.data.supported)
					return [
						[
							t.id,
							{
								state: 'unknown',
								reason: 'Upgrade the agent of this environment to see volume sizes.'
							}
						]
					];
				return (u.data?.items ?? []).flatMap((item): [string, Size][] =>
					item.sizeBytes === undefined
						? []
						: [[`${t.id}/${item.name}`, { state: 'known', bytes: item.sizeBytes }]]
				);
			})
		)
	);
	function sizeOf(v: Volume): Size {
		const env = sizes.get(v.environmentId);
		if (env) return env;
		return (
			sizes.get(`${v.environmentId}/${v.name}`) ?? {
				state: 'unknown',
				reason: 'The Engine does not report a size for this volume.'
			}
		);
	}
	const bytesOf = (v: Volume) => {
		const s = sizeOf(v);
		return s.state === 'known' ? s.bytes : null;
	};

	const filters = new ListFilters('volumes');
	let createOpen = $state(false);
	let remover = $state<ObjectRemoveHost>();

	const all = $derived(list.data?.items ?? []);
	const defs = $derived(volumeFilters(all, { envs: scope.single ? [] : scope.targets }));
	const rows = $derived(applyListFilters(all, defs, filters.state, volumeSearch));
	const filtered = $derived(isFiltering(defs, filters.state));
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
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			sortValue: bytesOf,
			numeric: true,
			width: '96px'
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
{#snippet sizeCell(v: Volume)}
	{@const s = sizeOf(v)}
	{#if s.state === 'known'}<span class="nowrap">{formatBytes(s.bytes)}</span>
	{:else if s.state === 'loading'}<span class="muted" title="Computing the size"
			>…<span class="sr-only">Computing the size</span></span
		>
	{:else}<span class="muted" title={s.reason}>—</span>{/if}
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
		description="Ask the owner of this Docker Manager to grant access."
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

		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The volumes could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			<ListCard
				title="All volumes"
				id="volumes"
				summary={list.data
					? listSummary(rows.length, all.length, filtered, 'volume', 'volumes')
					: undefined}
				label="Filter volumes"
				searchLabel="Search volumes"
				placeholder="Search by name"
				filters={defs}
				store={filters}
			>
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
								<NoMatches
									what="volumes"
									icon={HardDrive}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									icon={HardDrive}
									color="teal"
									title="No volumes on {scope.single
										? scope.targets[0]?.name
										: 'your environments'} yet."
									description="Create a volume for data that must outlive its containers, or let a stack create its own."
									level={3}
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
			</ListCard>
		{/if}
	</Page>
{/if}

<style>
	.nowrap {
		white-space: nowrap;
	}

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
