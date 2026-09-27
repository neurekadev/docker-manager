<script lang="ts">
	// Stacks (#22, #7): the Compose stacks of the selected environment, or
	// of every visible one with an environment column. Status is the live
	// Engine state Docker Manager last observed; "Undeployed changes" and the
	// update dot say what needs attention. Searched by name or description
	// and filtered by status, changes and environment (ListCard, kept per
	// list and browser tab). Create and import are shown only with
	// stack.create / stack.import (the server still decides).
	import { createQuery } from '@tanstack/svelte-query';
	import FolderSearch from '@lucide/svelte/icons/folder-search';
	import Layers from '@lucide/svelte/icons/layers';
	import Plus from '@lucide/svelte/icons/plus';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { serviceIcon } from '$lib/design/icons';
	import {
		canAnywhere,
		canInEnvironment,
		serviceCounts,
		stackIcon,
		stackStatus,
		stackTitle
	} from '$lib/features/stacks/model';
	import { stacksQuery, updatePoliciesQuery, type Stack } from '$lib/features/stacks/queries';
	import { stackFilters, stackSearch } from '$lib/features/stacks/filters';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering, listSummary } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import CreateStackDialog from '$lib/features/stacks/CreateStackDialog.svelte';
	import ImportStackDialog from '$lib/features/stacks/ImportStackDialog.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import UpdateStatusBadge from '$lib/features/updates/UpdateStatusBadge.svelte';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		EmptyState,
		ErrorState,
		IconTile,
		Skeleton,
		StatusBadge,
		Table,
		formatRelative,
		type Column
	} from '$lib/ui';

	usePage({ title: 'Stacks', crumbs: [{ label: 'Stacks' }], environmentScoped: true });

	const envId = $derived(environmentSelection.id);
	const stacks = createQuery(() => stacksQuery(envId));
	const envs = createQuery(() => environmentsQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const policies = createQuery(() => updatePoliciesQuery(envId));
	// routes.newStack() opens the create dialog over this list.
	const createDialog = urlDialog('create', ['environment']);
	// routes.importStack() opens the import dialog (discovered projects).
	const importDialog = urlDialog('import', ['environment']);

	const envById = $derived(new Map((envs.data ?? []).map((e) => [e.id, e])));
	const envName = $derived(envId ? (envById.get(envId)?.name ?? 'this environment') : null);
	const updateStates = $derived(
		new Map(
			(policies.data ?? [])
				.filter((p) => p.target.type === 'stack')
				.map((p) => {
					const s = p.summary;
					const status =
						(s?.available ?? 0) > 0
							? 'update_available'
							: (s?.upToDate ?? 0) > 0 &&
								  !(s?.failed || s?.unchecked || s?.quarantined)
								? 'up_to_date'
								: undefined;
					return [p.target.id, status] as const;
				})
		)
	);
	const canCreate = $derived(
		envId
			? canInEnvironment(perms.data, 'stack.create', envId)
			: canAnywhere(perms.data, 'stack.create')
	);
	const canImport = $derived(
		envId
			? canInEnvironment(perms.data, 'stack.import', envId)
			: canAnywhere(perms.data, 'stack.import')
	);

	const filters = new ListFilters('stacks');
	const all = $derived(stacks.data ?? []);
	const defs = $derived(
		stackFilters({
			envs: envId
				? []
				: [...new Set(all.map((s) => s.environmentId))].map((id) => ({
						id,
						name: envById.get(id)?.name ?? id
					})),
			updates: updateStates
		})
	);
	const rows = $derived(applyListFilters(all, defs, filters.state, stackSearch));
	const filtered = $derived(isFiltering(defs, filters.state));

	const columns = $derived.by((): Column<Stack>[] => {
		const cols: Column<Stack>[] = [
			{
				id: 'name',
				header: 'Name',
				cell: nameCell,
				sortValue: (s) => stackTitle(s),
				stack: 'title'
			},
			{
				id: 'status',
				header: 'Status',
				cell: statusCell,
				sortValue: (s) => stackStatus(s),
				stack: 'status',
				width: '160px'
			}
		];
		if (!envId)
			cols.push({
				id: 'environment',
				header: 'Environment',
				cell: envCell,
				sortValue: (s) => envById.get(s.environmentId)?.name ?? '',
				width: '160px'
			});
		cols.push(
			{
				id: 'services',
				header: 'Services running',
				cell: servicesCell,
				sortValue: (s) => serviceCounts(s).servicesRunning,
				numeric: true,
				width: '140px'
			},
			{ id: 'attention', header: 'Changes', cell: attentionCell, width: '220px' },
			{
				id: 'deployed',
				header: 'Last deploy',
				cell: deployedCell,
				sortValue: (s) => s.appliedRevision?.at ?? '',
				width: '140px'
			}
		);
		return cols;
	});
</script>

{#snippet nameCell(s: Stack)}
	{@const icon = stackIcon(s)}
	<a class="name" href={routes.stack(s.id)}>
		<IconTile icon={serviceIcon(icon.icon)} color={icon.color} size="sm" />
		<span class="text">
			<span class="title">{stackTitle(s)}</span>
			{#if s.description}<span class="desc">{s.description}</span
				>{:else if s.displayName}<span class="desc mono">{s.name}</span>{/if}
		</span>
	</a>
{/snippet}
{#snippet statusCell(s: Stack)}<StatusBadge status={stackStatus(s)} />{/snippet}
{#snippet envCell(s: Stack)}
	{@const e = envById.get(s.environmentId)}
	<span class="env">
		{e?.name ?? '—'}
		{#if e && !e.online}<Badge tone="offline" dot>Offline</Badge>{/if}
	</span>
{/snippet}
{#snippet servicesCell(s: Stack)}
	{#if s.view === 'full'}
		{@const c = serviceCounts(s)}
		<span class="num">{c.servicesRunning} / {c.services}</span>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet attentionCell(s: Stack)}
	<span class="chips">
		{#if s.undeployedChanges}
			<a class="chip" href={routes.stack(s.id, 'revisions')}
				><Badge tone="warn" dot>Undeployed changes</Badge></a
			>
		{/if}
		<UpdateStatusBadge status={updateStates.get(s.id)} />
		{#if !s.undeployedChanges && !updateStates.get(s.id)}<span class="muted">—</span>{/if}
	</span>
{/snippet}
{#snippet deployedCell(s: Stack)}
	{#if s.appliedRevision?.at}{formatRelative(s.appliedRevision.at)}{:else}<span class="muted"
			>Never</span
		>{/if}
{/snippet}

<div class="page">
	<header class="head">
		<div>
			<h1>Stacks</h1>
			<p class="muted">
				Compose projects Docker Manager manages{envName ? ` on ${envName}` : ''}. The files
				on disk are the source of truth.
			</p>
		</div>
		<div class="actions">
			{#if canImport}
				<Button icon={FolderSearch} onclick={() => (importDialog.open = true)}
					>Import project</Button
				>
			{/if}
			{#if canCreate}
				<Button variant="primary" icon={Plus} onclick={() => (createDialog.open = true)}
					>Create stack</Button
				>
			{/if}
		</div>
	</header>

	{#if stacks.isError}
		<ErrorState
			error={stacks.error}
			title="The stacks could not be loaded."
			onretry={() => stacks.refetch()}
		/>
	{:else}
		<ListCard
			title="All stacks"
			id="stacks"
			summary={stacks.data
				? listSummary(rows.length, all.length, filtered, 'stack', 'stacks')
				: undefined}
			label="Filter stacks"
			searchLabel="Search stacks"
			placeholder="Search by name or description"
			filters={defs}
			store={filters}
		>
			{#if stacks.isPending}
				<div class="loading" aria-busy="true"><Skeleton lines={5} height="20px" /></div>
			{:else}
				<Table
					label="Stacks"
					{rows}
					{columns}
					rowKey={(s) => s.id}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}
						{#if filtered}
							<NoMatches
								what="stacks"
								icon={Layers}
								onclear={() => filters.clear()}
							/>
						{:else}
							<EmptyState
								icon={Layers}
								color="blue"
								title={envName ? `No stacks on ${envName} yet.` : 'No stacks yet.'}
								description={canCreate || canImport
									? 'Create a stack or import an existing Compose project.'
									: 'Stacks you are given access to appear here.'}
								level={3}
								compact
							>
								{#snippet actions()}
									{#if canCreate}<Button
											variant="primary"
											onclick={() => (createDialog.open = true)}
											>Create stack</Button
										>{/if}
									{#if canImport}<Button
											onclick={() => (importDialog.open = true)}
											>Import project</Button
										>{/if}
								{/snippet}
							</EmptyState>
						{/if}
					{/snippet}
				</Table>
			{/if}
		</ListCard>
	{/if}

	<CreateStackDialog
		bind:open={createDialog.open}
		environmentId={createDialog.param('environment') ?? envId}
	/>
	<ImportStackDialog
		bind:open={importDialog.open}
		environmentId={importDialog.param('environment') ?? envId}
	/>
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-4);
	}

	h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.head p {
		margin-top: 2px;
		font-size: var(--text-control);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.loading {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.name {
		display: flex;
		align-items: center;
		gap: var(--space-3);
		min-width: 0;
		color: inherit;
		text-decoration: none;
	}

	.name:hover .title {
		color: var(--accent-text);
	}

	.text {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.desc {
		color: var(--text-muted);
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.env,
	.chips {
		display: inline-flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.chip {
		display: inline-flex;
		text-decoration: none;
	}
</style>
