<script lang="ts">
	// Registry connections (#19): host, which images they match (repository
	// matcher, environment or stack binding, priority), status and last
	// check, last use and the masked credential fingerprint. The owner adds,
	// edits, rotates, revokes, deletes and tests them.
	import { createQuery } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Plus from '@lucide/svelte/icons/plus';
	import { registriesQuery, type RegistryConnection } from '$lib/api/queries';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Skeleton,
		StatusBadge,
		Table,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import CredentialActionHost from '$lib/features/registries/CredentialActionHost.svelte';
	import RegistryDialog from '$lib/features/registries/RegistryDialog.svelte';
	import { checkLabel, maskFingerprint, stackNamesQuery } from '$lib/features/registries/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Registries', crumbs: [{ label: 'Registries' }] });

	const scope = useEnvironmentScope();
	const list = createQuery(() => registriesQuery());
	const stacks = createQuery(() => stackNamesQuery());
	const stackName = (id: string) => stacks.data?.find((s) => s.id === id)?.name ?? 'a stack';
	const rows = $derived(list.data ?? []);
	const owner = $derived(!!scope.perms.data?.owner);

	let dialogOpen = $state(false);
	let editing = $state<RegistryConnection | null>(null);
	let host = $state<CredentialActionHost>();

	function menu(c: RegistryConnection): MenuEntry[] {
		const out: MenuEntry[] = [];
		// Credential administration is owner-only (#19, never delegable):
		// the DTO's actions list only the delegable read capability.
		const manage = owner;
		if (manage) {
			out.push(
				{
					label: 'Test connection…',
					onSelect: () => host?.request({ kind: 'registry', item: c }, 'test')
				},
				{ label: 'Edit…', onSelect: () => ((editing = c), (dialogOpen = true)) },
				{
					label: 'Rotate credential…',
					onSelect: () => host?.request({ kind: 'registry', item: c }, 'rotate')
				},
				{ separator: true }
			);
			if (c.status === 'active')
				out.push({
					label: 'Revoke…',
					tone: 'danger',
					onSelect: () => host?.request({ kind: 'registry', item: c }, 'revoke')
				});
			out.push({
				label: 'Delete…',
				tone: 'danger',
				onSelect: () => host?.request({ kind: 'registry', item: c }, 'delete')
			});
		}
		return out;
	}

	const columns: Column<RegistryConnection>[] = [
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (c) => c.name,
			width: '24%',
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (c) => c.status,
			width: '150px',
			stack: 'status'
		},
		{
			id: 'match',
			header: 'Matches',
			cell: matchCell,
			sortValue: (c) => `${c.host}/${c.repositoryPattern ?? ''}`
		},
		{ id: 'binding', header: 'Used for', cell: bindingCell },
		{
			id: 'priority',
			header: 'Priority',
			cell: priorityCell,
			sortValue: (c) => c.priority ?? 0,
			numeric: true,
			width: '90px'
		},
		{
			id: 'used',
			header: 'Last used',
			cell: usedCell,
			sortValue: (c) => c.lastUsedAt ?? '',
			width: '130px'
		},
		{ id: 'secret', header: 'Credential', cell: secretCell, width: '150px' },
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '56px',
			align: 'end',
			stack: 'actions'
		}
	];
</script>

