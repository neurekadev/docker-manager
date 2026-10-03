<script lang="ts">
	// Volumes (#6): every volume of the selected environment (or all),
	// searched by name, stack, driver, container or label and filtered by
	// driver, stack and environment, with "Unused" and "Managed" (Docker
	// Manager's stacks and its own) switches (ListCard); who uses it, its
	// stack, driver (hidden while every volume has the same), size and age.
	// Volumes whose files Docker Manager cannot open carry a "Read-Only"
	// tag with the reason (#28: non-local drivers and NFS/CIFS-backed
	// volumes). Docker Manager's own volumes (#32) carry the shield mark
	// and are never removed. Marks stay on the name's line, so every row
	// has the same height. Selected volumes can be removed in bulk.
	// Sizes load separately (the Engine walks the volumes; the manager
	// reuses the answer for a minute), so the list never waits for them.
	import { createQueries, createQuery } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
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
		volumeFilters,
		volumeSearch
	} from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { ChangeTracker } from '$lib/features/resources/changes.svelte';
	import { sameEverywhere, volumeAccess } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';
	import { onlyOneEnvironment } from '$lib/features/common/data';

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
	let selected = $state<string[]>([]);
	// Bulk removal runs on the selected rows the filters still show.
	const chosen = $derived(rows.filter((v) => selected.includes(key(v))));
	// A column that says "local" on every row says nothing.
	const oneDriver = $derived(sameEverywhere(all, (v) => v.driver ?? 'local'));
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
				label: 'Browse Files',
				href: routes.volume(v.environmentId, v.name, 'files')
			});
		if (
			can(v.actions, 'volume.migrate') &&
			!v.protection &&
			!onlyOneEnvironment(scope.envs.data)
		)
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
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (v) => v.name,
			maxWidth: '320px',
			title: (v) => v.name,
			stack: 'title'
		},
		{
			id: 'use',
			header: 'Used By',
			cell: useCell,
			sortValue: (v) => v.usedBy?.length ?? (v.inUse ? 1 : 0),
			width: '170px',
			stack: 'status'
		},
		{
			id: 'stack',
			header: 'Stack',
			cell: stackCell,
			sortValue: (v) => v.stack?.project ?? '',
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
						sortValue: (v: Volume) => scope.name(v.environmentId),
						stack: 'hidden'
					} satisfies Column<Volume>
				]),
		...(oneDriver
			? []
			: [
					{
						id: 'driver',
						header: 'Driver',
						cell: driverCell,
						sortValue: (v: Volume) => v.driver ?? '',
						width: '110px',
						stack: 'hidden'
					} satisfies Column<Volume>
				]),
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			sortValue: bytesOf,
			numeric: true,
			width: '96px'
		},
		{
			id: 'created',
			header: 'Created',
			cell: createdCell,
			sortValue: (v) => v.createdAt ?? '',
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

{#snippet nameCell(v: Volume)}
	{@const access = volumeAccess(v)}
	<IconCell icon="volume"
		><div class="name-cell">
			<a class="name mono" href={routes.volume(v.environmentId, v.name)}>{v.name}</a>
			{#if v.protection}<ProtectionMark protection={v.protection} />{/if}
			{#if !access.local}<span class="tag" title={access.reason}
					><Badge tone="warn">Read-Only</Badge><span class="sr-only"
						>: {access.reason}</span
					></span
				>{/if}
		</div></IconCell
	>
{/snippet}
{#snippet useCell(v: Volume)}
	{#if v.usedBy?.length}
		<span class="users" title={v.usedBy.map((c) => c.name).join(', ')}>
			<Badge tone="ok" dot
				>{v.usedBy.length} container{v.usedBy.length === 1 ? '' : 's'}</Badge
			>
		</span>
	{:else if v.inUse}<Badge tone="ok" dot>In Use</Badge>
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
{#snippet stackCell(v: Volume)}
	{#if v.stack}<StackBadge stack={v.stack} />{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet driverCell(v: Volume)}<span class="mono">{v.driver ?? '—'}</span>{/snippet}
{#snippet envCell(v: Volume)}{scope.name(v.environmentId)}{/snippet}
{#snippet createdCell(v: Volume)}
	{#if v.createdAt}<span class="muted" title={formatDateTime(v.createdAt)}
			>{formatRelative(v.createdAt)}</span
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
		<PageHeader title="Volumes">
			{#snippet actions()}
				<PruneButton target="volumes" {scope} />
				{#if creatable.length}
					<Button variant="primary" icon={Plus} onclick={() => (createOpen = true)}
						>Create Volume</Button
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
				title="All Volumes"
				id="volumes"
				summary={list.data
					? listSummary(rows.length, all.length, filtered, 'volume', 'volumes')
					: undefined}
				label="Filter Volumes"
				searchLabel="Search Volumes"
				placeholder="Search name, stack or driver"
				filters={defs}
				store={filters}
			>
				<ObjectBulk
					selected={{ kind: 'volume', items: chosen }}
					environmentName={(id) => scope.name(id)}
					onclear={() => (selected = [])}
				/>
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
						selectable
						bind:selected
						rowLabel={(v) => `Select ${v.name}`}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="volumes"
									icon={resourceIcon('volume').icon}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									{...resourceIcon('volume')}
									title="No volumes on {scope.single
										? scope.targets[0]?.name
										: 'your environments'} yet."
									description="Create a volume, or let a stack create its own."
									level={3}
									compact
								>
									{#snippet actions()}
										{#if creatable.length}
											<Button
												variant="primary"
												icon={Plus}
												onclick={() => (createOpen = true)}
												>Create Volume</Button
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
