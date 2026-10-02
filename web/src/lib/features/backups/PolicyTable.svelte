<script lang="ts">
	// Backup policies as one row each (#10): what and where, the last run,
	// the schedule in words with the next run, and retention in words. The
	// name opens the policy page; Back up now and an overflow menu (Edit)
	// sit at the end of the row.
	import { useQueryClient } from '@tanstack/svelte-query';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		IconButton,
		Menu,
		Table,
		formatDateTime,
		formatRelative,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { runPolicy } from './actions';
	import {
		retentionShort,
		scopeText,
		setState,
		type BackupPolicy,
		type BackupRepository
	} from './model';

	interface Props {
		policies: BackupPolicy[];
		repositories: BackupRepository[] | undefined;
		environmentName: (id: string) => string;
		/** Policy IDs with a backup running now. */
		running?: ReadonlySet<string>;
	}

	let { policies, repositories, environmentName, running = new Set() }: Props = $props();
	const qc = useQueryClient();
	let starting = $state<string | null>(null);

	async function run(p: BackupPolicy) {
		starting = p.id;
		await runPolicy(qc, p);
		starting = null;
	}

	function where(p: BackupPolicy): string {
		const repo = repositories?.find((r) => r.id === p.repositoryId)?.name;
		const excluded = (p.excludeStacks ?? []).length + (p.excludeVolumes ?? []).length;
		return [
			`${scopeText(p, environmentName)}${repo ? ` to ${repo}` : ''}`,
			excluded ? `${excluded} left out` : undefined
		]
			.filter(Boolean)
			.join(' · ');
	}

	function menu(p: BackupPolicy): MenuEntry[] {
		const items: MenuEntry[] = [
			{ label: 'Open Policy', icon: ExternalLink, href: routes.backupPolicy(p.id) }
		];
		if (has(p, 'backup_policy.manage'))
			items.push({ label: 'Edit Policy', icon: Pencil, href: routes.backupPolicyEdit(p.id) });
		return items;
	}

	const columns: Column<BackupPolicy>[] = [
		{
			id: 'name',
			header: 'Policy',
			cell: nameCell,
			sortValue: (p) => p.name,
			maxWidth: '360px',
			stack: 'title'
		},
		{ id: 'last', header: 'Last Run', cell: lastCell, width: '170px', stack: 'status' },
		{ id: 'schedule', header: 'Schedule', cell: scheduleCell, width: '220px', stack: 'meta' },
		{
			id: 'retention',
			header: 'Retention',
			cell: retentionCell,
			width: '200px',
			stack: 'hidden'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '170px',
			pin: 'end',
			stack: 'actions'
		}
	];
</script>

{#snippet nameCell(p: BackupPolicy)}
	<NameCell icon="backupPolicy" name={p.name} href={routes.backupPolicy(p.id)} sub={where(p)} />
{/snippet}
{#snippet lastCell(p: BackupPolicy)}
	{@const s = p.recentSets?.[0]}
	{#if running.has(p.id)}
		<Badge tone="info" dot>Running</Badge>
	{:else if s}
		{@const st = setState(s.state)}
		<span class="cell">
			<Badge tone={st.tone} dot>{st.label}</Badge>
			<span class="muted num" title={formatDateTime(s.startedAt)}
				>{formatRelative(s.startedAt)}</span
			>
		</span>
	{:else}<span class="muted">Not run yet</span>{/if}
{/snippet}
{#snippet scheduleCell(p: BackupPolicy)}
	{#if p.schedule}
		<ScheduleSummary compact {...p.schedule} nextRun={p.schedule.nextRun} />
	{:else}<span class="muted">Only when started</span>{/if}
{/snippet}
{#snippet retentionCell(p: BackupPolicy)}<span class="muted">{retentionShort(p.retention)}</span
	>{/snippet}
{#snippet actionsCell(p: BackupPolicy)}
	<div class="row-actions">
		{#if has(p, 'backup.run')}
			<Button
				size="sm"
				variant="secondary"
				icon={Play}
				loading={starting === p.id || running.has(p.id)}
				onclick={() => run(p)}>{running.has(p.id) ? 'Backing Up…' : 'Back Up Now'}</Button
			>
		{/if}
		<Menu items={menu(p)} label="Actions for {p.name}" align="end">
			{#snippet trigger(props)}
				<IconButton {...props} icon={Ellipsis} label="Actions for {p.name}" size="sm" />
			{/snippet}
		</Menu>
	</div>
{/snippet}

<Table
	label="Backup Policies"
	rows={policies}
	{columns}
	rowKey={(p) => p.id}
	sort={{ column: 'name', direction: 'asc' }}
/>

<style>
	.cell {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.row-actions {
		display: flex;
		justify-content: flex-end;
		align-items: center;
		gap: var(--space-1);
	}
</style>
