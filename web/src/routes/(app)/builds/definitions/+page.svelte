<script lang="ts">
	// Build definitions (#33): saved Git builds of the selected environment
	// (or all), built again on demand. One ListCard (search, and the
	// environment filter with several environments). A definition's name
	// opens it in the edit dialog (?edit=<id>, routes.buildDefinitionEdit);
	// "New definition" in the header opens the create dialog (?create=1).
	// Build opens the build with its live log.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import FileCode from '@lucide/svelte/icons/file-code';
	import Hammer from '@lucide/svelte/icons/hammer';
	import { api, unwrap } from '$lib/api/client';
	import { buildDefinitionsQuery, queryKeys, type BuildDefinition } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		ConfirmDialog,
		DeniedState,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Notice,
		Skeleton,
		Table,
		errorMessage,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import BuildsHeader from '$lib/features/builds/BuildsHeader.svelte';
	import DefinitionDialog from '$lib/features/builds/DefinitionDialog.svelte';
	import { definitionFilters, definitionSearch } from '$lib/features/builds/filters';
	import { repoLabel } from '$lib/features/builds/source';
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import { applyListFilters, isFiltering, listSummary } from '$lib/features/resources/filters';
	import { idempotencyKey } from '$lib/features/resources/jobs.svelte';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { can } from '$lib/features/resources/permissions';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({
		title: 'Build definitions',
		crumbs: [{ label: 'Builds', href: routes.builds() }, { label: 'Definitions' }],
		environmentScoped: true
	});

	const queryClient = useQueryClient();
	const scope = useEnvironmentScope();
	const filters = new ListFilters('build-definitions');
	const list = createQuery(() => ({
		...buildDefinitionsQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0
	}));
	const all = $derived(list.data?.items ?? []);
	const creatable = $derived(
		scope.targets.filter((t) => scope.can('build_definition.manage', t.id))
	);
	const canBuild = $derived(scope.targets.some((t) => scope.can('image.build', t.id)));
	const defs = $derived(
		definitionFilters({
			envs: scope.single ? [] : scope.targets.map((t) => ({ id: t.id, name: t.name }))
		})
	);
	const rows = $derived(
		applyListFilters(
			all,
			defs,
			filters.state,
			definitionSearch((id) => scope.name(id))
		)
	);
	const filtered = $derived(isFiltering(defs, filters.state));

	// The dialogs live in the URL: ?create=1 and ?edit=<definition ID>.
	const createDialog = urlDialog('create');
	const editDialog = urlDialog('edit');
	const editId = $derived(editDialog.param('edit'));
	const editing = $derived(editId ? (all.find((d) => d.id === editId) ?? null) : null);
	const editable = (d: BuildDefinition) =>
		d.view === 'full' && can(d.actions, 'build_definition.manage');
	let deleting = $state<BuildDefinition | null>(null);
	let deleteOpen = $state(false);
	let runningId = $state<string | null>(null);

	async function build(d: BuildDefinition) {
		runningId = d.id;
		try {
			const job = await unwrap(
				api.POST(
					'/api/v1/environments/{environmentId}/build-definitions/{definitionId}/runs',
					{
						params: {
							path: { environmentId: d.environmentId, definitionId: d.id },
							header: { 'Idempotency-Key': idempotencyKey() }
						}
					}
				)
			);
			toast.info(`Building ${d.name}`);
			void queryClient.invalidateQueries({ queryKey: queryKeys.images.all });
			await goto(routes.build(d.environmentId, job.id));
		} catch (e) {
			toast.error(`${d.name} couldn't be built.`, { body: errorMessage(e) });
		} finally {
			runningId = null;
		}
	}

	async function remove() {
		const d = deleting!;
		await unwrap(
			api.DELETE('/api/v1/environments/{environmentId}/build-definitions/{definitionId}', {
				params: {
					path: { environmentId: d.environmentId, definitionId: d.id },
					header: { 'If-Match': `"${d.revision ?? 0}"` }
				}
			})
		);
		toast.success(`Deleted ${d.name}`);
		void queryClient.invalidateQueries({ queryKey: queryKeys.images.all });
	}

	function menu(d: BuildDefinition): MenuEntry[] {
		const out: MenuEntry[] = [];
		if (editable(d)) out.push({ label: 'Edit', href: routes.buildDefinitionEdit(d.id) });
		if (d.lastBuildId)
			out.push({
				label: 'Open the last build',
				href: routes.build(d.environmentId, d.lastBuildId)
			});
		if (can(d.actions, 'build_definition.manage'))
			out.push(
				{ separator: true },
				{
					label: 'Delete',
					tone: 'danger',
					onSelect: () => ((deleting = d), (deleteOpen = true))
				}
			);
		return out;
	}

	const columns: Column<BuildDefinition>[] = $derived([
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (d) => d.name,
			maxWidth: '320px',
			stack: 'title'
		},
		{
			id: 'source',
			header: 'Source',
			cell: sourceCell,
			sortValue: (d) => d.source?.gitUrl ?? '',
			maxWidth: '320px',
			stack: 'meta'
		},
		{
			id: 'tags',
			header: 'Produces',
			cell: tagsCell,
			maxWidth: '320px',
			truncate: true,
			title: (d) => d.source?.tags.join('\n'),
			stack: 'meta'
		},
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (d: BuildDefinition) => scope.name(d.environmentId),
						stack: 'meta'
					} satisfies Column<BuildDefinition>
				]),
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '150px',
			align: 'end',
			pin: 'end',
			stack: 'actions'
		}
	]);
