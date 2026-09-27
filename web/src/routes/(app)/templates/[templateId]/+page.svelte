<script lang="ts">
	// Template overview (template registry): the newest version (what a stack
	// created now gets) and what to do next: edit the draft, publish.
	import { createQuery } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import Facts from '$lib/features/common/Facts.svelte';
	import { templateQuery } from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { Button, Card, Notice, formatBytes, formatDateTime, formatRelative } from '$lib/ui';

	const id = $derived(page.params.templateId ?? '');
	const template = createQuery(() => templateQuery(id));
	const t = $derived(template.data);
	const can = (a: string) => !!t?.actions.includes(a);
</script>

{#if t}
	<div class="grid">
		{#if t.view === 'full' && t.latest}
			{@const v = t.latest}
			<Card title="Latest version: {v.label}" id="latest">
				<Facts
					items={[
						{
							label: 'Published',
							value: formatRelative(v.publishedAt),
							title: formatDateTime(v.publishedAt)
						},
						{ label: 'Files', value: `${v.entries} (${formatBytes(v.contentSize)})` },
						{
							label: 'Compose files',
							value: v.definition.map((f) => f.path).join(', ')
						},
						{
							label: 'Archive digest',
							value: v.archiveSha256.slice(0, 16),
							mono: true,
							title: v.archiveSha256
						}
					]}
				/>
				{#if v.notes}<p class="notes">{v.notes}</p>{/if}
			</Card>
		{:else if t.view === 'full'}
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

		{#if t.view === 'full'}
			<Card title="About" id="about">
				<Facts
					columns={1}
					items={[
						{
							label: 'Visibility',
							value:
								t.visibility === 'public'
									? "Public: listed in this instance's registry for other Docker Manager instances"
									: 'Private: only this instance'
						},
						{
							label: 'Created',
							value: t.createdAt ? formatDateTime(t.createdAt) : null
						},
						{
							label: 'Last changed',
							value: t.updatedAt ? formatRelative(t.updatedAt) : null
						}
					]}
				/>
			</Card>
		{/if}
	</div>
{/if}

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
		gap: var(--space-4);
		align-items: start;
	}

	.notes {
		margin-top: var(--space-3);
		color: var(--text-default);
		white-space: pre-wrap;
	}
</style>
