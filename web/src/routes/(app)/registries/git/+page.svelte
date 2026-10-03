<script lang="ts">
	// Git credentials (#33): HTTPS tokens for builds from private
	// repositories; managed like registry connections (write-only token,
	// rotate, revoke, test with a repository URL). The header's "Add
	// Credential" opens the dialog (?create=1).
	import { createQuery } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import { gitCredentialsQuery, type GitCredential } from '$lib/api/queries';
	import { routes } from '$lib/routes';
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
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import CredentialActionHost from '$lib/features/registries/CredentialActionHost.svelte';
	import GitCredentialDialog from '$lib/features/registries/GitCredentialDialog.svelte';
	import { checkLabel } from '$lib/features/registries/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({
		title: 'Git Credentials',
		crumbs: [{ label: 'Registries', href: routes.registries() }, { label: 'Git Credentials' }]
	});

	const scope = useEnvironmentScope();
	const list = createQuery(() => gitCredentialsQuery());
	const rows = $derived(list.data ?? []);
	const owner = $derived(!!scope.perms.data?.owner);
	const createDialog = urlDialog('create');
	let editOpen = $state(false);
	let editing = $state<GitCredential | null>(null);
	let host = $state<CredentialActionHost>();

	function menu(c: GitCredential): MenuEntry[] {
		const out: MenuEntry[] = [];
		// Credential administration is owner-only (#19, never delegable):
		// the DTO's actions list only the delegable read capability.
		const manage = owner;
		if (manage) {
			out.push(
				{
					label: 'Test Connection',
					onSelect: () => host?.request({ kind: 'git', item: c }, 'test')
				},
				{ label: 'Edit', onSelect: () => ((editing = c), (editOpen = true)) },
				{
					label: 'Rotate Token',
					onSelect: () => host?.request({ kind: 'git', item: c }, 'rotate')
				},
				{ separator: true }
			);
			if (c.status === 'active')
				out.push({
					label: 'Revoke',
					tone: 'danger',
					onSelect: () => host?.request({ kind: 'git', item: c }, 'revoke')
				});
			out.push({
				label: 'Delete',
				tone: 'danger',
				onSelect: () => host?.request({ kind: 'git', item: c }, 'delete')
			});
		}
		return out;
	}

	const columns: Column<GitCredential>[] = [
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
			header: 'Repositories',
			cell: matchCell,
			sortValue: (c) => `${c.host}/${c.pathPrefix ?? ''}`,
			maxWidth: '320px',
			stack: 'meta'
		},
		{
			id: 'used',
			header: 'Last Used',
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
	];
</script>

{#snippet nameCell(c: GitCredential)}<NameCell
		icon="gitCredential"
		name={c.name}
		sub={c.username}
		subMono
	/>{/snippet}
{#snippet statusCell(c: GitCredential)}
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
{#snippet matchCell(c: GitCredential)}
	<span class="mono">{c.host}/{c.pathPrefix ? `${c.pathPrefix}/…` : '…'}</span>
	{#if c.plainHttp}<Badge tone="warn">HTTP</Badge>{/if}
{/snippet}
{#snippet usedCell(c: GitCredential)}
	{#if c.lastUsedAt}<span class="muted" title={formatDateTime(c.lastUsedAt)}
			>{formatRelative(c.lastUsedAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet actionsCell(c: GitCredential)}
	{#if menu(c).length}
		<Menu items={menu(c)} label="Actions for {c.name}" align="end">
			{#snippet trigger(props)}
				<IconButton {...props} icon={Ellipsis} label="Actions for {c.name}" size="sm" />
			{/snippet}
		</Menu>
	{/if}
{/snippet}

<GitCredentialDialog bind:open={() => createDialog.open, (v) => (createDialog.open = v)} />
<GitCredentialDialog bind:open={editOpen} credential={editing} />
<CredentialActionHost bind:this={host} />

{#if list.isError}
	<ErrorState
		error={list.error}
		title="The Git credentials could not be loaded."
		onretry={() => list.refetch()}
	/>
{:else}
	<Card
		title="Credentials"
		info="Builds from private HTTPS repositories use the credential whose host and path match."
		padding="none"
	>
		{#if list.isPending}
			<div class="loading" aria-busy="true"><Skeleton lines={3} height="20px" /></div>
		{:else}
			<Table
				label="Git Credentials"
				{rows}
				{columns}
				rowKey={(c) => c.id}
				sort={{ column: 'name', direction: 'asc' }}
			>
				{#snippet empty()}
					<EmptyState
						{...resourceIcon('gitCredential')}
						title="No Git credentials yet."
						description="Public repositories need none. Add one to build from a private repository."
						level={3}
						compact
					/>
				{/snippet}
			</Table>
		{/if}
	</Card>
{/if}

<style>
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
