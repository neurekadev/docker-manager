<script lang="ts">
	// A template of an added registry (template registry), read-only: its
	// description, tags and published versions with their notes, and
	// "Create stack" with template.use. Its files stay on its registry until a
	// stack is created from it.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Plus from '@lucide/svelte/icons/plus';
	import { ApiRequestError } from '$lib/api/client';
	import TemplateIcon from '$lib/features/templates/TemplateIcon.svelte';
	import { catalogItemQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		ErrorState,
		Skeleton,
		formatBytes,
		formatDateTime,
		formatRelative
	} from '$lib/ui';

	const instanceId = $derived(page.params.instanceId ?? '');
	const templateId = $derived(page.params.templateId ?? '');
	const item = createQuery(() => catalogItemQuery(instanceId, templateId));
	const t = $derived(item.data);

	usePage(() => ({
		title: t?.name ?? 'Template',
		crumbs: [{ label: 'Templates', href: routes.templates() }, { label: t?.name ?? 'Template' }]
	}));
</script>

{#if item.isPending}
	<div aria-busy="true"><Skeleton lines={6} /></div>
{:else if item.error instanceof ApiRequestError && (item.error.status === 404 || item.error.status === 403)}
	<EmptyState
		icon={LayoutTemplate}
		color="violet"
		title="This template is not available."
		description="Its registry was removed, it is no longer public, or your access changed."
		level={1}
	>
		{#snippet actions()}<Button variant="primary" href={routes.templates()}
				>Open templates</Button
			>{/snippet}
	</EmptyState>
{:else if item.isError}
	<ErrorState
		error={item.error}
		title="The template could not be loaded."
		onretry={() => item.refetch()}
	/>
{:else if t}
	<div class="page">
		<header class="head">
			<TemplateIcon url={t.iconUrl} size="lg" />
			<div class="main">
				<div class="title-row">
					<h1>{t.name}</h1>
					<Badge tone="neutral">From {t.registryName}</Badge>
				</div>
				{#if t.description}<p class="desc">{t.description}</p>{/if}
				<div class="meta">
					{#each t.tags as tag (tag)}<a class="tag" href={routes.templates(tag)}>#{tag}</a
						>{/each}
				</div>
			</div>
			{#if t.actions.includes('template.use') && t.versions.length}
				<Button
					variant="primary"
					icon={Plus}
					href={routes.stackFromTemplate(t.templateId, null, t.instanceId)}
					>Create stack</Button
				>
			{/if}
		</header>

		<Card title="Versions" id="versions">
			<ol class="versions">
				{#each t.versions as v, i (v.number)}
					<li>
						<div class="version-head">
							<strong class="mono">{v.label}</strong>
							{#if i === 0}<Badge tone="info">Latest</Badge>{/if}
							<span class="muted" title={formatDateTime(v.publishedAt)}
								>{formatRelative(v.publishedAt)} · {formatBytes(
									v.contentSize
								)}</span
							>
						</div>
						{#if v.notes}<p class="notes">{v.notes}</p>{/if}
					</li>
				{/each}
			</ol>
		</Card>
	</div>
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.head {
		display: flex;
		align-items: flex-start;
		flex-wrap: wrap;
		gap: var(--space-4);
	}

	.main {
		display: flex;
		flex: 1;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}

	.title-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.desc {
		max-width: 80ch;
		color: var(--text-default);
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		font-size: var(--text-caption);
	}

	.tag {
		color: var(--text-muted);
		text-decoration: none;
	}

	.tag:hover {
		color: var(--accent-text);
	}

	.versions {
		display: grid;
		gap: var(--space-3);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.version-head {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2);
	}

	.notes {
		margin-top: 4px;
		color: var(--text-default);
		white-space: pre-wrap;
	}
</style>
