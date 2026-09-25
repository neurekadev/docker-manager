<script lang="ts">
	// Backups (#10): the shared backup history of the instance. Recent sets
	// (partial ones stay partial, with each member's own snapshot time) and
	// every snapshot, whoever configured the policy.
	import { createQuery } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Plus from '@lucide/svelte/icons/plus';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		KpiCard,
		Notice,
		Table,
		formatBytes,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';
	import { can, has } from '$lib/features/common/access';
	import { environmentName } from '$lib/features/common/data';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import BackupsHeader from '$lib/features/backups/BackupsHeader.svelte';
	import SetsTable from '$lib/features/backups/SetsTable.svelte';
	import {
		CONSISTENCY_LABEL,
		KIND_LABEL,
		itemName,
		recentSets,
		type Backup
	} from '$lib/features/backups/model';
	import {
		backupPoliciesWithSetsQuery,
		backupsQuery,
		repositoriesQuery
	} from '$lib/features/backups/queries';

	usePage({ title: 'Backups', crumbs: [{ label: 'Backups' }], environmentScoped: true });

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const policies = createQuery(() => backupPoliciesWithSetsQuery());
	const backups = createQuery(() =>
		backupsQuery(environmentSelection.id ? { environmentId: environmentSelection.id } : {})
	);
	const envName = (id: string) => environmentName(envs.data, id);

	const sets = $derived(
		recentSets(policies.data ?? []).filter(
			(s) =>
				!environmentSelection.id ||
				s.members.some((m) => m.environmentId === environmentSelection.id)
		)
	);
	const awaiting = $derived(
		(repos.data ?? []).filter((r) => r.state === 'awaiting_confirmation')
	);
	const lastComplete = $derived(sets.find((s) => s.state === 'complete'));
	const troubled = $derived(
		sets.filter((s) => s.state === 'partial' || s.state === 'failed').length
	);
	const totalBytes = $derived((backups.data ?? []).reduce((n, b) => n + (b.bytes ?? 0), 0));

	const columns: Column<Backup>[] = [
		{
			id: 'item',
			header: 'Backup',
			cell: itemCell,
			sortValue: (b) => itemName(b),
			stack: 'title'
		},
		{ id: 'state', header: 'State', cell: stateCell, width: '130px', stack: 'status' },
		{
			id: 'time',
			header: 'Snapshot time',
			cell: timeCell,
			sortValue: (b) => b.snapshotTime,
			width: '190px'
		},
		{
			id: 'env',
			header: 'Environment',
			cell: envCell,
			sortValue: (b) =>
				b.kind === 'manager_state' || !b.environmentId ? '' : envName(b.environmentId),
			width: '130px'
		},
		{ id: 'consistency', header: 'Consistency', cell: consistencyCell, width: '200px' },
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			sortValue: (b) => b.bytes ?? null,
			numeric: true,
			width: '100px'
		}
	];
</script>

