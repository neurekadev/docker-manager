<script lang="ts">
	// Templates (template registry): discover and manage stack templates.
	// Browse by tag, search and filter this instance's templates, shown as
	// cards; open one to edit its draft, publish versions and change its
	// settings. "New template" needs template.create (the server decides).
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Plus from '@lucide/svelte/icons/plus';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import ListCard from '$lib/features/resources/ListCard.svelte';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import { applyListFilters, isFiltering, listSummary } from '$lib/features/resources/filters';
	import { ListFilters } from '$lib/features/resources/list-filters.svelte';
	import { canAnywhere } from '$lib/features/stacks/model';
	import CreateTemplateDialog from '$lib/features/templates/CreateTemplateDialog.svelte';
	import OwnRegistryCard from '$lib/features/templates/OwnRegistryCard.svelte';
	import TemplateCard from '$lib/features/templates/TemplateCard.svelte';
	import { tagCounts, templateFilters, templateSearch } from '$lib/features/templates/model';
	import { templatesQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, EmptyState, ErrorState, Skeleton } from '$lib/ui';

	usePage({ title: 'Templates', crumbs: [{ label: 'Templates' }] });

	const templates = createQuery(() => templatesQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const createDialog = urlDialog('create');
	const canCreate = $derived(canAnywhere(perms.data, 'template.create'));

	const filters = new ListFilters('templates');
	const all = $derived(templates.data ?? []);
	const defs = $derived(templateFilters(all));
	const rows = $derived(
		applyListFilters(all, defs, filters.state, templateSearch).sort((a, b) =>
			a.name.localeCompare(b.name)
		)
	);
	const filtered = $derived(isFiltering(defs, filters.state));
	const tags = $derived(tagCounts(all).slice(0, 16));

	// routes.templates(tag) opens the list filtered by that tag.
	$effect(() => {
		const tag = page.url.searchParams.get('tag');
		if (tag) filters.set('tag', tag);
	});

	function pickTag(tag: string) {
		filters.set('tag', filters.get('tag') === tag ? '' : tag);
	}
</script>

<div class="page">
	<header class="head">
		<div>
			<h1>Templates</h1>
			<p class="muted">
				Complete Compose projects (compose.yaml, .env and the files next to them) to create
				stacks from. Publish a version to use it; make it public to share it with other
				Docker Manager instances.
			</p>
		</div>
		{#if canCreate}
			<div class="actions">
				<Button variant="primary" icon={Plus} onclick={() => (createDialog.open = true)}
					>New template</Button
				>
			</div>
		{/if}
	</header>

	{#if tags.length}
		<section class="tags" aria-labelledby="tags-title">
			<h2 id="tags-title">Browse by tag</h2>
			<div class="chips">
				{#each tags as t (t.tag)}
					<button
						type="button"
						class="chip"
						aria-pressed={filters.get('tag') === t.tag}
						onclick={() => pickTag(t.tag)}
						>#{t.tag} <span class="count">{t.count}</span></button
					>
				{/each}
			</div>
		</section>
	{/if}

	{#if templates.isError}
		<ErrorState
			error={templates.error}
			title="The templates could not be loaded."
			onretry={() => templates.refetch()}
		/>
	{:else}
		<ListCard
			title="This instance's templates"
			id="templates"
			summary={templates.data
				? listSummary(rows.length, all.length, filtered, 'template', 'templates')
				: undefined}
			label="Filter templates"
			searchLabel="Search templates"
			placeholder="Search by name, description or tag"
			filters={defs}
			store={filters}
		>
			{#if templates.isPending}
				<div class="loading" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
			{:else if rows.length}
				<ul class="grid" aria-label="Templates">
					{#each rows as t (t.id)}
						<li>
							<TemplateCard
								href={routes.template(t.id)}
								name={t.name}
								description={t.description}
								tags={t.tags}
								iconUrl={t.icon?.url}
								visibility={t.visibility}
								latest={t.latest?.label}
								onTag={pickTag}
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
						? 'Create a template from scratch, then add its compose.yaml, .env and files.'
						: 'Templates you are given access to appear here.'}
					level={3}
					compact
				>
					{#snippet actions()}
						{#if canCreate}<Button
								variant="primary"
								onclick={() => (createDialog.open = true)}>New template</Button
							>{/if}
					{/snippet}
				</EmptyState>
			{/if}
		</ListCard>
	{/if}

	{#if templates.data}<OwnRegistryCard templates={templates.data} />{/if}

	<CreateTemplateDialog bind:open={createDialog.open} />
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
		max-width: 72ch;
		margin-top: 2px;
		font-size: var(--text-control);
	}

	.actions {
		display: flex;
		gap: var(--space-2);
	}

	.tags h2 {
		margin-bottom: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.chip {
		padding: 4px 10px;
		border: 1px solid var(--border-subtle);
		border-radius: 999px;
		background: var(--surface-panel);
		color: var(--text-default);
		font: inherit;
		font-size: var(--text-control);
		cursor: pointer;
	}

	.chip:hover {
		border-color: var(--border-strong);
	}

	.chip[aria-pressed='true'] {
		border-color: var(--accent-text);
		color: var(--text-strong);
	}

	.count {
		color: var(--text-muted);
		font-variant-numeric: tabular-nums;
	}

	.loading {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
		gap: var(--space-3);
		margin: 0;
		padding: var(--space-4);
		list-style: none;
	}
</style>
