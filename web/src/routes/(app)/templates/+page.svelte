<script lang="ts">
	// Templates (template registry): every template of this instance and of
	// the template sources it browses, as cards in one list with one set of
	// filters (search, source, tag, status). A source that failed its last
	// sync is named above the list. This instance's templates open their
	// management pages; a source's templates open a read-only page to
	// create stacks from. "New Template" needs template.create (the server
	// decides).
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Archive from '@lucide/svelte/icons/archive';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Plus from '@lucide/svelte/icons/plus';
	import { myPermissionsQuery } from '$lib/api/queries';
	import Page from '$lib/features/common/Page.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering, listSummary } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { canAnywhere } from '$lib/features/stacks/model';
	import CreateTemplateDialog from '$lib/features/templates/CreateTemplateDialog.svelte';
	import TemplateCard from '$lib/features/templates/TemplateCard.svelte';
	import { catalogFilters, catalogHref, catalogSearch } from '$lib/features/templates/model';
	import { templateCatalogQuery, templateRegistriesQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, EmptyState, ErrorState, Notice, PageHeader, Skeleton } from '$lib/ui';

	usePage({ title: 'Templates', crumbs: [{ label: 'Templates' }] });

	const catalog = createQuery(() => templateCatalogQuery());
	const registries = createQuery(() => templateRegistriesQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const createDialog = urlDialog('create');
	const canCreate = $derived(canAnywhere(perms.data, 'template.create'));

	const filters = new ListFilters('template-catalog');
	const all = $derived(catalog.data ?? []);
	const defs = $derived(catalogFilters(all));
	const rows = $derived(
		applyListFilters(all, defs, filters.state, catalogSearch).sort((a, b) =>
			a.name.localeCompare(b.name)
		)
	);
	const filtered = $derived(isFiltering(defs, filters.state));
	const failing = $derived((registries.data ?? []).filter((r) => !r.own && r.status === 'error'));

	// routes.templates(tag) opens the list filtered by that tag.
	$effect(() => {
		const tag = page.url.searchParams.get('tag');
		if (tag) filters.set('tag', tag);
	});

	function toggleTag(tag: string) {
		filters.set('tag', filters.get('tag') === tag ? '' : tag);
	}
</script>

<Page>
	<PageHeader title="Templates">
		{#snippet actions()}
			<Button icon={Archive} href={routes.templateRegistries()}>Template Sources</Button>
			{#if canCreate}
				<Button variant="primary" icon={Plus} onclick={() => (createDialog.open = true)}
					>New Template</Button
				>
			{/if}
		{/snippet}
	</PageHeader>

	{#if failing.length}
		<Notice
			tone="warn"
			live="none"
			title={failing.length === 1
				? `${failing[0].name} could not be synced`
				: `${failing.length} template sources could not be synced`}
		>
			Their templates may be out of date.
			{#snippet actions()}
				<Button size="sm" href={routes.templateRegistries()}>Open Template Sources</Button>
			{/snippet}
		</Notice>
	{/if}

	{#if catalog.isError}
		<ErrorState
			error={catalog.error}
			title="The templates could not be loaded."
			onretry={() => catalog.refetch()}
		/>
	{:else}
		<ListCard
			title="All Templates"
			id="templates"
			summary={catalog.data
				? listSummary(rows.length, all.length, filtered, 'template', 'templates')
				: undefined}
			label="Filter Templates"
			searchLabel="Search Templates"
			placeholder="Search templates"
			filters={defs}
			store={filters}
		>
			{#if catalog.isPending}
				<div class="loading" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
			{:else if rows.length}
				<ul class="grid" aria-label="Templates">
					{#each rows as t (`${t.instanceId}/${t.templateId}`)}
						<li>
							<TemplateCard
								href={catalogHref(t)}
								name={t.name}
								description={t.description}
								tags={t.tags}
								iconUrl={t.iconUrl}
								visibility={t.visibility}
								latest={t.versions[0]?.label}
								source={t.own ? 'This Instance' : t.registryName}
								onTag={toggleTag}
							/>
						</li>
					{/each}
				</ul>
			{:else if filtered}
				<NoMatches what="templates" icon={LayoutTemplate} onclear={() => filters.clear()} />
			{:else}
				<EmptyState
					icon={LayoutTemplate}
					color="violet"
					title="No templates yet."
					description={canCreate
						? 'Create a template or add a template source.'
						: 'Templates you are given access to appear here.'}
					level={3}
					compact
				>
					{#snippet actions()}
						{#if canCreate}<Button
								variant="primary"
								onclick={() => (createDialog.open = true)}>New Template</Button
							>{/if}
						<Button href={routes.templateRegistries()}>Open Template Sources</Button>
					{/snippet}
				</EmptyState>
			{/if}
		</ListCard>
	{/if}

	<CreateTemplateDialog bind:open={createDialog.open} />
</Page>

<style>
	.loading {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	/* The cards fill the row: one template spans the card, three share it. */
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 300px), 1fr));
		gap: var(--space-3);
		margin: 0;
		padding: var(--space-4);
		list-style: none;
	}
</style>
