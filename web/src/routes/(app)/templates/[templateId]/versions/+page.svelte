<script lang="ts">
	// Template detail, Versions tab (template registry): every published
	// version, newest first, with its notes and size. Deleting a version
	// (template.publish) keeps stacks created from it working; its number is
	// never reused.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import History from '@lucide/svelte/icons/history';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { deleteVersion } from '$lib/features/templates/actions';
	import {
		templateKeys,
		templateQuery,
		templateVersionsQuery,
		type TemplateVersion
	} from '$lib/features/templates/queries';
	import { usePage } from '$lib/shell/page.svelte';
	import { routes } from '$lib/routes';
	import {
		Card,
		ConfirmDialog,
		EmptyState,
		ErrorState,
		IconButton,
		Skeleton,
		Table,
		formatBytes,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';

	const id = $derived(page.params.templateId ?? '');
	const template = createQuery(() => templateQuery(id));
	const versions = createQuery(() => templateVersionsQuery(id));
	const queryClient = useQueryClient();
	const t = $derived(template.data);
	const canPublish = $derived(!!t?.actions.includes('template.publish'));
	let removing = $state<TemplateVersion | null>(null);
	let confirming = $state(false);

	usePage(() => ({
		title: `${t?.name ?? 'Template'} versions`,
		crumbs: [
			{ label: 'Templates', href: routes.templates() },
			{ label: t?.name ?? 'Template', href: routes.template(id) },
			{ label: 'Versions' }
		]
	}));

	const columns = $derived.by((): Column<TemplateVersion>[] => {
		const cols: Column<TemplateVersion>[] = [
			{
				id: 'label',
				header: 'Version',
				cell: labelCell,
				sortValue: (v) => v.number,
				width: '140px'
			},
			{ id: 'notes', header: 'What changed', cell: notesCell },
			{
				id: 'size',
				header: 'Files',
				cell: sizeCell,
				sortValue: (v) => v.contentSize,
				numeric: true,
				width: '140px'
			},
			{
				id: 'published',
				header: 'Published',
				cell: publishedCell,
				sortValue: (v) => v.publishedAt,
				width: '160px'
			}
		];
		if (canPublish) cols.push({ id: 'actions', header: '', cell: actionsCell, width: '56px' });
		return cols;
	});

	async function remove() {
		if (!t || !removing) return;
		await deleteVersion(t, removing.number);
		void queryClient.invalidateQueries({ queryKey: templateKeys.all });
		toast.success(`Deleted version ${removing.label} of ${t.name}`);
	}
</script>

{#snippet labelCell(v: TemplateVersion)}<span class="mono">{v.label}</span>{/snippet}
{#snippet notesCell(v: TemplateVersion)}
	{#if v.notes}<span class="notes">{v.notes}</span>{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet sizeCell(v: TemplateVersion)}{v.entries} · {formatBytes(v.contentSize)}{/snippet}
{#snippet publishedCell(v: TemplateVersion)}
	<span title={formatDateTime(v.publishedAt)}>{formatRelative(v.publishedAt)}</span>
{/snippet}
{#snippet actionsCell(v: TemplateVersion)}
	<IconButton
		icon={Trash2}
		label="Delete version {v.label}"
		onclick={() => {
			removing = v;
			confirming = true;
		}}
	/>
{/snippet}

{#if versions.isError}
	<ErrorState
		error={versions.error}
		title="The versions could not be loaded."
		onretry={() => versions.refetch()}
	/>
{:else}
	<Card padding="none" title="Published versions" id="versions">
		{#if versions.isPending}
			<div class="loading" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
		{:else}
			<Table
				label="Versions"
				rows={versions.data ?? []}
				{columns}
				rowKey={(v) => String(v.number)}
				sort={{ column: 'label', direction: 'desc' }}
			>
				{#snippet empty()}
					<EmptyState
						icon={History}
						color="violet"
						title="No versions yet."
						description="Publish the draft to create the first version."
						level={3}
						compact
					/>
				{/snippet}
			</Table>
		{/if}
	</Card>
{/if}

{#if removing && t}
	<ConfirmDialog
		bind:open={confirming}
		title="Delete version {removing.label}?"
		message="People can no longer create stacks from it."
		consequences={[
			'Stacks created from it keep working with their own files.',
			'Other instances stop offering it after their next registry sync.'
		]}
		confirmLabel="Delete version"
		tone="danger"
		onconfirm={remove}
	/>
{/if}

<style>
	.loading {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.notes {
		display: -webkit-box;
		overflow: hidden;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 2;
		line-clamp: 2;
	}
</style>
