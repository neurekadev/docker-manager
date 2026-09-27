<script lang="ts">
	// Template detail (template registry): the header with the template's
	// icon, visibility, tags and newest version, "Publish version", then the
	// tabs Overview · Files · Versions · Settings. Tabs and actions follow the
	// template's DTO actions (hidden, never disabled).
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Upload from '@lucide/svelte/icons/upload';
	import { ApiRequestError } from '$lib/api/client';
	import PublishDialog from '$lib/features/templates/PublishDialog.svelte';
	import TemplateIcon from '$lib/features/templates/TemplateIcon.svelte';
	import { templateQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import { Badge, Button, EmptyState, ErrorState, Skeleton, TabNav, type TabLink } from '$lib/ui';

	let { children } = $props();

	const id = $derived(page.params.templateId ?? '');
	const template = createQuery(() => templateQuery(id));
	const t = $derived(template.data);
	const can = (a: string) => !!t?.actions.includes(a);
	let publishing = $state(false);

	usePage(() => ({
		title: t?.name ?? 'Template',
		crumbs: [{ label: 'Templates', href: routes.templates() }, { label: t?.name ?? 'Template' }]
	}));

	const notFound = $derived(
		template.error instanceof ApiRequestError &&
			(template.error.status === 404 || template.error.status === 403)
	);
	const tabs = $derived.by((): TabLink[] => {
		if (!t) return [];
		const out: TabLink[] = [{ href: routes.template(id), label: 'Overview' }];
		if (can('template.files.read'))
			out.push({ href: routes.template(id, 'files'), label: 'Files' });
		if (t.view === 'full')
			out.push({ href: routes.template(id, 'versions'), label: 'Versions' });
		if (can('template.manage') || can('template.publish') || can('template.remove'))
			out.push({ href: routes.template(id, 'settings'), label: 'Settings' });
		return out;
	});
</script>

{#if template.isPending}
	<div class="page" aria-busy="true" aria-label="Loading the template">
		<div class="head-skeleton">
			<Skeleton width="48px" height="48px" radius="md" />
			<div class="grow"><Skeleton lines={3} /></div>
		</div>
		<Skeleton height="280px" radius="lg" />
	</div>
{:else if notFound}
	<EmptyState
		icon={LayoutTemplate}
		color="violet"
		title="This template does not exist or you can't see it."
		description="It may have been deleted, or your access changed. Templates you can see are listed under Templates."
		level={1}
	>
		{#snippet actions()}<Button variant="primary" href={routes.templates()}
				>Open templates</Button
			>{/snippet}
	</EmptyState>
{:else if template.isError}
	<ErrorState
		error={template.error}
		title="The template could not be loaded."
		onretry={() => template.refetch()}
	/>
{:else if t}
	<div class="page">
		<header class="head">
			<TemplateIcon url={t.icon?.url} size="lg" />
			<div class="main">
				<div class="title-row">
					<h1>{t.name}</h1>
					{#if t.visibility === 'public'}<Badge tone="info">Public</Badge>{:else}<Badge
							tone="neutral">Private</Badge
						>{/if}
				</div>
				{#if t.description}<p class="desc">{t.description}</p>{/if}
				<div class="meta">
					<span
						>{t.latest ? `Latest version ${t.latest.label}` : 'Not published yet'}</span
					>
					{#if t.view === 'full'}<span
							>{t.versions} {t.versions === 1 ? 'version' : 'versions'}</span
						>{/if}
					{#each t.tags ?? [] as tag (tag)}
						<a class="tag" href={routes.templates(tag)}>#{tag}</a>
					{/each}
				</div>
			</div>
			{#if can('template.publish')}
				<div class="actions">
					<Button variant="primary" icon={Upload} onclick={() => (publishing = true)}
						>Publish version</Button
					>
				</div>
			{/if}
		</header>
		<TabNav label="Template sections" current={page.url.pathname} items={tabs} />
		{@render children()}
	</div>
	{#if can('template.publish')}
		<PublishDialog bind:open={publishing} template={t} />
	{/if}
{/if}

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
		min-width: 0;
	}

	.head-skeleton {
		display: flex;
		gap: var(--space-4);
	}

	.grow {
		flex: 1;
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
		font-size: var(--text-control);
	}

	.meta {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: var(--space-2) var(--space-3);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.tag {
		color: var(--text-muted);
		text-decoration: none;
	}

	.tag:hover {
		color: var(--accent-text);
	}

	.actions {
		display: flex;
		gap: var(--space-2);
	}
</style>
