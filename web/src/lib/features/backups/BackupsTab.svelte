<script lang="ts">
	// The Backups tab of a stack or volume (#10): which policy covers it and
	// when that runs next, then its backups, newest first, each restorable
	// whole or file by file. Choose Files opens the shared file picker
	// (several files and folders); every restore is previewed and confirmed with a danger button
	// that says what is replaced. Without backups yet, the covering
	// policy's recent runs say whether they included it and how that went.
	import { createQuery } from '@tanstack/svelte-query';
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import FolderSearch from '@lucide/svelte/icons/folder-search';
	import History from '@lucide/svelte/icons/history';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		formatBytes,
		formatDateTime,
		formatRelative,
		Table,
		type Column
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { stacksQuery } from '$lib/features/common/data';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { canAnywhere } from '$lib/features/stacks/model';
	import FilePicker from '$lib/features/common/FilePicker.svelte';
	import RestoreDialog from './RestoreDialog.svelte';
	import {
		CONSISTENCY_LABEL,
		memberReason,
		memberRuns,
		memberState,
		policyCovers,
		scheduleWords,
		type Backup,
		type CoverageTarget,
		type MemberRun
	} from './model';
	import { backupPlaces, backupSource, PICKER_LIMIT } from './picker';
	import { backupPoliciesQuery, backupsQuery, type BackupFilter } from './queries';
	import type { RestorePlan } from './restore';

	interface Props {
		filter: BackupFilter;
		/** The stack's or volume's name. */
		subject: string;
		/** A volume's page: only this volume of stack backups. */
		volume?: string;
		/** The user may deploy the stack (offers the redeploy). */
		canDeploy?: boolean;
	}

	let { filter, subject, volume, canDeploy = false }: Props = $props();

	const backups = createQuery(() => backupsQuery(filter));
	const perms = createQuery(() => myPermissionsQuery());
	const policies = createQuery(() => backupPoliciesQuery());
	const stacks = createQuery(() => ({ ...stacksQuery(), enabled: !!filter.stackId }));
	const canCreatePolicy = $derived(canAnywhere(perms.data, 'backup_policy.manage'));
	const rows = $derived(
		[...(backups.data ?? [])]
			.filter((b) => b.kind !== 'manager_state')
			.sort((a, b) => b.snapshotTime.localeCompare(a.snapshotTime))
	);

	// What is asked about: the stack (its environment from the stack list)
	// or the volume of one environment.
	const target = $derived.by((): CoverageTarget | null => {
		if (filter.stackId) {
			const env = stacks.data?.find((s) => s.id === filter.stackId)?.environmentId;
			return env ? { environmentId: env, stackId: filter.stackId } : null;
		}
		if (filter.volume && filter.environmentId)
			return { environmentId: filter.environmentId, volume: filter.volume };
		return null;
	});
	const covering = $derived(
		target ? (policies.data ?? []).filter((p) => policyCovers(p, target)) : []
	);
	const runs = $derived(target ? memberRuns(covering, target).slice(0, 5) : []);

	let picking = $state<Backup | null>(null);
	let pickerOpen = $state(false);
	let restoring = $state<{ backup: Backup; plan: RestorePlan } | null>(null);
	let restoreOpen = $state(false);

	function browse(b: Backup) {
		picking = b;
		pickerOpen = true;
	}

	function restoreAll(b: Backup) {
		restoring = { backup: b, plan: { kind: 'full' } };
		restoreOpen = true;
	}

	const canRestore = (b: Backup) => has(b, 'backup.restore');
	const canBrowse = (b: Backup) => canRestore(b) && has(b, 'backup.contents.read');

	const columns: Column<Backup>[] = [
		{
			id: 'time',
			header: 'Backup',
			cell: timeCell,
			sortValue: (b) => b.snapshotTime,
			stack: 'title'
		},
		{
			id: 'consistency',
			header: 'Consistency',
			cell: consistencyCell,
			width: '190px',
			stack: 'hidden'
		},
		{ id: 'state', header: 'State', cell: stateCell, width: '120px', stack: 'status' },
		{
			id: 'size',
			header: 'Size',
			cell: sizeCell,
			numeric: true,
			width: '100px',
			sortValue: (b) => b.bytes ?? 0,
			stack: 'meta'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '300px',
			pin: 'end',
			stack: 'actions'
		}
	];

	const runColumns: Column<MemberRun>[] = [
		{
			id: 'when',
			header: 'Run',
			cell: runWhenCell,
			sortValue: (r) => r.startedAt,
			stack: 'title'
		},
		{ id: 'state', header: 'Result', cell: runStateCell, width: '200px', stack: 'status' },
		{
			id: 'job',
			header: 'Job',
			hideHeader: true,
			cell: runJobCell,
			width: '90px',
			stack: 'actions'
		}
	];
</script>

