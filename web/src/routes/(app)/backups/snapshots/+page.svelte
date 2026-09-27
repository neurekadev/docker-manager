<script lang="ts">
	// All backups (#10): every backup of a stack, volume or the manager
	// state in the instance (or the selected environment), whoever
	// configured the policy. Open one to browse its contents or restore it.
	import { createQuery } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Card,
		EmptyState,
		Table,
		TextField,
		formatBytes,
		formatDateTime,
		type Column
	} from '$lib/ui';
	import { environmentName } from '$lib/features/common/data';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import BackupsHeader from '$lib/features/backups/BackupsHeader.svelte';
	import {
		CONSISTENCY_LABEL,
		KIND_LABEL,
		itemName,
		type Backup
	} from '$lib/features/backups/model';
	import { backupPoliciesQuery, backupsQuery } from '$lib/features/backups/queries';

	usePage({
		title: 'All backups',
		crumbs: [{ label: 'Backups', href: routes.backups() }, { label: 'All backups' }],
		environmentScoped: true
	});

	const envs = createQuery(() => environmentsQuery());
	const policies = createQuery(() => backupPoliciesQuery());
	const backups = createQuery(() =>
		backupsQuery(environmentSelection.id ? { environmentId: environmentSelection.id } : {})
	);
	const envName = (id: string) => environmentName(envs.data, id);
	const policyName = (id?: string) => policies.data?.find((p) => p.id === id)?.name;

	let filter = $state('');
	const rows = $derived.by(() => {
		const q = filter.trim().toLowerCase();
		const all = backups.data ?? [];
		if (!q) return all;
		return all.filter((b) =>
			[
				itemName(b),
				policyName(b.policyId) ?? '',
				b.environmentId ? envName(b.environmentId) : ''
			]
				.join(' ')
				.toLowerCase()
				.includes(q)
		);
	});
	const totalBytes = $derived(rows.reduce((n, b) => n + (b.bytes ?? 0), 0));

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
			header: 'Taken',
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
			width: '160px'
		},
		{ id: 'consistency', header: 'Consistency', cell: consistencyCell, width: '220px' },
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			sortValue: (b) => b.bytes ?? null,
			numeric: true,
			width: '110px'
		}
	];
</script>

{#snippet itemCell(b: Backup)}
	<NameCell
		name={itemName(b)}
		href={routes.backup(b.id)}
		sub={[
			b.kind && b.kind !== 'manager_state' ? KIND_LABEL[b.kind] : undefined,
			policyName(b.policyId)
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
	<BackupsHeader />
	<Card
		title="All backups"
		subtitle={backups.data
			? `${rows.length} ${rows.length === 1 ? 'backup' : 'backups'}, ${formatBytes(totalBytes)} backed up`
			: undefined}
		padding="none"
	>
		{#snippet actions()}
			<div class="filter">
				<TextField
					label="Filter backups"
					hideLabel
					placeholder="Filter by name, policy or environment"
					bind:value={filter}
				/>
			</div>
		{/snippet}
		<QueryView query={backups} errorTitle="The backups could not be loaded.">
			<Table
				label="All backups"
				{rows}
				{columns}
				rowKey={(b) => b.id}
				sort={{ column: 'time', direction: 'desc' }}
			>
				{#snippet empty()}
					<EmptyState
						icon={Archive}
						color="slate"
						title={filter.trim() ? 'No backup matches the filter.' : 'No backups here.'}
						description={filter.trim()
							? 'Clear the filter to see every backup.'
							: environmentSelection.id
								? 'Nothing from this environment is backed up yet.'
								: 'Backups appear after a policy runs.'}
						level={3}
						compact
					/>
				{/snippet}
			</Table>
		</QueryView>
	</Card>
</Page>

<style>
	.filter {
		width: min(320px, 100%);
	}
</style>
