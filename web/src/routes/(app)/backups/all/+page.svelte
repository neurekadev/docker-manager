<script lang="ts">
	// Backups (#10): every backup of a stack, volume or the manager state in
	// the instance (or the selected environment), whoever configured the
	// policy, one row per run: the policy, when it ran, how it went, the
	// backups it took (each opens its page) and Restore. Backups without a
	// run stand alone.
	import { createQuery } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import History from '@lucide/svelte/icons/history';
	import { SvelteSet } from 'svelte/reactivity';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		Menu,
		Table,
		TextField,
		formatBytes,
		formatDateTime,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { environmentName } from '$lib/features/common/data';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import BackupsHeader from '$lib/features/backups/BackupsHeader.svelte';
	import {
		KIND_LABEL,
		groupBackupsByRun,
		itemName,
		type Backup,
		type BackupRun
	} from '$lib/features/backups/model';
	import { backupPoliciesQuery, backupsQuery } from '$lib/features/backups/queries';

	usePage({
		title: 'Backups',
		crumbs: [{ label: 'Backups', href: routes.backups() }, { label: 'All backups' }],
		environmentScoped: true
	});

	/** Backups listed per run before "Show all". */
	const SHOWN = 3;

	const envs = createQuery(() => environmentsQuery());
	const single = singleEnvironment();
	const policies = createQuery(() => backupPoliciesQuery());
	const backups = createQuery(() =>
		backupsQuery(environmentSelection.id ? { environmentId: environmentSelection.id } : {})
	);
	const envName = (id: string) => environmentName(envs.data, id);
	const policyName = (id?: string) => policies.data?.find((p) => p.id === id)?.name;
	const where = (b: Backup) =>
		b.kind === 'manager_state' || !b.environmentId ? 'Manager' : envName(b.environmentId);

	let filter = $state('');
	const expanded = new SvelteSet<string>();
	const matching = $derived.by(() => {
		const q = filter.trim().toLowerCase();
		const all = backups.data ?? [];
		if (!q) return all;
		return all.filter((b) =>
			[itemName(b), policyName(b.policyId) ?? '', where(b)]
				.join(' ')
				.toLowerCase()
				.includes(q)
		);
	});
	const runs = $derived(groupBackupsByRun(matching));

	function runName(r: BackupRun): string {
		if (r.setId) return policyName(r.policyId) ?? 'Backup run';
		const b = r.backups[0];
		return b ? itemName(b) : 'Backup';
	}

	function restoreMenu(r: BackupRun): MenuEntry[] {
		return r.backups
			.filter((b) => has(b, 'backup.restore'))
			.map((b) => ({ label: `Restore ${itemName(b)}`, href: routes.backupRestore(b.id) }));
	}

	const columns = $derived<Column<BackupRun>[]>([
		{
			id: 'run',
			header: 'Run',
			cell: runCell,
			sortValue: (r) => r.time,
			maxWidth: '280px',
			stack: 'title'
		},
		{ id: 'state', header: 'State', cell: stateCell, width: '120px', stack: 'status' },
		{ id: 'backups', header: 'Backups', cell: backupsCell, stack: 'meta' },
		...(single.current
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						width: '160px',
						stack: 'hidden' as const
					}
				]),
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			sortValue: (r) => r.bytes ?? -1,
			numeric: true,
			width: '100px',
			title: () => 'The size of the data the run backed up',
			stack: 'hidden'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '130px',
			pin: 'end',
			stack: 'actions'
		}
	]);
</script>

