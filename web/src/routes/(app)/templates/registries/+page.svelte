<script lang="ts">
	// Template sources (template registries, route /templates/registries):
	// this instance's own registry (its address to share, never removable)
	// and the registries of other Docker Manager instances with their sync
	// state, called "template sources" in the UI. The owner adds, syncs and
	// removes them; removing one keeps stacks created from its templates
	// working (they lose its icons until it is added back).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import Plus from '@lucide/svelte/icons/plus';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { myPermissionsQuery } from '$lib/api/queries';
	import IconCell from '$lib/features/common/IconCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import AddRegistryDialog from '$lib/features/templates/AddRegistryDialog.svelte';
	import OwnRegistryCard from '$lib/features/templates/OwnRegistryCard.svelte';
	import { removeRegistry, syncRegistry } from '$lib/features/templates/actions';
	import {
		templateKeys,
		templateRegistriesQuery,
		templatesQuery,
		type TemplateRegistryInfo
	} from '$lib/features/templates/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		EmptyState,
		ErrorState,
		IconButton,
		PageHeader,
		Skeleton,
		Table,
		errorMessage,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';

	usePage({
		title: 'Template Sources',
		crumbs: [{ label: 'Templates', href: routes.templates() }, { label: 'Template Sources' }]
	});

	const registries = createQuery(() => templateRegistriesQuery());
	const own = createQuery(() => templatesQuery());
	const perms = createQuery(() => myPermissionsQuery());
	const queryClient = useQueryClient();
	const owner = $derived(!!perms.data?.owner);
	const remote = $derived((registries.data ?? []).filter((r) => !r.own));

	let adding = $state(false);
	let removing = $state<TemplateRegistryInfo | null>(null);
	let confirming = $state(false);
	let syncing = $state<string | null>(null);

	async function sync(r: TemplateRegistryInfo) {
		syncing = r.instanceId;
		try {
			const out = await syncRegistry(r.instanceId);
			void queryClient.invalidateQueries({ queryKey: templateKeys.all });
			if (out.status === 'error')
				toast.error(`${r.name} could not be synced`, { body: out.errorMessage });
			else toast.success(`Synced ${r.name}`);
		} catch (e) {
			toast.error(`${r.name} could not be synced`, { body: errorMessage(e) });
		} finally {
			syncing = null;
		}
	}

	async function remove() {
		if (!removing) return;
		await removeRegistry(removing.instanceId);
		void queryClient.invalidateQueries({ queryKey: templateKeys.all });
		toast.success(`Removed ${removing.name}`);
	}

	const columns = $derived.by((): Column<TemplateRegistryInfo>[] => {
		const cols: Column<TemplateRegistryInfo>[] = [
			{
				id: 'name',
				header: 'Source',
				cell: nameCell,
				sortValue: (r) => r.name,
				maxWidth: '360px',
				stack: 'title'
			},
			{ id: 'status', header: 'Status', cell: statusCell, width: '200px', stack: 'status' },
			{
				id: 'templates',
				header: 'Templates',
				cell: countCell,
				sortValue: (r) => r.templates,
				numeric: true,
				width: '120px'
			},
			{
				id: 'synced',
				header: 'Last Sync',
				cell: syncedCell,
				sortValue: (r) => r.syncedAt ?? '',
				width: '160px'
			}
		];
		if (owner)
			cols.push({
				id: 'actions',
				header: 'Actions',
				hideHeader: true,
				cell: actionsCell,
				width: '96px',
				pin: 'end',
				stack: 'head'
			});
		return cols;
	});
</script>

{#snippet nameCell(r: TemplateRegistryInfo)}
	<IconCell icon="templateRegistry">
		<span class="name">
			<span class="title">{r.name}</span>
			<span class="url mono" title={r.url}>{r.url}</span>
		</span>
	</IconCell>
{/snippet}
{#snippet statusCell(r: TemplateRegistryInfo)}
	{#if r.status === 'error'}
		<span class="status">
			<Badge tone="warn" dot>Sync Failed</Badge>
			{#if r.errorMessage}<span class="muted small">{r.errorMessage}</span>{/if}
		</span>
	{:else}<Badge tone="ok" dot>Up to Date</Badge>{/if}
{/snippet}
{#snippet countCell(r: TemplateRegistryInfo)}<span class="num">{r.templates}</span>{/snippet}
{#snippet syncedCell(r: TemplateRegistryInfo)}
	{#if r.syncedAt}<span title={formatDateTime(r.syncedAt)}>{formatRelative(r.syncedAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet actionsCell(r: TemplateRegistryInfo)}
	<span class="row-actions">
		<IconButton
			icon={RefreshCw}
			label="Sync {r.name} Now"
			disabled={syncing === r.instanceId}
			onclick={() => void sync(r)}
		/>
		<IconButton
			icon={Trash2}
			label="Remove {r.name}"
			onclick={() => {
				removing = r;
				confirming = true;
			}}
		/>
	</span>
{/snippet}

<Page>
	<PageHeader
		title="Template Sources"
		info="Other Docker Managers whose public templates you can use. They sync every 30 minutes."
	>
		{#snippet actions()}
			{#if owner}
				<Button variant="primary" icon={Plus} onclick={() => (adding = true)}
					>Add Template Source</Button
				>
			{/if}
		{/snippet}
	</PageHeader>

	{#if own.data}<OwnRegistryCard templates={own.data} />{/if}

	{#if registries.isError}
		<ErrorState
			error={registries.error}
			title="The template sources could not be loaded."
			onretry={() => registries.refetch()}
		/>
	{:else}
		<Card padding="none" title="Other Docker Managers" id="registries">
			{#if registries.isPending}
				<div class="loading" aria-busy="true"><Skeleton lines={3} height="20px" /></div>
			{:else}
				<Table
					label="Template Sources"
					rows={remote}
					{columns}
					rowKey={(r) => r.instanceId}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}
						<EmptyState
							icon={Archive}
							color="violet"
							title="No template sources yet."
							description={owner
								? "Add another Docker Manager's address to browse and use its public templates."
								: 'The owner of this Docker Manager adds template sources.'}
							level={3}
							compact
						>
							{#snippet actions()}
								{#if owner}<Button variant="primary" onclick={() => (adding = true)}
										>Add Template Source</Button
									>{/if}
							{/snippet}
						</EmptyState>
					{/snippet}
				</Table>
			{/if}
		</Card>
	{/if}

	{#if owner}
		<AddRegistryDialog bind:open={adding} />
		{#if removing}
			<ConfirmDialog
				bind:open={confirming}
				title="Remove {removing.name}?"
				message="Its templates disappear from Templates."
				consequences={[
					'Stacks created from its templates keep working.',
					'They show their template icon again if you add the source back.'
				]}
				confirmLabel="Remove Template Source"
				tone="danger"
				onconfirm={remove}
			/>
		{/if}
	{/if}
</Page>

<style>
	.loading {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.name,
	.status {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.title {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.url,
	.small {
		overflow: hidden;
		color: var(--text-muted);
		font-size: var(--text-caption);
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.row-actions {
		display: inline-flex;
		gap: 4px;
	}
</style>
