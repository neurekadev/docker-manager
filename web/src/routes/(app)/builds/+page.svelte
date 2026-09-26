<script lang="ts">
	// Build history (#33): manual Git builds of the selected environment (or
	// all), newest first, with the exact commit each one built, the result
	// and the duration. Running builds refresh until they end.
	import { createQuery } from '@tanstack/svelte-query';
	import Hammer from '@lucide/svelte/icons/hammer';
	import Play from '@lucide/svelte/icons/play';
	import { imageBuildsQuery, type ImageBuild } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		DeniedState,
		EmptyState,
		ErrorState,
		Skeleton,
		StatusBadge,
		Table,
		formatRelative,
		type Column
	} from '$lib/ui';
	import BuildsHeader from '$lib/features/builds/BuildsHeader.svelte';
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import { repoLabel } from '$lib/features/builds/source';
	import Page from '$lib/features/resources/Page.svelte';
	import { compactDuration } from '$lib/features/resources/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Builds', crumbs: [{ label: 'Builds' }], environmentScoped: true });

	const scope = useEnvironmentScope();
	const running = (b: ImageBuild) => b.status === 'queued' || b.status === 'running';
	const list = createQuery(() => ({
		...imageBuildsQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0,
		refetchInterval: (q) => (q.state.data?.items.some(running) ? 5000 : false)
	}));
	const rows = $derived(list.data?.items ?? []);
	const canBuild = $derived(scope.targets.some((t) => scope.can('image.build', t.id)));

	const columns: Column<ImageBuild>[] = $derived([
		{
			id: 'image',
			header: 'Image',
			cell: imageCell,
			sortValue: (b) => b.tags[0] ?? '',
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (b) => b.status,
			width: '130px',
			stack: 'status'
		},
		{ id: 'source', header: 'Source', cell: sourceCell, sortValue: (b) => b.gitUrl },
		{ id: 'commit', header: 'Commit', cell: commitCell, width: '110px' },
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (b: ImageBuild) => scope.name(b.environmentId)
					} satisfies Column<ImageBuild>
				]),
		{
			id: 'duration',
			header: 'Duration',
			cell: durationCell,
			sortValue: (b) => b.durationMs ?? -1,
			numeric: true,
			width: '100px'
		},
		{
			id: 'created',
			header: 'Started',
			cell: createdCell,
			sortValue: (b) => b.createdAt,
			width: '130px'
		}
	]);
</script>

{#snippet imageCell(b: ImageBuild)}
	<div class="name-cell">
		<a class="name mono" href={routes.build(b.environmentId, b.id)}>{b.tags[0] ?? 'Build'}</a>
		{#if b.tags.length > 1}<span class="sub">+{b.tags.length - 1} more names</span>{/if}
	</div>
{/snippet}
{#snippet statusCell(b: ImageBuild)}<StatusBadge status={b.status} kind="job" />{/snippet}
{#snippet sourceCell(b: ImageBuild)}
	<div class="name-cell">
		<span class="mono">{repoLabel(b.gitUrl)}</span>
		<span class="sub mono">{b.resolvedRef ?? b.ref ?? 'default branch'}</span>
	</div>
{/snippet}
{#snippet commitCell(b: ImageBuild)}
	{#if b.resolvedCommit}<span class="mono commit" title={b.resolvedCommit}
			>{b.resolvedCommit.slice(0, 7)}</span
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet envCell(b: ImageBuild)}{scope.name(b.environmentId)}{/snippet}
{#snippet durationCell(b: ImageBuild)}<span class="num">{compactDuration(b.durationMs)}</span
	>{/snippet}
{#snippet createdCell(b: ImageBuild)}<span class="muted" title={b.createdAt}
		>{formatRelative(b.createdAt)}</span
	>{/snippet}

{#if scope.restricted}
	<DeniedState level={1} />
{:else if scope.perms.data && !scope.hasAny('image.build', 'build_definition.')}
	<DeniedState
		level={1}
		title="You don't have access to builds."
		description="Ask the owner of this DockYard to grant access."
	/>
{:else}
	<Page>
		<BuildsHeader
			{canBuild}
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
				{#each list.data.unavailable as u (u.environment.id)}
					<p class="muted">The builds of {u.environment.name} could not be read.</p>
				{/each}
			{/if}
			<Card padding="none">
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
							<EmptyState
								icon={Hammer}
								color="violet"
								title="No builds yet."
								description="Build an image from a Git repository on one of your environments. Compose services with a build section build when their stack deploys."
								level={2}
								compact
							>
								{#snippet actions()}
									{#if canBuild}
										<Button
											variant="primary"
											icon={Play}
											href={routes.newBuild(
												scope.single ? scope.targets[0]?.id : undefined
											)}>Build image</Button
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
		text-decoration: none;
		overflow-wrap: anywhere;
	}

	.name:hover {
		color: var(--accent-text);
	}

	.sub {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.commit {
		color: var(--accent-text);
	}

	.loading {
		padding: var(--space-5);
	}
</style>
