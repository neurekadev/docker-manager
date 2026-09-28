<script lang="ts">
	// Templates (template registry): the discovery dashboard. The registries
	// this instance browses (its own first), templates by tag, and every
	// template of every registry as cards with search and filters (registry,
	// tag, publication). This instance's templates open their management
	// pages; registry templates open a read-only page to create stacks from.
	// "New template" needs template.create (the server decides).
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Archive from '@lucide/svelte/icons/archive';
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
	import TemplateCard from '$lib/features/templates/TemplateCard.svelte';
	import {
		catalogFilters,
		catalogHref,
		catalogSearch,
		tagCounts
	} from '$lib/features/templates/model';
	import { templateCatalogQuery, templateRegistriesQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Badge, Button, EmptyState, ErrorState, Skeleton } from '$lib/ui';

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
	const tags = $derived(tagCounts(all).slice(0, 16));

	// routes.templates(tag) opens the list filtered by that tag.
	$effect(() => {
		const tag = page.url.searchParams.get('tag');
		if (tag) filters.set('tag', tag);
	});

	function toggle(id: string, value: string) {
		filters.set(id, filters.get(id) === value ? '' : value);
	}
</script>

<div class="page">
	<header class="head">
		<div>
			<h1>Templates</h1>
			<p class="muted">
				Complete Compose projects (compose.yaml, .env and the files next to them) to create
				stacks from: this instance's templates and those of the registries it browses.
			</p>
		</div>
		<div class="actions">
			<Button icon={Archive} href={routes.templateRegistries()}>Registries</Button>
			{#if canCreate}
				<Button variant="primary" icon={Plus} onclick={() => (createDialog.open = true)}
					>New template</Button
				>
			{/if}
		</div>
	</header>

	{#if registries.data?.length}
		<section aria-labelledby="registries-title">
			<h2 id="registries-title" class="section">Registries</h2>
			<ul class="registries">
				{#each registries.data as r (r.instanceId)}
					<li>
						<button
							type="button"
							class="registry"
							aria-pressed={filters.get('registry') === r.instanceId}
							onclick={() => toggle('registry', r.instanceId)}
						>
							<span class="registry-name"
								>{r.own ? `${r.name} (this instance)` : r.name}</span
							>
							<span class="registry-meta">
								{r.own
									? 'Your templates'
									: `${r.templates} ${r.templates === 1 ? 'template' : 'templates'}`}
								{#if r.status === 'error'}<Badge tone="warn" dot>Sync failed</Badge
									>{/if}
							</span>
						</button>
					</li>
				{/each}
			</ul>
		</section>
	{/if}

	{#if tags.length}
		<section aria-labelledby="tags-title">
			<h2 id="tags-title" class="section">Browse by tag</h2>
			<div class="chips">
				{#each tags as t (t.tag)}
					<button
						type="button"
						class="chip"
						aria-pressed={filters.get('tag') === t.tag}
						onclick={() => toggle('tag', t.tag)}
						>#{t.tag} <span class="count">{t.count}</span></button
					>
				{/each}
			</div>
		</section>
	{/if}

	{#if catalog.isError}
		<ErrorState
			error={catalog.error}
			title="The templates could not be loaded."
			onretry={() => catalog.refetch()}
		/>
	{:else}
		<ListCard
			title="All templates"
			id="templates"
			summary={catalog.data
				? listSummary(rows.length, all.length, filtered, 'template', 'templates')
				: undefined}
			label="Filter templates"
			searchLabel="Search templates"
			placeholder="Search by name, description, tag or registry"
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
								source={t.own ? 'This instance' : t.registryName}
								onTag={(tag) => toggle('tag', tag)}
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
						? 'Create a template, or add another Docker Manager as a registry to use its public templates.'
						: 'Templates you are given access to appear here.'}
					level={3}
					compact
				>
					{#snippet actions()}
						{#if canCreate}<Button
								variant="primary"
								onclick={() => (createDialog.open = true)}>New template</Button
							>{/if}
						<Button href={routes.templateRegistries()}>Open registries</Button>
					{/snippet}
				</EmptyState>
			{/if}
		</ListCard>
	{/if}

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
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.section {
		margin-bottom: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
		font-weight: var(--weight-medium);
	}

	.registries {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.registry {
		display: flex;
		flex-direction: column;
		gap: 2px;
		width: 100%;
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-panel);
		color: inherit;
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	.registry:hover {
		border-color: var(--border-strong);
	}

	.registry[aria-pressed='true'] {
		border-color: var(--accent-text);
	}

	.registry-name {
		overflow: hidden;
		color: var(--text-strong);
		font-weight: var(--weight-medium);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.registry-meta {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
		color: var(--text-muted);
		font-size: var(--text-caption);
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