{#snippet timeCell(b: Backup)}
	<a class="when" href={routes.backup(b.id)}>
		<span>{formatDateTime(b.snapshotTime)}</span>
		<span class="ago">{formatRelative(b.snapshotTime)}</span>
	</a>
{/snippet}
{#snippet consistencyCell(b: Backup)}{b.consistency
		? CONSISTENCY_LABEL[b.consistency]
		: '—'}{/snippet}
{#snippet stateCell(b: Backup)}
	{#if b.state === 'complete'}<Badge tone="ok" dot>Complete</Badge>{:else}<Badge tone="warn" dot
			>Partial</Badge
		>{/if}
{/snippet}
{#snippet sizeCell(b: Backup)}<span class="num">{b.bytes ? formatBytes(b.bytes) : '—'}</span
	>{/snippet}
{#snippet actionsCell(b: Backup)}
	<span class="acts">
		{#if canBrowse(b)}
			<Button size="sm" icon={FolderSearch} onclick={() => browse(b)}>Choose Files</Button>
		{/if}
		{#if canRestore(b)}
			<Button size="sm" variant="danger-soft" icon={History} onclick={() => restoreAll(b)}
				>Restore All</Button
			>
		{/if}
	</span>
{/snippet}
{#snippet runWhenCell(r: MemberRun)}
	<span class="when">
		<span class="num" title={formatDateTime(r.startedAt)}>{formatRelative(r.startedAt)}</span>
		<a class="ago" href={routes.backupPolicy(r.policyId)}>{r.policyName}</a>
	</span>
{/snippet}
{#snippet runStateCell(r: MemberRun)}
	{@const s = memberState(r.member.state)}
	{@const why = memberReason(r.member)}
	<span class="result">
		<Badge tone={s.tone} dot>{s.label}</Badge>
		{#if why}<span class="ago">{why.text}</span>{/if}
	</span>
{/snippet}
{#snippet runJobCell(r: MemberRun)}
	{#if r.member.jobId}<a href={routes.job(r.member.jobId)}>Open Job</a>{/if}
{/snippet}

<Card title="Backups" padding="none">
	{#if covering.length}
		<ul class="coverage" role="list" aria-label="Backup Policies Covering {subject}">
			{#each covering as p (p.id)}
				<li>
					<DatabaseBackup size={16} aria-hidden="true" />
					<span>
						Covered by <a href={routes.backupPolicy(p.id)}>{p.name}</a
						>{#if p.schedule?.enabled}, {scheduleWords(
								p.schedule.cron,
								p.schedule.timeZone
							)}{#if p.schedule.nextRun}; next run <span
									class="num"
									title={formatDateTime(p.schedule.nextRun)}
									>{formatRelative(p.schedule.nextRun)}</span
								>{/if}.{:else}; it runs only when started.{/if}
					</span>
				</li>
			{/each}
		</ul>
	{/if}
	<QueryView query={backups} errorTitle="The backups could not be loaded.">
		{#if rows.length === 0}
			{#if covering.length}
				<EmptyState
					icon={DatabaseBackup}
					color="teal"
					level={3}
					title="No Backups of {subject} Yet"
					description={runs.length
						? 'The recent runs below say how backing it up went.'
						: 'The first backup appears after the next run of the policy.'}
					compact
				/>
				{#if runs.length}
					<Table
						label="Recent Runs That Included {subject}"
						rows={runs}
						columns={runColumns}
						rowKey={(r) => `${r.setId}-${r.member.item}`}
						sort={{ column: 'when', direction: 'desc' }}
					/>
				{/if}
			{:else}
				<EmptyState
					icon={DatabaseBackup}
					color="teal"
					level={3}
					title="No Backups of {subject} Yet"
					description="No backup policy covers {subject}. Create one to back it up."
				>
					{#snippet actions()}
						{#if canCreatePolicy}<Button
								href={routes.backupPolicyNew()}
								variant="primary">Create Backup Policy</Button
							>{/if}
					{/snippet}
				</EmptyState>
			{/if}
		{:else}
			<Table
				label="Backups of {subject}"
				{rows}
				{columns}
				rowKey={(b) => b.id}
				sort={{ column: 'time', direction: 'desc' }}
			/>
		{/if}
	</QueryView>
</Card>

{#if picking}
	<FilePicker
		bind:open={pickerOpen}
		multiple
		title="Choose What to Restore"
		description="Backup of {formatDateTime(
			picking.snapshotTime
		)}. A ticked folder is made identical to the backup."
		places={backupPlaces(picking, volume)}
		source={backupSource(picking.id)}
		value={restoring?.backup.id === picking.id && restoring.plan.kind === 'paths'
			? restoring.plan.paths
			: []}
		confirmLabel="Review Restore"
		truncatedHint="Only the first {PICKER_LIMIT} entries are listed. Tick the folder to restore all of it."
		onpick={(paths) => {
			if (!picking) return;
			restoring = { backup: picking, plan: { kind: 'paths', paths } };
			restoreOpen = true;
		}}
	/>
{/if}
{#if restoring}
	<RestoreDialog
		bind:open={restoreOpen}
		backup={restoring.backup}
		plan={restoring.plan}
		{subject}
		{volume}
		{canDeploy}
	/>
{/if}

<style>
	.coverage {
		display: grid;
		gap: var(--space-1);
		margin: 0;
		padding: var(--space-3) var(--space-4);
		border-bottom: 1px solid var(--border-subtle);
		list-style: none;
	}

	.coverage li {
		display: flex;
		align-items: flex-start;
		gap: var(--space-2);
		color: var(--text-default);
	}

	.coverage li :global(svg) {
		flex: none;
		margin-top: 2px;
		color: var(--ok);
	}

	.when {
		display: grid;
		color: var(--text-strong);
	}

	.ago {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.result {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-1) var(--space-2);
	}

	.acts {
		display: inline-flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: var(--space-2);
	}
</style>
