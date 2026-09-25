<script lang="ts">
	// Git credentials (#33): HTTPS tokens for builds from private
	// repositories; managed like registry connections (write-only token,
	// fingerprint, rotate, revoke, test with a repository URL).
	import { createQuery } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import GitBranch from '@lucide/svelte/icons/git-branch';
	import Plus from '@lucide/svelte/icons/plus';
	import { gitCredentialsQuery, type GitCredential } from '$lib/api/queries';
	import { routes } from '$lib/routes';
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
	import GitCredentialDialog from '$lib/features/registries/GitCredentialDialog.svelte';
	import { checkLabel, maskFingerprint } from '$lib/features/registries/model';
	import { useEnvironmentScope } from '$lib/features/resources/scope.svelte';

	usePage({
		title: 'Git credentials',
		crumbs: [{ label: 'Registries', href: routes.registries() }, { label: 'Git credentials' }]
	});

	const scope = useEnvironmentScope();
	const list = createQuery(() => gitCredentialsQuery());
	const rows = $derived(list.data ?? []);
	const owner = $derived(!!scope.perms.data?.owner);
	let dialogOpen = $state(false);
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
					label: 'Test connection…',
					onSelect: () => host?.request({ kind: 'git', item: c }, 'test')
				},
				{ label: 'Edit…', onSelect: () => ((editing = c), (dialogOpen = true)) },
				{
					label: 'Rotate token…',
					onSelect: () => host?.request({ kind: 'git', item: c }, 'rotate')
				},
				{ separator: true }
			);
			if (c.status === 'active')
				out.push({
					label: 'Revoke…',
					tone: 'danger',
					onSelect: () => host?.request({ kind: 'git', item: c }, 'revoke')
				});
			out.push({
				label: 'Delete…',
				tone: 'danger',
				onSelect: () => host?.request({ kind: 'git', item: c }, 'delete')
			});
		}
		return out;
	}

	const columns: Column<GitCredential>[] = [
		{ id: 'name', header: 'Name', cell: nameCell, sortValue: (c) => c.name, stack: 'title' },
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
			header: 'Repositories',
			cell: matchCell,
			sortValue: (c) => `${c.host}/${c.pathPrefix ?? ''}`
		},
		{
			id: 'used',
			header: 'Last used',
			cell: usedCell,
			sortValue: (c) => c.lastUsedAt ?? '',
			width: '130px'
		},
		{ id: 'secret', header: 'Token', cell: secretCell, width: '150px' },
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

{#snippet nameCell(c: GitCredential)}
	<div class="name-cell">
		<span class="name">{c.name}</span>
		{#if c.username}<span class="sub mono">{c.username}</span>{/if}
	</div>
{/snippet}
{#snippet statusCell(c: GitCredential)}
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
{#snippet matchCell(c: GitCredential)}
	<span class="mono">{c.host}/{c.pathPrefix ? `${c.pathPrefix}/…` : '…'}</span>
	{#if c.plainHttp}<Badge tone="warn">HTTP</Badge>{/if}
{/snippet}
{#snippet usedCell(c: GitCredential)}
	{#if c.lastUsedAt}<span class="muted" title={c.lastUsedAt}>{formatRelative(c.lastUsedAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet secretCell(c: GitCredential)}
	{#if c.secret?.set}
		<span class="mono fp" title="Fingerprint of the stored token (version {c.secret.version})"
			>{maskFingerprint(c.secret.fingerprint)}</span
		>
	{:else}<span class="muted">None stored</span>{/if}
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

<GitCredentialDialog bind:open={dialogOpen} credential={editing} />
<CredentialActionHost bind:this={host} />

<div class="bar">
	<p class="muted">
		Builds from private HTTPS repositories use the credential whose host and path match the
		repository. SSH Git access is not supported in v1.
	</p>
	{#if owner}
		<Button
			variant="primary"
			icon={Plus}
			onclick={() => ((editing = null), (dialogOpen = true))}>Add credential</Button
		>
	{/if}
</div>

{#if list.isError}
	<ErrorState
		error={list.error}
		title="The Git credentials could not be loaded."
		onretry={() => list.refetch()}
	/>
{:else}
	<Card padding="none">
		{#if list.isPending}
			<div class="loading" aria-busy="true"><Skeleton lines={3} height="20px" /></div>
		{:else}
			<Table
				label="Git credentials"
				{rows}
				{columns}
				rowKey={(c) => c.id}
				sort={{ column: 'name', direction: 'asc' }}
			>
				{#snippet empty()}
					<EmptyState
						icon={GitBranch}
						color="slate"
						title="No Git credentials yet."
						description="Public repositories need none. Add one to build images from a private repository."
						level={2}
						compact
					>
						{#snippet actions()}
							{#if owner}
								<Button
									variant="primary"
									icon={Plus}
									onclick={() => ((editing = null), (dialogOpen = true))}
									>Add credential</Button
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
