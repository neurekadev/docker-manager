<script lang="ts">
	// Build definitions (#33): saved Git builds of the selected environment
	// (or all), re-run on demand. Run opens the build with its live log.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import FileCode from '@lucide/svelte/icons/file-code';
	import Play from '@lucide/svelte/icons/play';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, unwrap } from '$lib/api/client';
	import { buildDefinitionsQuery, queryKeys, type BuildDefinition } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		ConfirmDialog,
		DeniedState,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Skeleton,
		Table,
		errorMessage,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import BuildsHeader from '$lib/features/builds/BuildsHeader.svelte';
	import DefinitionDialog from '$lib/features/builds/DefinitionDialog.svelte';
	import { repoLabel } from '$lib/features/builds/source';
	import Page from '$lib/features/resources/Page.svelte';
	import { idempotencyKey } from '$lib/features/resources/jobs.svelte';
	import { can } from '$lib/features/resources/permissions';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({
		title: 'Build definitions',
		crumbs: [{ label: 'Builds', href: routes.builds() }, { label: 'Definitions' }],
		environmentScoped: true
	});

	const queryClient = useQueryClient();
	const scope = useEnvironmentScope();
	const list = createQuery(() => ({
		...buildDefinitionsQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0
	}));
	const rows = $derived(list.data?.items ?? []);
	const creatable = $derived(
		scope.targets.filter((t) => scope.can('build_definition.manage', t.id))
	);
	const canBuild = $derived(scope.targets.some((t) => scope.can('image.build', t.id)));

	let editOpen = $state(false);
	let editing = $state<BuildDefinition | null>(null);
	let deleting = $state<BuildDefinition | null>(null);
	let deleteOpen = $state(false);
	let runningId = $state<string | null>(null);

	async function run(d: BuildDefinition) {
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
		if (d.view === 'full' && can(d.actions, 'build_definition.manage'))
			out.push({ label: 'Edit…', onSelect: () => ((editing = d), (editOpen = true)) });
		if (d.lastBuildId)
			out.push({
				label: 'Open the last build',
				href: routes.build(d.environmentId, d.lastBuildId)
			});
		if (can(d.actions, 'build_definition.manage'))
			out.push(
				{ separator: true },
				{
					label: 'Delete…',
					tone: 'danger',
					onSelect: () => ((deleting = d), (deleteOpen = true))
				}
			);
		return out;
	}

	const columns: Column<BuildDefinition>[] = $derived([
		{ id: 'name', header: 'Name', cell: nameCell, sortValue: (d) => d.name, stack: 'title' },
		{
			id: 'source',
			header: 'Source',
			cell: sourceCell,
			sortValue: (d) => d.source?.gitUrl ?? ''
		},
		{ id: 'tags', header: 'Produces', cell: tagsCell },
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (d: BuildDefinition) => scope.name(d.environmentId)
					} satisfies Column<BuildDefinition>
				]),
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '150px',
			align: 'end',
			stack: 'actions'
		}
	]);
</script>

{#snippet nameCell(d: BuildDefinition)}
	<div class="name-cell">
		<span class="name">{d.name}</span>
		{#if d.description}<span class="sub">{d.description}</span>{/if}
	</div>
{/snippet}
{#snippet sourceCell(d: BuildDefinition)}
	{#if d.source}
		<div class="name-cell">
			<span class="mono">{repoLabel(d.source.gitUrl)}</span>
			<span class="sub mono"
				>{d.source.ref || 'default branch'}{d.source.contextPath
					? ` / ${d.source.contextPath}`
					: ''}</span
			>
		</div>
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
				icon={Play}
				loading={runningId === d.id}
				onclick={() => run(d)}>Run</Button
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
	bind:open={editOpen}
	definition={editing}
	environments={creatable}
	environmentId={scope.single ? scope.targets[0]?.id : undefined}
/>
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
			environmentId={scope.single ? scope.targets[0]?.id : undefined}
			description="Saved builds you can run again with one click."
		/>
		{#if creatable.length}
			<div class="bar">
				<Button
					variant="secondary"
					icon={Plus}
					onclick={() => ((editing = null), (editOpen = true))}>New definition</Button
				>
			</div>
		{/if}
		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The build definitions could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			<Card padding="none">
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
							<EmptyState
								icon={FileCode}
								color="violet"
								title="No saved builds yet."
								description="Save a Git build as a definition to run it again without filling in the form."
								level={2}
								compact
							>
								{#snippet actions()}
									{#if creatable.length}
										<Button
											variant="primary"
											icon={Plus}
											onclick={() => ((editing = null), (editOpen = true))}
											>New definition</Button
										>
									{/if}
								{/snippet}
							</EmptyState>
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
		gap: 2px;
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.sub {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.tags {
		font-size: var(--text-caption);
		overflow-wrap: anywhere;
	}

	.row-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-2);
	}

	.bar {
		display: flex;
		justify-content: flex-end;
		margin-bottom: calc(-1 * var(--space-2));
	}

	.loading {
		padding: var(--space-5);
	}
</style>
