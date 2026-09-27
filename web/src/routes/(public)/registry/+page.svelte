<script lang="ts">
	// This instance's public template registry (template registry), readable
	// without signing in: the public templates with their tags and versions,
	// search and tag filters, and the registry URL to add in another Docker
	// Manager (Templates → Registries → Add registry). Private templates never
	// appear here.
	import { createQuery } from '@tanstack/svelte-query';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import { ApiRequestError, api, unwrap } from '$lib/api/client';
	import TemplateCard from '$lib/features/templates/TemplateCard.svelte';
	import { tagCounts } from '$lib/features/templates/model';
	import {
		Card,
		CopyButton,
		EmptyState,
		ErrorState,
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
		<header class="head">
			<h1>{idx.name} templates</h1>
			<p class="muted">
				Complete Compose projects shared by this Docker Manager. {idx.templates.length}
				{idx.templates.length === 1 ? 'template' : 'templates'}{idx.templates.length
					? `, updated ${formatRelative(idx.updatedAt)}`
					: ''}.
			</p>
		</header>

		<Card title="Use these templates in your Docker Manager" id="add">
			<div class="add">
				<p>
					In your Docker Manager, open <strong>Templates → Registries</strong>, choose
					<strong>Add registry</strong> and paste this URL:
				</p>
				<div class="url">
					<code>{registryUrl}</code>
					<CopyButton value={registryUrl} what="registry URL" />
				</div>
			</div>
		</Card>

		{#if idx.templates.length === 0}
			<EmptyState
				icon={LayoutTemplate}
				color="violet"
				title="No public templates yet."
				description="Templates appear here when their owner makes them public and publishes a version."
				level={2}
			/>
		{:else}
			<div class="filters">
				<TextField
					label="Search templates"
					hideLabel
					type="search"
					placeholder="Search by name, description or tag"
					bind:value={query}
				/>
				{#if tags.length}
					<div class="chips" role="group" aria-label="Filter by tag">
						{#each tags as { tag: t, count: n } (t)}
							<button
								type="button"
								class="chip"
								aria-pressed={tag === t}
								onclick={() => (tag = tag === t ? '' : t)}
								>#{t} <span class="count">{n}</span></button
							>
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
				<p class="muted">No template matches. Clear the search or the tag.</p>
			{/if}
		{/if}
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.head p {
		margin-top: 2px;
	}

	.add {
		display: grid;
		gap: var(--space-3);
	}

	.url {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
	}

	.url code {
		flex: 1;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.filters {
		display: grid;
		gap: var(--space-3);
		max-width: 640px;
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

	.chip[aria-pressed='true'] {
		border-color: var(--accent-text);
		color: var(--text-strong);
	}

	.count {
		color: var(--text-muted);
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