</script>

{#snippet nameCell(d: BuildDefinition)}<NameCell
		name={d.name}
		href={editable(d)
			? routes.buildDefinitionEdit(d.id)
			: d.lastBuildId
				? routes.build(d.environmentId, d.lastBuildId)
				: undefined}
		sub={d.description}
	/>{/snippet}
{#snippet sourceCell(d: BuildDefinition)}
	{#if d.source}
		<NameCell
			name={repoLabel(d.source.gitUrl)}
			mono
			sub="{d.source.ref || 'Default branch'}{d.source.contextPath
				? ` / ${d.source.contextPath}`
				: ''}"
			subMono
		/>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet tagsCell(d: BuildDefinition)}
	{#if d.source?.tags.length}<span class="mono tags">{d.source.tags.join(', ')}</span>{:else}<span
			class="muted">—</span
		>{/if}
{/snippet}
{#snippet envCell(d: BuildDefinition)}{scope.name(d.environmentId)}{/snippet}
{#snippet actionsCell(d: BuildDefinition)}
	<div class="row-actions">
		{#if can(d.actions, 'image.build')}
			<Button
				size="sm"
				variant="secondary"
				icon={Hammer}
				loading={runningId === d.id}
				onclick={() => build(d)}>Build</Button
			>
		{/if}
		{#if menu(d).length}
			<Menu items={menu(d)} label="Actions for {d.name}" align="end">
				{#snippet trigger(props)}
					<IconButton {...props} icon={Ellipsis} label="Actions for {d.name}" size="sm" />
				{/snippet}
			</Menu>
		{/if}
	</div>
{/snippet}

<DefinitionDialog
	bind:open={() => createDialog.open, (v) => (createDialog.open = v)}
	environments={creatable}
	environmentId={scope.single ? scope.targets[0]?.id : undefined}
/>
{#if editing}
	<DefinitionDialog
		bind:open={() => editDialog.open, (v) => (editDialog.open = v)}
		definition={editing}
		environments={creatable}
	/>
{/if}
{#if deleting}
	<ConfirmDialog
		bind:open={deleteOpen}
		title="Delete {deleting.name}?"
		consequences={['The saved build is deleted; its past builds and their images stay.']}
		confirmLabel="Delete definition"
		tone="danger"
		onconfirm={remove}
	/>
{/if}

{#if scope.restricted}
	<DeniedState level={1} />
{:else if scope.perms.data && !scope.hasAny('image.build', 'build_definition.')}
	<DeniedState
		level={1}
		title="You don't have access to builds."
		description="Ask the owner of this Docker Manager to grant access."
	/>
{:else}
	<Page>
		<BuildsHeader
			{canBuild}
			canDefine={creatable.length > 0}
			onnewdefinition={() => (createDialog.open = true)}
			environmentId={scope.single ? scope.targets[0]?.id : undefined}
			description="Saved builds you can build again with one click."
		>
			{#snippet extra()}<PruneButton target="build_cache" {scope} />{/snippet}
		</BuildsHeader>
		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The build definitions could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			{#if editId && list.data && !editing}
				<Notice tone="info" title="This definition no longer exists" live="none">
					It was deleted, or it belongs to an environment that isn't shown.
				</Notice>
			{/if}
			<ListCard
				title="All definitions"
				id="build-definitions"
				summary={list.data
					? listSummary(rows.length, all.length, filtered, 'definition', 'definitions')
					: undefined}
				label="Filter build definitions"
				searchLabel="Search definitions"
				placeholder="Search definitions"
				filters={defs}
				store={filters}
			>
				{#if !list.data}
					<div class="loading" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
				{:else}
					<Table
						label="Build definitions"
						{rows}
						{columns}
						rowKey={(d) => d.id}
						sort={{ column: 'name', direction: 'asc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="definitions"
									icon={FileCode}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									icon={FileCode}
									color="violet"
									title="No saved builds yet."
									description="Save a Git build as a definition to build it again without filling in the form. Use New definition above, or save one when you build an image."
									level={3}
									compact
								/>
							{/if}
						{/snippet}
					</Table>
				{/if}
			</ListCard>
		{/if}
	</Page>
{/if}

<style>
	.tags {
		font-size: var(--text-caption);
	}

	.row-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-2);
	}

	.loading {
		padding: var(--space-5);
	}
</style>
