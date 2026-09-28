<script lang="ts">
	// A template of a template source (another Docker Manager), read-only:
	// the page header with its links and "Create stack" (template.use),
	// what its newest
	// version runs (services, images, ports and .env names, downloaded from
	// its source with template.use; values never shown), and its published
	// versions with their notes and "Duplicate as a new template". Its files
	// stay on its source until a stack is created from it.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Archive from '@lucide/svelte/icons/archive';
	import Copy from '@lucide/svelte/icons/copy';
	import History from '@lucide/svelte/icons/history';
	import LayoutTemplate from '@lucide/svelte/icons/layout-template';
	import Plus from '@lucide/svelte/icons/plus';
	import Tag from '@lucide/svelte/icons/tag';
	import { ApiRequestError } from '$lib/api/client';
	import { myPermissionsQuery } from '$lib/api/queries';
	import LinkList from '$lib/features/common/LinkList.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import { canAnywhere } from '$lib/features/stacks/model';
	import DefinitionSummary from '$lib/features/templates/DefinitionSummary.svelte';
	import DuplicateDialog from '$lib/features/templates/DuplicateDialog.svelte';
	import TemplateIcon from '$lib/features/templates/TemplateIcon.svelte';
	import { versionTitle } from '$lib/features/templates/model';
	import { catalogDefinitionQuery, catalogItemQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		Chip,
		EmptyState,
		ErrorState,
		PageHeader,
		Skeleton,
		formatBytes,
		formatDateTime,
		formatRelative,
		type MetaItem
	} from '$lib/ui';

	const instanceId = $derived(page.params.instanceId ?? '');
	const templateId = $derived(page.params.templateId ?? '');
	const item = createQuery(() => catalogItemQuery(instanceId, templateId));
	const t = $derived(item.data);
	const perms = createQuery(() => myPermissionsQuery());
	const canUse = $derived(!!t?.actions.includes('template.use') && !!t?.versions.length);
	const canDuplicate = $derived(canUse && canAnywhere(perms.data, 'template.create'));
	const latest = $derived(t?.versions[0]);
	const definition = createQuery(() =>
		catalogDefinitionQuery(
			canUse ? instanceId : '',
			canUse ? templateId : '',
			latest?.number ?? 0
		)
	);
	let duplicating = $state(false);

	usePage(() => ({
		title: t?.name ?? 'Template',
		crumbs: [{ label: 'Templates', href: routes.templates() }, { label: t?.name ?? 'Template' }]
	}));

	const meta = $derived.by((): MetaItem[] => {
		if (!t) return [];
		const out: MetaItem[] = [
			{ icon: Archive, label: `From ${t.registryName}`, title: 'Its template source' }
		];
		if (latest) {
			out.push({ icon: Tag, label: versionTitle(latest.label), title: 'The newest version' });
			out.push({
				icon: History,
				label: `${t.versions.length} ${t.versions.length === 1 ? 'version' : 'versions'}`
			});
		}
		return out;
	});
</script>

{#snippet duplicateAction()}
	<Button size="sm" icon={Copy} onclick={() => (duplicating = true)}
		>Duplicate as a new template</Button
	>
{/snippet}

{#if item.isPending}
	<Page>
		<div aria-busy="true" aria-label="Loading the template"><Skeleton lines={6} /></div>
	</Page>
{:else if item.error instanceof ApiRequestError && (item.error.status === 404 || item.error.status === 403)}
	<EmptyState
		icon={LayoutTemplate}
		color="violet"
		title="This template is not available."
		description="Its source was removed, it is no longer public, or your access changed."
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
	{#snippet links()}<LinkList links={t.links} label="Links of {t.name}" />{/snippet}
	<Page>
		<PageHeader
			title={t.name}
			description={t.description || undefined}
			{meta}
			below={t.links?.length ? links : undefined}
		>
			{#snippet media()}<TemplateIcon url={t.iconUrl} size="lg" />{/snippet}
			{#snippet actions()}
				{#if canUse}
					<Button
						variant="primary"
						icon={Plus}
						href={routes.stackFromTemplate(t.templateId, null, t.instanceId)}
						>Create stack</Button
					>
				{/if}
			{/snippet}
		</PageHeader>

		{#if t.tags.length}
			<ul class="tags" aria-label="Tags">
				{#each t.tags as tag (tag)}
					<li>
						<Chip
							size="sm"
							label={tag}
							href={routes.templates(tag)}
							title="Show templates tagged {tag}"
						/>
					</li>
				{/each}
			</ul>
		{/if}

		{#if latest && canUse}
			<Card
				title="What it runs"
				subtitle="{versionTitle(
					latest.label
				)}: its services and the settings its .env asks for."
				padding="none"
				id="services"
			>
				{#if definition.isError}
					<div class="pad">
						<ErrorState
							error={definition.error}
							title="The version's files could not be downloaded from {t.registryName}."
							onretry={() => definition.refetch()}
							bare
							compact
						/>
					</div>
				{:else if definition.isPending}
					<div class="pad" aria-busy="true"><Skeleton lines={3} height="20px" /></div>
				{:else if definition.data}
					<DefinitionSummary files={definition.data.files} />
				{/if}
			</Card>
		{/if}

		<Card title="Versions" id="versions" actions={canDuplicate ? duplicateAction : undefined}>
			{#if t.versions.length}
				<ol class="versions">
					{#each t.versions as v, i (v.number)}
						<li>
							<div class="version-head">
								<strong>{versionTitle(v.label)}</strong>
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
			{:else}
				<p class="muted">No published versions.</p>
			{/if}
		</Card>
	</Page>
	{#if canDuplicate}
		<DuplicateDialog
			bind:open={duplicating}
			source={{
				instanceId: t.instanceId,
				templateId: t.templateId,
				name: t.name,
				versions: t.versions
			}}
		/>
	{/if}
{/if}

<style>
	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
		margin: calc(-1 * var(--space-2)) 0 0;
		padding: 0;
		list-style: none;
	}

	.pad {
		padding: var(--space-4) var(--space-5);
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
