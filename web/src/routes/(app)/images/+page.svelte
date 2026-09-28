<script lang="ts">
	// Images (#6): every image of the selected environment (or all), with
	// its tags and short ID, whether containers use it, its size and age.
	// Tagged images come first; untagged (dangling) ones are folded into a
	// section at the end ("30 untagged images"). Searched by tag, ID,
	// digest or label; an "Unused" switch and the environment filter the
	// list (ListCard). Pull (#19 registry connection preview), tag and
	// remove (in-use check) from here, removal of selected images in bulk
	// (ObjectBulk); builds (#33) have their own page in the navigation.
	import { createQuery } from '@tanstack/svelte-query';
	import Box from '@lucide/svelte/icons/box';
	import Download from '@lucide/svelte/icons/download';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import { imagesQuery, type Image } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		formatDateTime,
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
	import ObjectBulk from '$lib/features/resources/ObjectBulk.svelte';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import Page from '$lib/features/resources/Page.svelte';
	import ProtectionMark from '$lib/features/resources/ProtectionMark.svelte';
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
	import { shortDigest, splitUntagged } from '$lib/features/resources/model';
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
	const groups = $derived(splitUntagged(rows));
	const untaggedSize = $derived(groups.untagged.reduce((n, im) => n + (im.size ?? 0), 0));
	let selected = $state<string[]>([]);
	// Bulk removal runs on the selected rows the filters still show.
	const chosen = $derived(rows.filter((im) => selected.includes(key(im))));
	const tracker = new ChangeTracker<Image>(key, (im) => `${im.repoTags.join(',')}/${im.inUse}`);
	$effect(() => {
		if (list.data) tracker.update(list.data.items);
	});

	function menu(im: Image): MenuEntry[] {
		const out: MenuEntry[] = [{ label: 'Open', href: routes.image(im.environmentId, im.id) }];
		if (can(im.actions, 'image.tag'))
			out.push({ label: 'Tag…', onSelect: () => host?.request(im, 'tag') });
		if (im.repoTags[0] && scope.can('container.create', im.environmentId))
			out.push({
				label: 'Create container',
				href: routes.newContainer(im.environmentId, im.repoTags[0])
			});
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
			sortValue: (im) => im.repoTags[0] ?? shortDigest(im.id),
			maxWidth: '360px',
			title: (im) => im.repoTags.join('\n') || im.id,
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
		...(scope.single
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (im: Image) => scope.name(im.environmentId),
						stack: 'hidden'
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

{#snippet refCell(im: Image)}
	<div class="name-cell">
		<span class="title-line">
			<a class="name mono" href={routes.image(im.environmentId, im.id)}
				>{im.repoTags[0] ?? shortDigest(im.id)}</a
			>
			{#if im.protection}<ProtectionMark protection={im.protection} />{/if}
		</span>
		{#if im.repoTags.length > 1}
			<span class="sub">+{im.repoTags.length - 1} more tags</span>
		{:else if im.repoTags.length === 0}
			<span class="sub">Untagged</span>
		{/if}
	</div>
{/snippet}
{#snippet useCell(im: Image)}
	{#if im.usedBy?.length}
		<Badge tone="ok" dot>{im.usedBy.length} container{im.usedBy.length === 1 ? '' : 's'}</Badge>
	{:else if im.inUse}<Badge tone="ok" dot>In use</Badge>
	{:else}<Badge>Unused</Badge>{/if}
{/snippet}
{#snippet envCell(im: Image)}{scope.name(im.environmentId)}{/snippet}
{#snippet sizeCell(im: Image)}<span class="num">{im.size ? formatBytes(im.size) : '—'}</span
	>{/snippet}
{#snippet createdCell(im: Image)}
	{#if im.createdAt}<span class="muted" title={formatDateTime(im.createdAt)}
			>{formatRelative(im.createdAt)}</span
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
				placeholder="Search tag, ID or label"
				filters={defs}
				store={filters}
			>
				<ObjectBulk
					selected={{ kind: 'image', items: chosen }}
					environmentName={(id) => scope.name(id)}
					onclear={() => (selected = [])}
				/>
				{#if !list.data}
					<div class="loading" aria-busy="true"><Skeleton lines={6} height="20px" /></div>
				{:else if groups.tagged.length || !groups.untagged.length}
					<Table
						label="Images"
						rows={groups.tagged}
						{columns}
						rowKey={key}
						changed={tracker.changed}
						sort={{ column: 'ref', direction: 'asc' }}
						selectable
						bind:selected
						rowLabel={(im) => `Select ${im.repoTags[0] ?? shortDigest(im.id)}`}
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
				{#if list.data && groups.untagged.length}
					<div class="untagged" class:alone={!groups.tagged.length}>
						<Disclosure
							summary="{groups.untagged.length} untagged {groups.untagged.length === 1
								? 'image'
								: 'images'}, {formatBytes(untaggedSize)}"
							open={!groups.tagged.length}
						>
							<p class="hint">
								Older versions whose tag moved to a newer image. Unused ones are
								safe to remove.
							</p>
							<Table
								label="Untagged images"
								rows={groups.untagged}
								{columns}
								rowKey={key}
								changed={tracker.changed}
								sort={{ column: 'created', direction: 'desc' }}
								selectable
								bind:selected
								rowLabel={(im) => `Select ${shortDigest(im.id)}`}
							/>
						</Disclosure>
					</div>
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

	.title-line {
		display: flex;
		align-items: center;
		gap: var(--space-1);
		min-width: 0;
	}

	.name {
		overflow: hidden;
		color: var(--text-strong);
		text-decoration: none;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.name:hover {
		color: var(--accent-text);
	}

	.sub {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.untagged {
		padding: var(--space-3) var(--space-4);
		border-top: 1px solid var(--border-subtle);
	}

	.untagged.alone {
		border-top: 0;
	}

	/* The untagged table spans the card like the main one. */
	.untagged :global(.scroll) {
		margin: 0 calc(-1 * var(--space-4));
	}

	.hint {
		margin: 0;
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.loading {
		padding: var(--space-5);
	}
</style>
