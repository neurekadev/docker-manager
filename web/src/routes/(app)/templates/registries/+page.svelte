<script lang="ts">
	// Template registries (template registry): this instance's own registry
	// (its URL to share, never removable) and the registries of other Docker
	// Manager instances with their sync state. The owner adds, syncs and
	// removes registries; removing one keeps stacks created from its
	// templates working (they lose its icons until it is added back).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import Plus from '@lucide/svelte/icons/plus';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { myPermissionsQuery } from '$lib/api/queries';
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
		Skeleton,
		Table,
		errorMessage,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';

	usePage({
		title: 'Template registries',
		crumbs: [{ label: 'Templates', href: routes.templates() }, { label: 'Registries' }]
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
			{ id: 'name', header: 'Registry', cell: nameCell, sortValue: (r) => r.name },
			{ id: 'status', header: 'Status', cell: statusCell, width: '200px' },
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
				header: 'Last sync',
				cell: syncedCell,
				sortValue: (r) => r.syncedAt ?? '',
				width: '160px'
			}
		];
		if (owner) cols.push({ id: 'actions', header: '', cell: actionsCell, width: '96px' });
		return cols;
	});
</script>

{#snippet nameCell(r: TemplateRegistryInfo)}
	<span class="name">
		<span class="title">{r.name}</span>
		<span class="url mono">{r.url}</span>
	</span>
{/snippet}
{#snippet statusCell(r: TemplateRegistryInfo)}
	{#if r.status === 'error'}
		<span class="status">
			<Badge tone="warn" dot>Sync failed</Badge>
			{#if r.errorMessage}<span class="muted small">{r.errorMessage}</span>{/if}
		</span>
	{:else}<Badge tone="ok" dot>Up to date</Badge>{/if}
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
			label="Sync {r.name} now"
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

<div class="page">
	<header class="head">
		<div>
			<h1>Template registries</h1>
			<p class="muted">
				Other Docker Manager instances whose public templates you browse and create stacks
				from. They are synced every 30 minutes.
			</p>
		</div>
		{#if owner}
			<Button variant="primary" icon={Plus} onclick={() => (adding = true)}
				>Add registry</Button
			>
		{/if}
	</header>

	{#if own.data}<OwnRegistryCard templates={own.data} />{/if}

	{#if registries.isError}
		<ErrorState
			error={registries.error}
			title="The registries could not be loaded."
			onretry={() => registries.refetch()}
		/>
	{:else}
		<Card padding="none" title="Added registries" id="registries">
			{#if registries.isPending}
				<div class="loading" aria-busy="true"><Skeleton lines={3} height="20px" /></div>
			{:else}
				<Table
					label="Added registries"
					rows={remote}
					{columns}
					rowKey={(r) => r.instanceId}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}
						<EmptyState
							icon={Archive}
							color="violet"
							title="No registries added yet."
							description={owner
								? "Add another Docker Manager's address to browse and use its public templates."
								: 'The owner of this Docker Manager adds registries.'}
							level={3}
							compact
						>
							{#snippet actions()}
								{#if owner}<Button variant="primary" onclick={() => (adding = true)}
										>Add registry</Button
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
					'They show their template icon again if you add the registry back.'
				]}
				confirmLabel="Remove registry"
				tone="danger"
				onconfirm={remove}
			/>
		{/if}
	{/if}
</div>

<style>
	.page {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}

	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: var(--space-4);
	}

	h1 {
		font-size: var(--text-title);
		line-height: var(--leading-title);
		letter-spacing: -0.01em;
	}

	.head p {
		max-width: 72ch;
		margin-top: 2px;
		font-size: var(--text-control);
	}

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
