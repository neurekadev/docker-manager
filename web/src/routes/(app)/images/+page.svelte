<script lang="ts">
	// Images (#6): every image of the selected environment (or all), with
	// its tags, size, and whether containers use it; untagged (dangling)
	// images are marked. Searched by tag, ID or digest and filtered by
	// usage, tags, Docker Manager system and environment (ListCard). Pull
	// (#19 registry connection preview), tag and remove (in-use check) from
	// here; builds (#33) have their own page.
	import { createQuery } from '@tanstack/svelte-query';
	import Box from '@lucide/svelte/icons/box';
	import Download from '@lucide/svelte/icons/download';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Hammer from '@lucide/svelte/icons/hammer';
	import { imagesQuery, type Image } from '$lib/api/queries';
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
	import PruneButton from '$lib/features/maintenance/PruneButton.svelte';
	import EnvironmentGaps from '$lib/features/resources/EnvironmentGaps.svelte';
	import ImageActionHost from '$lib/features/resources/ImageActionHost.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionBadge from '$lib/features/resources/ProtectionBadge.svelte';
	import PullImageDialog from '$lib/features/resources/PullImageDialog.svelte';
	import { ChangeTracker } from '$lib/features/resources/changes.svelte';
	import {
		applyListFilters,
		imageFilters,
		imageSearch,
		isFiltering,
		listSummary
	} from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { shortDigest } from '$lib/features/resources/model';
	import { can } from '$lib/features/resources/permissions';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Images', crumbs: [{ label: 'Images' }], environmentScoped: true });

	const scope = useEnvironmentScope();
	const list = createQuery(() => ({
		...imagesQuery(scope.targets),
		enabled: scope.ready && scope.targets.length > 0
	}));

	const filters = new ListFilters('images');
	let pullOpen = $state(false);
	let host = $state<ImageActionHost>();

	const all = $derived(list.data?.items ?? []);
	const defs = $derived(imageFilters({ envs: scope.single ? [] : scope.targets }));
	const rows = $derived(applyListFilters(all, defs, filters.state, imageSearch));
	const filtered = $derived(isFiltering(defs, filters.state));
	const pullable = $derived(scope.creatable('image.pull'));
	const totalSize = $derived(rows.reduce((n, im) => n + (im.size ?? 0), 0));
	const key = (im: Image) => `${im.environmentId}/${im.id}`;
	const tracker = new ChangeTracker<Image>(key, (im) => `${im.repoTags.join(',')}/${im.inUse}`);
	$effect(() => {
		if (list.data) tracker.update(list.data.items);
	});

	function menu(im: Image): MenuEntry[] {
		const out: MenuEntry[] = [{ label: 'Open', href: routes.image(im.environmentId, im.id) }];
		if (can(im.actions, 'image.tag'))
			out.push({ label: 'Tag…', onSelect: () => host?.request(im, 'tag') });
		if (can(im.actions, 'image.remove'))
			out.push(
				{ separator: true },
				{ label: 'Remove…', tone: 'danger', onSelect: () => host?.request(im, 'remove') }
			);
		return out;
	}

	const columns: Column<Image>[] = $derived([
		{
			id: 'ref',
			header: 'Image',
			cell: refCell,
			sortValue: (im) => im.repoTags[0] ?? '~',
			stack: 'title'
		},
		{
			id: 'use',
			header: 'Used by',
			cell: useCell,
			sortValue: (im) => im.usedBy?.length ?? 0,
			width: '150px',
			stack: 'status'
		},
		{ id: 'id', header: 'ID', cell: idCell, width: '140px' },
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (im: Image) => scope.name(im.environmentId)
					} satisfies Column<Image>
				]),
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			sortValue: (im) => im.size ?? 0,
			numeric: true,
			width: '110px'
		},
		{
			id: 'created',
			header: 'Created',
			cell: createdCell,
			sortValue: (im) => im.createdAt ?? '',
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

{#snippet refCell(im: Image)}
	<div class="name-cell">
		<a class="name mono" href={routes.image(im.environmentId, im.id)}
			>{im.repoTags[0] ?? shortDigest(im.id)}</a
		>
		{#if im.repoTags.length > 1}<span class="sub">+{im.repoTags.length - 1} more tags</span
			>{/if}
		{#if im.repoTags.length === 0 || im.protection}
			<span class="tags">
				{#if im.repoTags.length === 0}<Badge tone="warn">Untagged</Badge>{/if}
				{#if im.protection}<ProtectionBadge protection={im.protection} />{/if}
			</span>
		{/if}
	</div>
{/snippet}
{#snippet useCell(im: Image)}
	{#if im.usedBy?.length}
		<Badge tone="ok" dot>{im.usedBy.length} container{im.usedBy.length === 1 ? '' : 's'}</Badge>
	{:else if im.inUse}<Badge tone="ok" dot>In use</Badge>
	{:else}<Badge>Unused</Badge>{/if}
{/snippet}
{#snippet idCell(im: Image)}<span class="mono muted" title={im.id}>{shortDigest(im.id)}</span
	>{/snippet}
{#snippet envCell(im: Image)}{scope.name(im.environmentId)}{/snippet}
{#snippet sizeCell(im: Image)}<span class="num">{im.size ? formatBytes(im.size) : '—'}</span
	>{/snippet}
{#snippet createdCell(im: Image)}
	{#if im.createdAt}<span class="muted" title={im.createdAt}>{formatRelative(im.createdAt)}</span
		>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionsCell(im: Image)}
	<Menu items={menu(im)} label="Actions for {im.repoTags[0] ?? shortDigest(im.id)}" align="end">
		{#snippet trigger(props)}
			<IconButton
				{...props}
				icon={Ellipsis}
				label="Actions for {im.repoTags[0] ?? shortDigest(im.id)}"
				size="sm"
			/>
		{/snippet}
	</Menu>
{/snippet}

<ImageActionHost bind:this={host} environmentName={(id) => scope.name(id)} />
<PullImageDialog
	bind:open={pullOpen}
	environments={pullable}
	environmentId={scope.single ? scope.targets[0]?.id : undefined}
/>

{#if scope.restricted}
	<DeniedState level={1} />
{:else if scope.perms.data && !scope.hasAny('image.')}
	<DeniedState
		level={1}
		title="You don't have access to images."
		description="Ask the owner of this Docker Manager to grant access."
	/>
{:else}
	<Page>
		<PageHeader
			title="Images"
			description="Images on {scope.single
				? scope.targets[0]?.name
				: 'your environments'}, with the containers that use them."
		>
			{#snippet actions()}
				<PruneButton target="images" {scope} />
				{#if scope.hasAny('image.build')}
					<Button variant="secondary" icon={Hammer} href={routes.builds()}>Builds</Button>
				{/if}
				{#if pullable.length}
					<Button variant="primary" icon={Download} onclick={() => (pullOpen = true)}
						>Pull image</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if list.data}
			<EnvironmentGaps
				unavailable={list.data.unavailable}
				what="images"
				single={scope.single}
			/>
		{/if}

		{#if list.isError}
			<ErrorState
				error={list.error}
				title="The images could not be loaded."
				onretry={() => list.refetch()}
			/>
		{:else}
			<ListCard
				title="All images"
				id="images"
				summary={list.data
					? `${listSummary(rows.length, all.length, filtered, 'image', 'images')}, ${formatBytes(totalSize)}`
					: undefined}
				label="Filter images"
				searchLabel="Search images"
				placeholder="Search by tag or ID"
				filters={defs}
				store={filters}
			>
				{#if !list.data}
					<div class="loading" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
				{:else}
					<Table
						label="Images"
						{rows}
						{columns}
						rowKey={key}
						changed={tracker.changed}
						sort={{ column: 'ref', direction: 'asc' }}
					>
						{#snippet empty()}
							{#if filtered}
								<NoMatches
									what="images"
									icon={Box}
									onclear={() => filters.clear()}
								/>
							{:else}
								<EmptyState
									icon={Box}
									color="blue"
									title="No images on {scope.single
										? scope.targets[0]?.name
										: 'your environments'} yet."
									description="Pull an image from a registry, or build one from a Git repository."
									level={3}
									compact
								>
									{#snippet actions()}
										{#if pullable.length}
											<Button
												variant="primary"
												icon={Download}
												onclick={() => (pullOpen = true)}>Pull image</Button
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

	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1);
		margin-top: 2px;
	}

	.loading {
		padding: var(--space-5);
	}
</style>