{#snippet itemCell(b: Backup)}
	<NameCell
		name={itemName(b)}
		href={routes.backup(b.id)}
		sub={[
			b.kind && b.kind !== 'manager_state' ? KIND_LABEL[b.kind] : undefined,
			policies.data?.find((p) => p.id === b.policyId)?.name
		]
			.filter(Boolean)
			.join(', ') || undefined}
	/>
{/snippet}
{#snippet stateCell(b: Backup)}
	{#if b.state === 'complete'}<Badge tone="ok" dot>Complete</Badge>{:else}<Badge tone="warn" dot
			>Partial</Badge
		>{/if}
{/snippet}
{#snippet timeCell(b: Backup)}<span class="num">{formatDateTime(b.snapshotTime)}</span>{/snippet}
{#snippet envCell(b: Backup)}{b.kind === 'manager_state' || !b.environmentId
		? 'Manager'
		: envName(b.environmentId)}{/snippet}
{#snippet consistencyCell(b: Backup)}
	{b.consistency ? CONSISTENCY_LABEL[b.consistency] : '—'}
{/snippet}
{#snippet sizeCell(b: Backup)}<span class="num">{formatBytes(b.bytes)}</span>{/snippet}

<Page>
	<BackupsHeader>
		{#snippet actions()}
			{#if can(access, 'backup_policy.manage') && (repos.data?.length ?? 0) > 0}
				<Button variant="primary" icon={Plus} href={routes.backupPolicyNew()}
					>Create backup policy</Button
				>
			{/if}
		{/snippet}
	</BackupsHeader>

	{#each awaiting as r (r.id)}
		<Notice
			tone="warn"
			icon={KeyRound}
			title="Confirm the Recovery Key for {r.name}"
			live="none"
		>
			Nothing is backed up to this repository until the owner re-enters the Recovery Key.
			{#snippet actions()}
				<Button size="sm" href={routes.backupRepository(r.id)}>Confirm the key</Button>
			{/snippet}
		</Notice>
	{/each}

	{#if repos.data && repos.data.length === 0}
		<Card>
			<EmptyState
				icon={DatabaseBackup}
				color="teal"
				title="No backups yet."
				description="Add a backup repository on a local disk or S3 and save your Recovery Key, then create a policy that backs up the manager, stacks and volumes."
				level={2}
			>
				{#snippet actions()}
					{#if access.owner || can(access, 'backup_repository.manage')}
						<Button variant="primary" icon={Plus} href={routes.backupRepositoryNew()}
							>Add backup repository</Button
						>
					{/if}
				{/snippet}
			</EmptyState>
		</Card>
	{:else}
		<KpiRow>
			<KpiCard
				label="Last complete set"
				value={lastComplete ? formatRelative(lastComplete.startedAt) : 'None yet'}
				secondary={lastComplete
					? formatDateTime(lastComplete.startedAt)
					: 'Run a policy to create one'}
				icon={DatabaseBackup}
				color="teal"
				tone={lastComplete ? 'ok' : undefined}
			/>
			<KpiCard
				label="Partial or failed sets"
				value={String(troubled)}
				secondary="Of the {sets.length} most recent"
				icon={TriangleAlert}
				color="rose"
				tone={troubled ? 'warn' : undefined}
			/>
			<KpiCard
				label="Snapshots"
				value={String(backups.data?.length ?? 0)}
				secondary="{formatBytes(totalBytes)} stored"
				icon={Archive}
				color="blue"
			/>
			<KpiCard
				label="Repositories"
				value={String(repos.data?.length ?? 0)}
				secondary={awaiting.length
					? `${awaiting.length} awaiting key confirmation`
					: 'All ready'}
				icon={HardDrive}
				color="slate"
				tone={awaiting.length ? 'warn' : undefined}
			/>
		</KpiRow>

		<Card title="Recent backup sets" padding="none">
			<QueryView query={policies} errorTitle="The backup sets could not be loaded.">
				{#snippet children(list)}
					{#if list.length && sets.length}
						<SetsTable
							{sets}
							label="Recent backup sets"
							environmentName={envName}
							canRetry={(id) =>
								has(
									policies.data?.find((p) => p.id === id),
									'backup.run'
								)}
						/>
					{:else}
						<EmptyState
							icon={DatabaseBackup}
							color="teal"
							title="No backup sets yet."
							description="Run a backup policy, or turn its schedule on."
							level={3}
							compact
						>
							{#snippet actions()}
								<Button href={routes.backupPolicies()}>Open policies</Button>
							{/snippet}
						</EmptyState>
					{/if}
				{/snippet}
			</QueryView>
		</Card>

		<Card title="Snapshots" padding="none">
			<QueryView query={backups} errorTitle="The backups could not be loaded.">
				{#snippet children(rows)}
					<Table
						label="Backup snapshots"
						{rows}
						{columns}
						rowKey={(b) => b.id}
						sort={{ column: 'time', direction: 'desc' }}
						maxHeight="560px"
					>
						{#snippet empty()}
							<EmptyState
								icon={Archive}
								color="slate"
								title="No snapshots here."
								description={environmentSelection.id
									? 'Nothing from this environment is backed up yet. Add its stacks or volumes to a policy.'
									: 'Snapshots appear after a policy runs.'}
								level={3}
								compact
							/>
						{/snippet}
					</Table>
				{/snippet}
			</QueryView>
		</Card>
	{/if}
</Page>