{#snippet nameCell(c: RegistryConnection)}
	<div class="name-cell">
		<span class="name">{c.name}</span>
		{#if c.username}<span class="sub"
				><span class="mono">{c.username}</span>{c.credentialType === 'password'
					? ', password'
					: ', token'}</span
			>{/if}
	</div>
{/snippet}
{#snippet statusCell(c: RegistryConnection)}
	<div class="name-cell">
		{#if c.status === 'revoked'}<Badge tone="danger" dot>Revoked</Badge>
		{:else if c.lastCheck && c.lastCheck.result !== 'ok'}<Badge tone="warn" dot
				>{checkLabel(c.lastCheck.result)}</Badge
			>
		{:else}<StatusBadge status="online" label="Active" />{/if}
		{#if c.lastCheck}<span class="sub" title={c.lastCheck.at}
				>checked {formatRelative(c.lastCheck.at)}</span
			>{/if}
	</div>
{/snippet}
{#snippet matchCell(c: RegistryConnection)}
	<span class="mono">{c.host}/{c.repositoryPattern || '*'}</span>
	{#if c.plainHttp}<Badge tone="warn">HTTP</Badge>{/if}
{/snippet}
{#snippet bindingCell(c: RegistryConnection)}
	{#if c.stackId}Stack {stackName(c.stackId)}
	{:else if c.environmentId}{scope.name(c.environmentId)}
	{:else}<span class="muted">Every environment</span>{/if}
{/snippet}
{#snippet priorityCell(c: RegistryConnection)}<span class="num">{c.priority ?? 0}</span>{/snippet}
{#snippet usedCell(c: RegistryConnection)}
	{#if c.lastUsedAt}<span class="muted" title={c.lastUsedAt}>{formatRelative(c.lastUsedAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet secretCell(c: RegistryConnection)}
	{#if c.secret?.set}
		<span
			class="mono fp"
			title="Fingerprint of the stored credential (version {c.secret.version})"
			>{maskFingerprint(c.secret.fingerprint)}</span
		>
	{:else}<span class="muted">None stored</span>{/if}
{/snippet}
{#snippet actionsCell(c: RegistryConnection)}
	{#if menu(c).length}
		<Menu items={menu(c)} label="Actions for {c.name}" align="end">
			{#snippet trigger(props)}
				<IconButton {...props} icon={Ellipsis} label="Actions for {c.name}" size="sm" />
			{/snippet}
		</Menu>
	{/if}
{/snippet}

<RegistryDialog bind:open={dialogOpen} connection={editing} />
<CredentialActionHost bind:this={host} />

<div class="bar">
	<p class="muted">
		For each image DockYard picks the most specific match: a stack or environment binding first,
		then the longest repository matcher, then priority. Login raises Docker Hub's pull limit but
		doesn't remove it.
	</p>
	{#if owner}
		<Button
			variant="primary"
			icon={Plus}
			onclick={() => ((editing = null), (dialogOpen = true))}>Add connection</Button
		>
	{/if}
</div>

{#if list.isError}
	<ErrorState
		error={list.error}
		title="The registry connections could not be loaded."
		onretry={() => list.refetch()}
	/>
{:else}
	<Card padding="none">
		{#if list.isPending}
			<div class="loading" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
		{:else}
			<Table
				label="Registry connections"
				{rows}
				{columns}
				rowKey={(c) => c.id}
				sort={{ column: 'name', direction: 'asc' }}
			>
				{#snippet empty()}
					<EmptyState
						icon={Archive}
						color="slate"
						title="No registry connections yet."
						description="Public images need none. Add one to pull private images, or to pull from Docker Hub with your account."
						level={2}
						compact
					>
						{#snippet actions()}
							{#if owner}
								<Button
									variant="primary"
									icon={Plus}
									onclick={() => ((editing = null), (dialogOpen = true))}
									>Add connection</Button
								>
							{/if}
						{/snippet}
					</EmptyState>
				{/snippet}
			</Table>
		{/if}
	</Card>
{/if}

<style>
	.bar {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: var(--space-4);
	}

	.bar p {
		max-width: 72ch;
		font-size: var(--text-caption);
	}

	.name-cell {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 2px;
		min-width: 0;
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.sub {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.fp {
		white-space: nowrap;
		font-size: var(--text-caption);
		color: var(--text-muted);
	}

	.loading {
		padding: var(--space-5);
	}

	@media (max-width: 767px) {
		.bar {
			flex-direction: column;
		}
	}
</style>
