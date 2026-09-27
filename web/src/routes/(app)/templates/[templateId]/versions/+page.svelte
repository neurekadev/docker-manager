<script lang="ts">
	// Template detail, Versions tab (template registry): every published
	// version, newest first, with its notes and size. Deleting a version
	// (template.publish) keeps stacks created from it working; its number is
	// never reused.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { page } from '$app/state';
	import Copy from '@lucide/svelte/icons/copy';
	import History from '@lucide/svelte/icons/history';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { canAnywhere } from '$lib/features/stacks/model';
	import { deleteVersion, restoreDraft } from '$lib/features/templates/actions';
	import DuplicateDialog from '$lib/features/templates/DuplicateDialog.svelte';
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
	const perms = createQuery(() => myPermissionsQuery());
	const canRestore = $derived(
		!!t?.actions.includes('template.files.write') &&
			!!t?.actions.includes('template.files.delete')
	);
	const canDuplicate = $derived(
		!!t?.actions.includes('template.use') && canAnywhere(perms.data, 'template.create')
	);
	let duplicating = $state(false);
	let restoring = $state<TemplateVersion | null>(null);
	let confirmRestore = $state(false);
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
		if (canPublish || canRestore)
			cols.push({ id: 'actions', header: '', cell: actionsCell, width: '96px' });
		return cols;
	});

	async function restore() {
		if (!t || !restoring) return;
		await restoreDraft(t, restoring.number);
		toast.success(`Restored the draft of ${t.name} to ${restoring.label}`);
	}

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
	<span class="row-actions">
		{#if canRestore}
			<IconButton
				icon={RotateCcw}
				label="Restore the draft to {v.label}"
				onclick={() => {
					restoring = v;
					confirmRestore = true;
				}}
			/>
		{/if}
		{#if canPublish}
			<IconButton
				icon={Trash2}
				label="Delete version {v.label}"
				onclick={() => {
					removing = v;
					confirming = true;
				}}
			/>
		{/if}
	</span>
{/snippet}

{#if versions.isError}
	<ErrorState
		error={versions.error}
		title="The versions could not be loaded."
		onretry={() => versions.refetch()}
	/>
{:else}
	{#if canDuplicate && (versions.data ?? []).length}
		<div class="toolbar">
			<Button icon={Copy} onclick={() => (duplicating = true)}>Duplicate</Button>
		</div>
	{/if}
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

{#if restoring && t}
	<ConfirmDialog
		bind:open={confirmRestore}
		title="Restore the draft to {restoring.label}?"
		message="The draft's files are replaced by the files of this version."
		consequences={[
			'Changes to the draft since then are lost.',
			'Published versions stay as they are.'
		]}
		confirmLabel="Restore draft"
		onconfirm={restore}
	/>
{/if}

{#if canDuplicate && t && versions.data?.length}
	<DuplicateDialog
		bind:open={duplicating}
		source={{ templateId: t.id, name: t.name, versions: versions.data }}
	/>
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
	.toolbar {
		display: flex;
		justify-content: flex-end;
		margin-bottom: var(--space-3);
	}

	.row-actions {
		display: inline-flex;
		gap: 4px;
	}

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
