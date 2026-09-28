<script lang="ts">
	// Registry connections (#19): name and login, status and last check,
	// which images they match and what they are bound to, and last use. The
	// priority column shows only when priorities differ; the credential's
	// fingerprint is in the edit dialog. The owner adds (the header's "Add
	// connection", ?create=1), edits, rotates, revokes, deletes and tests
	// them.
	import { createQuery } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import { registriesQuery, type RegistryConnection } from '$lib/api/queries';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Card,
		EmptyState,
		ErrorState,
		IconButton,
		Menu,
		Skeleton,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import CredentialActionHost from '$lib/features/registries/CredentialActionHost.svelte';
	import RegistryDialog from '$lib/features/registries/RegistryDialog.svelte';
	import { checkLabel, stackNamesQuery } from '$lib/features/registries/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({ title: 'Registries', crumbs: [{ label: 'Registries' }] });

	const scope = useEnvironmentScope();
	const list = createQuery(() => registriesQuery());
	const stacks = createQuery(() => stackNamesQuery());
	const stackName = (id: string) => stacks.data?.find((s) => s.id === id)?.name ?? 'a stack';
	const rows = $derived(list.data ?? []);
	const owner = $derived(!!scope.perms.data?.owner);
	const samePriority = $derived(new Set(rows.map((c) => c.priority ?? 0)).size <= 1);

	const createDialog = urlDialog('create');
	let editOpen = $state(false);
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
					label: 'Test connection',
					onSelect: () => host?.request({ kind: 'registry', item: c }, 'test')
				},
				{ label: 'Edit', onSelect: () => ((editing = c), (editOpen = true)) },
				{
					label: 'Rotate credential',
					onSelect: () => host?.request({ kind: 'registry', item: c }, 'rotate')
				},
				{ separator: true }
			);
			if (c.status === 'active')
				out.push({
					label: 'Revoke',
					tone: 'danger',
					onSelect: () => host?.request({ kind: 'registry', item: c }, 'revoke')
				});
			out.push({
				label: 'Delete',
				tone: 'danger',
				onSelect: () => host?.request({ kind: 'registry', item: c }, 'delete')
			});
		}
		return out;
	}

	const columns: Column<RegistryConnection>[] = $derived([
		{
			id: 'name',
			header: 'Name',
			cell: nameCell,
			sortValue: (c) => c.name,
			maxWidth: '300px',
			stack: 'title'
		},
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (c) => c.status,
			width: '190px',
			stack: 'status'
		},
		{
			id: 'match',
			header: 'Matches',
			cell: matchCell,
			sortValue: (c) => `${c.host}/${c.repositoryPattern ?? ''}`,
			maxWidth: '320px',
			stack: 'meta'
		},
		{ id: 'binding', header: 'Used for', cell: bindingCell, stack: 'meta' },
		...(samePriority
			? []
			: [
					{
						id: 'priority',
						header: 'Priority',
						cell: priorityCell,
						sortValue: (c: RegistryConnection) => c.priority ?? 0,
						numeric: true,
						width: '90px',
						stack: 'meta'
					} satisfies Column<RegistryConnection>
				]),
		{
			id: 'used',
			header: 'Last used',
			cell: usedCell,
			sortValue: (c) => c.lastUsedAt ?? '',
			width: '130px',
			stack: 'meta'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '56px',
			align: 'end',
			pin: 'end',
			stack: 'head'
		}
	]);
</script>

{#snippet nameCell(c: RegistryConnection)}<NameCell
		icon="registry"
		name={c.name}
		sub={c.username
			? `${c.username}, ${c.credentialType === 'password' ? 'password' : 'token'}`
			: undefined}
	/>{/snippet}
{#snippet statusCell(c: RegistryConnection)}
	<div class="status">
		{#if c.status === 'revoked'}<Badge tone="danger" dot>Revoked</Badge>
		{:else if c.lastCheck && c.lastCheck.result !== 'ok'}<Badge tone="warn" dot
				>{checkLabel(c.lastCheck.result)}</Badge
			>
		{:else}<StatusBadge status="online" label="Active" />{/if}
		{#if c.lastCheck}<span class="sub" title={formatDateTime(c.lastCheck.at)}
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
	{#if c.lastUsedAt}<span class="muted" title={formatDateTime(c.lastUsedAt)}
			>{formatRelative(c.lastUsedAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
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

<RegistryDialog bind:open={() => createDialog.open, (v) => (createDialog.open = v)} />
<RegistryDialog bind:open={editOpen} connection={editing} />
<CredentialActionHost bind:this={host} />

{#if list.isError}
	<ErrorState
		error={list.error}
		title="The registry connections could not be loaded."
		onretry={() => list.refetch()}
	/>
{:else}
	<Card
		title="Connections"
		subtitle="For each image Docker Manager picks the most specific matching connection."
		padding="none"
	>
		<div class="how">
			<Disclosure summary="How a connection is chosen">
				<p class="muted">
					A connection bound to the image's stack or environment comes first, then the
					longest repository match, then the higher priority. Signing in to Docker Hub
					raises its pull limit but doesn't remove it.
				</p>
			</Disclosure>
		</div>
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
						{...resourceIcon('registry')}
						title="No registry connections yet."
						description="Public images need none. Add one to pull private images, or to pull from Docker Hub with your account."
						level={3}
						compact
					/>
				{/snippet}
			</Table>
		{/if}
	</Card>
{/if}

<style>
	.how {
		padding: 0 var(--space-5) var(--space-3);
	}

	.status {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 2px;
		min-width: 0;
	}

	.sub {
		color: var(--text-muted);
		font-size: var(--text-caption);
		white-space: nowrap;
	}

	.loading {
		padding: var(--space-5);
	}
</style>
