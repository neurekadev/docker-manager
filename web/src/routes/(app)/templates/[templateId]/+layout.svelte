<script lang="ts">
	// Template detail (template registry): the page header (the template's
	// icon, visibility, newest version, number of versions, last change),
	// "Create stack" as the primary action once a version is published and
	// "Publish version", then the tabs Overview · Files · Versions ·
	// Settings. Tabs and actions follow the template's DTO actions (hidden,
	// never disabled).
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import History from '@lucide/svelte/icons/history';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Plus from '@lucide/svelte/icons/plus';
	import Tag from '@lucide/svelte/icons/tag';
	import Upload from '@lucide/svelte/icons/upload';
	import { ApiRequestError } from '$lib/api/client';
	import Page from '$lib/features/common/Page.svelte';
	import PublishDialog from '$lib/features/templates/PublishDialog.svelte';
	import TemplateIcon from '$lib/features/templates/TemplateIcon.svelte';
	import { versionTitle } from '$lib/features/templates/model';
	import { templateQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		EmptyState,
		ErrorState,
		PageHeader,
		Skeleton,
		TabNav,
		formatDateTime,
		formatRelative,
		type MetaItem,
		type TabLink
	} from '$lib/ui';

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
	const canCreateStack = $derived(can('template.use') && !!t?.latest);
	const meta = $derived.by((): MetaItem[] => {
		if (!t || t.view !== 'full') return [];
		const out: MetaItem[] = t.latest
			? [
					{ icon: Tag, label: versionTitle(t.latest.label), title: 'The newest version' },
					{
						icon: History,
						label: `${t.versions} ${t.versions === 1 ? 'version' : 'versions'}`
					}
				]
			: [{ icon: History, label: 'Draft only, no version yet' }];
		if (t.updatedAt)
			out.push({
				icon: Clock,
				label: `Changed ${formatRelative(t.updatedAt)}`,
				title: formatDateTime(t.updatedAt)
			});
		return out;
	});
</script>

{#if template.isPending}
	<Page>
		<div class="head-skeleton" aria-busy="true" aria-label="Loading the template">
			<Skeleton width="48px" height="48px" radius="md" />
			<div class="grow"><Skeleton lines={3} /></div>
		</div>
		<Skeleton height="280px" radius="lg" />
	</Page>
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
	<Page>
		<PageHeader title={t.name} description={t.description || undefined} {meta}>
			{#snippet media()}<TemplateIcon url={t.icon?.url} size="lg" />{/snippet}
			{#snippet status()}
				{#if t.visibility === 'public'}<Badge tone="info">Public</Badge>{:else}<Badge
						tone="neutral">Private</Badge
					>{/if}
			{/snippet}
			{#snippet actions()}
				{#if canCreateStack}
					<Button variant="primary" icon={Plus} href={routes.stackFromTemplate(t.id)}
						>Create stack</Button
					>
				{/if}
				{#if can('template.publish')}
					<Button
						variant={canCreateStack ? 'secondary' : 'primary'}
						icon={Upload}
						onclick={() => (publishing = true)}>Publish version</Button
					>
				{/if}
			{/snippet}
		</PageHeader>
		<TabNav label="Template sections" current={page.url.pathname} items={tabs} />
		{@render children()}
	</Page>
	{#if can('template.publish')}
		<PublishDialog bind:open={publishing} template={t} />
	{/if}
{/if}

<style>
	.head-skeleton {
		display: flex;
		gap: var(--space-4);
	}

	.grow {
		flex: 1;
	}
</style>
