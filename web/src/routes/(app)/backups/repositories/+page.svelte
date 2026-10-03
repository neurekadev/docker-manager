<script lang="ts">
	// Backup repositories (#10): where backups are stored, whether the
	// Recovery Key is confirmed for each, the last connection test and
	// verification. Credentials and the key are never shown.
	import { createQuery } from '@tanstack/svelte-query';
	import Plus from '@lucide/svelte/icons/plus';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		Table,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';
	import { environmentName } from '$lib/features/common/data';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import BackupsHeader from '$lib/features/backups/BackupsHeader.svelte';
	import {
		connectionTestText,
		repositoryLocation,
		type BackupRepository
	} from '$lib/features/backups/model';
	import { repositoriesQuery } from '$lib/features/backups/queries';

	usePage({
		title: 'Backup Repositories',
		crumbs: [{ label: 'Backups', href: routes.backups() }, { label: 'Repositories' }]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const envName = (id: string) => environmentName(envs.data, id);

	const columns: Column<BackupRepository>[] = [
		{
			id: 'name',
			header: 'Repository',
			cell: nameCell,
			sortValue: (r) => r.name,
			maxWidth: '420px',
			stack: 'title'
		},
		{ id: 'state', header: 'Recovery Key', cell: stateCell, width: '190px', stack: 'status' },
		{ id: 'test', header: 'Connection', cell: testCell, width: '240px', stack: 'meta' },
		{
			id: 'verified',
			header: 'Last Verified',
			cell: verifiedCell,
			width: '150px',
			stack: 'hidden'
		}
	];
</script>

{#snippet nameCell(r: BackupRepository)}
	<NameCell
		icon="backupRepository"
		name={r.name}
		href={routes.backupRepository(r.id)}
		sub={repositoryLocation(r)}
	/>
{/snippet}
{#snippet stateCell(r: BackupRepository)}
	{#if r.state === 'ready'}<Badge tone="ok" dot>Confirmed</Badge>{:else}<Badge tone="warn" dot
			>Awaiting Confirmation</Badge
		>{/if}
{/snippet}
{#snippet testCell(r: BackupRepository)}
	{#if r.lastTest}
		<span class:danger={!r.lastTest.ok} title={formatDateTime(r.lastTest.at)}
			>{connectionTestText(r.lastTest)}</span
		>
	{:else}<span class="muted">Not tested yet</span>{/if}
{/snippet}
{#snippet verifiedCell(r: BackupRepository)}
	{#if r.verification?.lastVerifiedAt}<span
			class="num"
			title={formatDateTime(r.verification.lastVerifiedAt)}
			>{formatRelative(r.verification.lastVerifiedAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}

<Page>
	<BackupsHeader>
		{#snippet actions()}
			<!-- Without a ready repository the header's primary button adds one. -->
			{#if perms.data?.owner && (repos.data ?? []).some((r) => r.state === 'ready')}
				<Button icon={Plus} href={routes.backupRepositoryNew()}>Add Repository</Button>
			{/if}
		{/snippet}
	</BackupsHeader>
	<Card title="Repositories" padding="none">
		<QueryView query={repos} errorTitle="The backup repositories could not be loaded.">
			{#snippet children(rows)}
				<Table
					label="Backup Repositories"
					{rows}
					{columns}
					rowKey={(r) => r.id}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}
						<EmptyState
							{...resourceIcon('backupRepository')}
							title="No backup repositories yet."
							description="Add an S3 bucket. The first repository creates your Recovery Key."
							level={3}
							compact
						>
							{#snippet actions()}
								{#if perms.data?.owner}
									<Button
										variant="primary"
										icon={Plus}
										href={routes.backupRepositoryNew()}>Add Repository</Button
									>
								{/if}
							{/snippet}
						</EmptyState>
					{/snippet}
				</Table>
			{/snippet}
		</QueryView>
	</Card>
</Page>

<style>
	.danger {
		color: var(--danger);
	}
</style>
