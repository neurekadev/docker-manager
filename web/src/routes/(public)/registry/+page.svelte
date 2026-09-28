<script lang="ts">
	// This instance's public template registry (template registry), readable
	// without signing in: the public templates with their tags and versions,
	// search and tag filters, and the registry URL to add in another Docker
	// Manager (Templates → Template sources) in a compact side
	// card, below the templates on narrow screens. Private templates never
	// appear here.
	import { createQuery } from '@tanstack/svelte-query';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import { ApiRequestError, api, unwrap } from '$lib/api/client';
	import TemplateCard from '$lib/features/templates/TemplateCard.svelte';
	import { tagCounts } from '$lib/features/templates/model';
	import NoMatches from '$lib/features/resources/NoMatches.svelte';
	import {
		Card,
		Chip,
		CopyButton,
		EmptyState,
		ErrorState,
		PageHeader,
		Skeleton,
		TextField,
		formatRelative
	} from '$lib/ui';

	const index = createQuery(() => ({
		queryKey: ['public', 'template-registry'],
		queryFn: ({ signal }) => unwrap(api.GET('/api/v1/template-registry', { signal })),
		retry: false,
		staleTime: 60_000
	}));

	const registryUrl = $derived(
		index.data?.url || (typeof location !== 'undefined' ? location.origin : '')
	);
	let query = $state('');
	let tag = $state('');

	const tags = $derived(tagCounts(index.data?.templates ?? []).slice(0, 20));
	const shown = $derived.by(() => {
		const q = query.trim().toLowerCase();
		return (index.data?.templates ?? [])
			.filter((t) => !tag || t.tags.includes(tag))
			.filter(
				(t) =>
					!q ||
					[t.name, t.description, ...t.tags].some((s) => s?.toLowerCase().includes(q))
			)
			.sort((a, b) => a.name.localeCompare(b.name));
	});

	$effect(() => {
		document.title = index.data ? `${index.data.name} templates` : 'Templates';
	});
</script>

{#if index.isPending}
	<div aria-busy="true"><Skeleton lines={8} /></div>
{:else if index.isError}
	{#if index.error instanceof ApiRequestError && index.error.status === 404}
		<EmptyState
			icon={LayoutTemplate}
			color="violet"
			title="This Docker Manager does not share templates."
			description="Its owner has turned the public template registry off."
			level={1}
		/>
	{:else}
		<ErrorState
			error={index.error}
			title="The templates could not be loaded."
			onretry={() => index.refetch()}
		/>
	{/if}
{:else if index.data}
	{@const idx = index.data}
	<div class="page">
		<PageHeader
			title="{idx.name} templates"
			description="Complete Compose projects shared by this Docker Manager. {idx.templates
				.length} {idx.templates.length === 1 ? 'template' : 'templates'}{idx.templates
				.length
				? `, updated ${formatRelative(idx.updatedAt)}`
				: ''}."
		/>

		<div class="layout">
			<div class="main">
				{#if idx.templates.length === 0}
					<Card>
						<EmptyState
							icon={LayoutTemplate}
							color="violet"
							title="No public templates yet."
							description="Templates appear here when their owner makes them public and publishes a version."
							level={2}
						/>
					</Card>
				{:else}
					<div class="filters">
						<div class="search">
							<TextField
								label="Search templates"
								hideLabel
								type="search"
								placeholder="Search templates"
								bind:value={query}
							/>
						</div>
						{#if tags.length}
							<div class="chips" role="group" aria-label="Filter by tag">
								{#each tags as { tag: t, count: n } (t)}
									<Chip
										label={t}
										count={n}
										size="sm"
										selected={tag === t}
										onclick={() => (tag = tag === t ? '' : t)}
									/>
								{/each}
							</div>
						{/if}
					</div>
					{#if shown.length}
						<ul class="grid" aria-label="Public templates">
							{#each shown as t (t.id)}
								<li class="item">
									<TemplateCard
										name={t.name}
										description={t.description}
										tags={t.tags}
										iconUrl={t.icon?.url}
										latest={t.versions[0]?.label}
										source={idx.name}
										onTag={(x) => (tag = x)}
									/>
									{#if t.versions.length > 1}
										<p class="versions muted">
											Versions: {t.versions.map((v) => v.label).join(', ')}
										</p>
									{/if}
								</li>
							{/each}
						</ul>
					{:else}
						<Card>
							<NoMatches
								what="templates"
								icon={LayoutTemplate}
								onclear={() => {
									query = '';
									tag = '';
								}}
							/>
						</Card>
					{/if}
				{/if}
			</div>

			<aside class="side" aria-labelledby="add-title">
				<Card>
					<div class="add" id="add">
						<h2 id="add-title" class="subsection-title">Use these templates</h2>
						<p class="muted">
							In your Docker Manager, open <strong
								>Templates → Template sources</strong
							>, choose
							<strong>Add template source</strong> and paste this address:
						</p>
						<div class="url">
							<code title={registryUrl}>{registryUrl}</code>
							<CopyButton value={registryUrl} what="registry address" />
						</div>
					</div>
				</Card>
			</aside>
		</div>
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	/* Templates first; the instructions in a narrow side card (below the
	   templates on narrow screens). */
	.layout {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 300px;
		align-items: start;
		gap: var(--space-4);
	}

	.main {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}

	@media (max-width: 1023px) {
		.layout {
			grid-template-columns: minmax(0, 1fr);
		}
	}

	.add {
		display: grid;
		gap: var(--space-3);
	}

	.url {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		min-width: 0;
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
	}

	.url code {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.filters {
		display: grid;
		gap: var(--space-3);
	}

	.search {
		max-width: 480px;
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
		gap: var(--space-3);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.item {
		display: grid;
		gap: var(--space-2);
	}

	.versions {
		font-size: var(--text-caption);
	}
</style>
