<script lang="ts">
	// Build history (#33): manual Git builds of the selected environment (or
	// all), newest first, with the exact commit each one built, the result
	// and the duration. One ListCard: a search (image names, repository,
	// ref, commit) and filters for the result and, with several
	// environments, the environment. Running builds refresh until they end;
	// environments whose builds can't be read are named in a notice.
	import { createQuery } from '@tanstack/svelte-query';
	import Play from '@lucide/svelte/icons/play';
	import { imageBuildsQuery, type ImageBuild } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		DeniedState,
		EmptyState,
		ErrorState,
		Notice,
		Skeleton,
		StatusBadge,
		Table,
		formatDateTime,
		formatDuration,
		formatRelative,
		type Column
	} from '$lib/ui';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import BuildsHeader from '$lib/features/builds/BuildsHeader.svelte';
	import { buildFilters, buildSearch } from '$lib/features/builds/filters';
	import { repoLabel } from '$lib/features/builds/source';
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import { applyListFilters, isFiltering, listSummary } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Builds', crumbs: [{ label: 'Builds' }], environmentScoped: true });

	const scope = useEnvironmentScope();
	const filters = new ListFilters('builds');
	const running = (b: ImageBuild) => b.status === 'queued' || b.status === 'running';
	const list = createQuery(() => ({
		...imageBuildsQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0,
		refetchInterval: (q) => (q.state.data?.items.some(running) ? 5000 : false)
	}));
	const all = $derived(list.data?.items ?? []);
	const canBuild = $derived(scope.targets.some((t) => scope.can('image.build', t.id)));
	const canDefine = $derived(
		scope.targets.some((t) => scope.can('build_definition.manage', t.id))
	);
	const defs = $derived(
		buildFilters({
			envs: scope.single ? [] : scope.targets.map((t) => ({ id: t.id, name: t.name }))
		})
	);
	const rows = $derived(
		applyListFilters(
			all,
			defs,
			filters.state,
			buildSearch((id) => scope.name(id))
		)
	);
	const filtered = $derived(isFiltering(defs, filters.state));

	const columns: Column<ImageBuild>[] = $derived([
		{
			id: 'image',
			header: 'Image',
			cell: imageCell,
			sortValue: (b) => b.tags[0] ?? '',
			maxWidth: '360px',
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Result',
			cell: statusCell,
			sortValue: (b) => b.status,
			width: '130px',
			stack: 'status'
		},
		{
			id: 'source',
			header: 'Source',
			cell: sourceCell,
			sortValue: (b) => b.gitUrl,
			maxWidth: '320px',
			stack: 'meta'
		},
		{ id: 'commit', header: 'Commit', cell: commitCell, width: '110px', stack: 'hidden' },
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (b: ImageBuild) => scope.name(b.environmentId),
						stack: 'meta'
					} satisfies Column<ImageBuild>
				]),
		{
			id: 'duration',
			header: 'Duration',
			cell: durationCell,
			sortValue: (b) => b.durationMs ?? -1,
			numeric: true,
			width: '110px',
			stack: 'hidden'
		},
		{
			id: 'created',
			header: 'Started',
			cell: createdCell,
			sortValue: (b) => b.createdAt,
			width: '130px',
			stack: 'status'
		}
	]);
</script>

{#snippet imageCell(b: ImageBuild)}<NameCell
		icon="build"
		name={b.tags[0] ?? 'Build'}
		href={routes.build(b.environmentId, b.id)}
		mono
		sub={b.tags.length > 1 ? `+${b.tags.length - 1} more names` : undefined}
	/>{/snippet}
{#snippet statusCell(b: ImageBuild)}<StatusBadge status={b.status} kind="job" />{/snippet}
{#snippet sourceCell(b: ImageBuild)}<NameCell
		name={repoLabel(b.gitUrl)}
		mono
		sub={b.resolvedRef ?? b.ref ?? 'Default branch'}
		subMono
	/>{/snippet}
{#snippet commitCell(b: ImageBuild)}
	{#if b.resolvedCommit}<span class="mono commit" title={b.resolvedCommit}
			>{b.resolvedCommit.slice(0, 7)}</span
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet envCell(b: ImageBuild)}{scope.name(b.environmentId)}{/snippet}
{#snippet durationCell(b: ImageBuild)}<span class="num"
		>{b.durationMs !== undefined ? formatDuration(b.durationMs / 1000) : '—'}</span
	>{/snippet}
{#snippet createdCell(b: ImageBuild)}<span class="muted" title={formatDateTime(b.createdAt)}
		>{formatRelative(b.createdAt)}</span
	>{/snippet}

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
			{canDefine}
			environmentId={scope.single ? scope.targets[0]?.id : undefined}
			description="Images built from Git repositories on {scope.single
				? scope.targets[0]?.name
				: 'your environments'}."
		>
			{#snippet extra()}<PruneButton target="build_cache" {scope} />{/snippet}
		</BuildsHeader>
		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The builds could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			{#if list.data?.unavailable.length}
				<Notice tone="warn" title="Some builds are missing" live="none">
					The builds of {list.data.unavailable.map((u) => u.environment.name).join(', ')} could
					not be read. Try again in a moment.
				</Notice>
			{/if}
			<ListCard
				title="All Builds"
				id="builds"
				summary={list.data
					? listSummary(rows.length, all.length, filtered, 'build', 'builds')
					: undefined}
				label="Filter Builds"
				searchLabel="Search Builds"
				placeholder="Search builds"
				filters={defs}
				store={filters}
			>
				{#if !list.data}
					<div class="loading" aria-busy="true"><Skeleton lines={5} height="20px" /></div>
				{:else}
					<Table
						label="Builds"
						{rows}
						{columns}
						rowKey={(b) => b.id}
						sort={{ column: 'created', direction: 'desc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="builds"
									icon={resourceIcon('build').icon}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									{...resourceIcon('build')}
									title="No builds yet."
									description="Build an image from a Git repository on one of your environments. Compose services with a build section build when their stack deploys."
									level={3}
									compact
								>
									{#snippet actions()}
										{#if canBuild}
											<Button
												variant="primary"
												icon={Play}
												href={routes.newBuild(
													scope.single ? scope.targets[0]?.id : undefined
												)}>Build Image</Button
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
	.commit {
		color: var(--accent-text);
	}

	.loading {
		padding: var(--space-5);
	}
</style>