{#snippet runCell(r: BackupRun)}
	<NameCell
		name={runName(r)}
		href={r.setId && r.policyId && policyName(r.policyId)
			? routes.backupPolicy(r.policyId)
			: r.backups.length === 1
				? routes.backup(r.backups[0].id)
				: undefined}
		sub={formatDateTime(r.time)}
	/>
{/snippet}
{#snippet stateCell(r: BackupRun)}
	{#if r.state === 'complete'}<Badge tone="ok" dot>Complete</Badge>{:else}<Badge tone="warn" dot
			>Partial</Badge
		>{/if}
{/snippet}
{#snippet backupsCell(r: BackupRun)}
	{@const open = expanded.has(r.key)}
	{@const list = open ? r.backups : r.backups.slice(0, SHOWN)}
	<ul class="items" role="list">
		{#each list as b (b.id)}
			<li>
				<a href={routes.backup(b.id)}>{itemName(b)}</a>
				<span class="muted"
					>{b.kind && b.kind !== 'manager_state' ? KIND_LABEL[b.kind] : ''}{b.state !==
					'complete'
						? `${b.kind && b.kind !== 'manager_state' ? ', ' : ''}some files unreadable`
						: ''}</span
				>
			</li>
		{/each}
	</ul>
	{#if r.backups.length > SHOWN}
		<button
			type="button"
			class="more"
			aria-expanded={open}
			onclick={() => (open ? expanded.delete(r.key) : expanded.add(r.key))}
		>
			{open ? 'Show fewer' : `Show all ${r.backups.length}`}
			<ChevronDown size={14} aria-hidden="true" class={open ? 'up' : ''} />
		</button>
	{/if}
{/snippet}
{#snippet envCell(r: BackupRun)}
	<span class="muted">{[...new Set(r.backups.map(where))].join(', ')}</span>
{/snippet}
{#snippet sizeCell(r: BackupRun)}<span class="num">{formatBytes(r.bytes)}</span>{/snippet}
{#snippet actionsCell(r: BackupRun)}
	{@const items = restoreMenu(r)}
	<div class="row-actions">
		{#if items.length === 1 && r.backups.length === 1}
			<Button
				size="sm"
				variant="secondary"
				icon={History}
				href={routes.backupRestore(r.backups[0].id)}>Restore</Button
			>
		{:else if items.length}
			<Menu {items} label="Restore from the run of {formatDateTime(r.time)}" align="end">
				{#snippet trigger(props)}
					<Button
						{...props}
						size="sm"
						variant="secondary"
						icon={History}
						iconEnd={ChevronDown}>Restore</Button
					>
				{/snippet}
			</Menu>
		{/if}
	</div>
{/snippet}

<Page>
	<BackupsHeader />
	<Card
		title="All backups"
		subtitle={backups.data
			? `${matching.length} ${matching.length === 1 ? 'backup' : 'backups'} from ${runs.length} ${runs.length === 1 ? 'run' : 'runs'}; restore one from its run or its page.`
			: undefined}
		padding="none"
		stretchActions
	>
		{#snippet actions()}
			<div class="filter">
				<TextField
					label="Search backups"
					hideLabel
					placeholder="Search backups"
					bind:value={filter}
				/>
			</div>
		{/snippet}
		<QueryView query={backups} errorTitle="The backups could not be loaded.">
			<Table
				label="All backups by run"
				rows={runs}
				{columns}
				rowKey={(r) => r.key}
				sort={{ column: 'run', direction: 'desc' }}
			>
				{#snippet empty()}
					<EmptyState
						icon={Archive}
						color="slate"
						title={filter.trim() ? 'No backup matches the search.' : 'No backups here.'}
						description={filter.trim()
							? 'Clear the search to see every backup.'
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
		margin-left: auto;
	}

	.items {
		display: grid;
		gap: 2px;
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.items li {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-1) var(--space-2);
		min-width: 0;
	}

	.items .muted {
		font-size: var(--text-caption);
	}

	.more {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		margin-top: var(--space-1);
		padding: 0;
		border: 0;
		background: transparent;
		color: var(--accent-text);
		font-size: var(--text-caption);
		cursor: pointer;
	}

	.more :global(.up) {
		transform: rotate(180deg);
	}

	.row-actions {
		display: flex;
		justify-content: flex-end;
	}
</style>
