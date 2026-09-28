<script lang="ts">
	// Template overview (template registry): the newest version (what a stack
	// created now gets) beside the template's details, then what that version
	// runs: its services with images and ports and the names of its .env
	// settings (read from the version's files with template.use; values are
	// never shown). Without a version it says how to publish one; with
	// limited access (view minimal) it says so instead of an empty page.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import Columns from '$lib/features/common/Columns.svelte';
	import Digest from '$lib/features/common/Digest.svelte';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import Facts from '$lib/features/common/Facts.svelte';
	import DefinitionSummary from '$lib/features/templates/DefinitionSummary.svelte';
	import { composeFiles, contentsSummary, versionTitle } from '$lib/features/templates/model';
	import { templateDefinitionQuery, templateQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Card,
		Chip,
		ErrorState,
		Notice,
		Skeleton,
		formatBytes,
		formatDateTime,
		formatRelative
	} from '$lib/ui';

	const id = $derived(page.params.templateId ?? '');
	const template = createQuery(() => templateQuery(id));
	const t = $derived(template.data);
	const can = (a: string) => !!t?.actions.includes(a);
	const latest = $derived(t?.view === 'full' ? t.latest : undefined);
	const canUse = $derived(can('template.use'));
	// The version's Compose files and .env (template.use; the .env values
	// stay in memory and are never rendered).
	const definition = createQuery(() =>
		templateDefinitionQuery(canUse && latest ? id : '', latest?.number ?? 0)
	);
</script>

{#snippet tagList()}
	{#if t?.tags?.length}
		<span class="tags">
			{#each t.tags as tag (tag)}<Chip
					size="sm"
					label={tag}
					href={routes.templates(tag)}
					title="Show templates tagged {tag}"
				/>{/each}
		</span>
	{:else}<span class="muted">None</span>{/if}
{/snippet}

{#if t && t.view !== 'full'}
	<Notice tone="info" live="none" title="You have limited access to this template">
		Its versions, files and details are shown to people with access to it. Ask the owner of this
		Docker Manager if you need it.
	</Notice>
{:else if t}
	<Columns ratio="equal">
		{#if latest}
			{@const v = latest}
			<Card title="Latest version" id="latest">
				<div class="stack">
					{#snippet digest()}<Digest value={v.archiveSha256} />{/snippet}
					<Facts
						items={[
							{ label: 'Version', value: v.label },
							{
								label: 'Published',
								value: formatRelative(v.publishedAt),
								title: formatDateTime(v.publishedAt)
							},
							{
								label: 'Contents',
								value: `${contentsSummary(v.entries, v.definition)}, ${formatBytes(v.contentSize)}`
							},
							{
								label: 'Compose files',
								value: composeFiles(v.definition)
									.map((f) => f.path)
									.join(', ')
							}
						]}
					/>
					{#if v.notes}
						<div>
							<h3 class="subsection-title">What changed</h3>
							<p class="notes">{v.notes}</p>
						</div>
					{/if}
					<Disclosure summary="Details">
						<Facts
							items={[
								{ label: 'Archive digest', render: digest },
								{ label: 'Archive size', value: formatBytes(v.archiveSize) }
							]}
						/>
					</Disclosure>
				</div>
			</Card>
		{:else}
			<Notice tone="info" title="Not published yet" live="none">
				Stacks are created from published versions. Edit the draft's files, then publish its
				first version.
				{#snippet actions()}
					{#if can('template.files.read')}
						<Button size="sm" icon={FolderOpen} href={routes.template(t.id, 'files')}
							>Open files</Button
						>
					{/if}
				{/snippet}
			</Notice>
		{/if}

		<Card title="About" id="about">
			<Facts
				columns={1}
				items={[
					{
						label: 'Visibility',
						value:
							t.visibility === 'public'
								? 'Public: other Docker Managers can use its published versions'
								: 'Private: only this Docker Manager'
					},
					{ label: 'Tags', render: tagList },
					{
						label: 'Created',
						value: t.createdAt ? formatDateTime(t.createdAt) : null
					},
					{
						label: 'Last changed',
						value: t.updatedAt ? formatRelative(t.updatedAt) : null,
						title: t.updatedAt ? formatDateTime(t.updatedAt) : undefined
					}
				]}
			/>
		</Card>
	</Columns>

	{#if latest}
		<Card
			title="What it runs"
			subtitle="{versionTitle(
				latest.label
			)}: its services and the settings its .env asks for."
			padding="none"
			id="services"
		>
			{#if !canUse}
				<p class="muted pad">
					People who may create stacks from this template see its services here. Its
					Compose files: {composeFiles(latest.definition)
						.map((f) => f.path)
						.join(', ')}.
				</p>
			{:else if definition.isError}
				<div class="pad">
					<ErrorState
						error={definition.error}
						title="The version's files could not be read."
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
{/if}

<style>
	.stack {
		display: grid;
		gap: var(--space-4);
	}

	.notes {
		margin-top: var(--space-1);
		color: var(--text-default);
		white-space: pre-wrap;
	}

	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}

	.pad {
		padding: var(--space-4) var(--space-5);
	}
</style>
