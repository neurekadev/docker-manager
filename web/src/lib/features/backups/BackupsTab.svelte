<script lang="ts">
	// The Backups tab of a stack or volume (#10, #246): whether the backups
	// cover it and when they run next, then its backups (a copy per
	// repository), newest first, each restorable whole or file by file.
	// Browsing opens the file picker (a lazily listed tree); every restore
	// is previewed and confirmed with a danger button that says what is
	// replaced. Without backups yet, the recent runs say whether they
	// included it and how that went.
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
	import FilePickerDialog from './FilePickerDialog.svelte';
	import RestoreDialog from './RestoreDialog.svelte';
	import {
		CONSISTENCY_LABEL,
		memberReason,
		memberRuns,
		memberState,
		scheduleWords,
		settingsCover,
		type Backup,
		type CoverageTarget,
		type MemberRun
	} from './model';
	import {
		backupSettingsQuery,
		backupsQuery,
		repositoriesQuery,
		type BackupFilter
	} from './queries';
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
	// Callers who may not read the backup settings see the backups without
	// a coverage line.
	const settings = createQuery(() => ({ ...backupSettingsQuery(), retry: false }));
	const repos = createQuery(() => repositoriesQuery());
	const repoName = (id: string) =>
		repos.data?.find((r) => r.id === id)?.name ?? 'a removed repository';
	const stacks = createQuery(() => ({ ...stacksQuery(), enabled: !!filter.stackId }));
	const canEditBackups = $derived(canAnywhere(perms.data, 'backup_policy.manage'));
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
	const st = $derived(settings.data);
	// Covered: backups have a Primary repository and leave neither it nor its environment out.
	const covered = $derived(!!st?.primaryRepositoryId && !!target && settingsCover(st, target));
	const runs = $derived(st && target && covered ? memberRuns(st, target).slice(0, 5) : []);

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
			id: 'repository',
			header: 'Stored In',
			cell: repositoryCell,
			sortValue: (b) => repoName(b.repositoryId),
			width: '160px',
			stack: 'meta'
		},
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
{#snippet repositoryCell(b: Backup)}<span class="muted">{repoName(b.repositoryId)}</span>{/snippet}
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
		<span class="ago">in {repoName(r.member.repositoryId)}</span>
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
	{#if st && covered}
		<ul class="coverage" role="list" aria-label="Backups Covering {subject}">
			<li>
				<DatabaseBackup size={16} aria-hidden="true" />
				<span>
					Covered by <a href={routes.backups()}>Backups</a>{#if st.enabled}, {scheduleWords(
							st.schedule.cron,
							st.schedule.timeZone
						)}{#if st.schedule.nextRun}; next run <span
								class="num"
								title={formatDateTime(st.schedule.nextRun)}
								>{formatRelative(st.schedule.nextRun)}</span
							>{/if}.{:else}; they run only when started.{/if}
				</span>
			</li>
		</ul>
	{/if}
	<QueryView query={backups} errorTitle="The backups could not be loaded.">
		{#if rows.length === 0}
			{#if covered}
				<EmptyState
					icon={DatabaseBackup}
					color="teal"
					level={3}
					title="No Backups of {subject} Yet"
					description={runs.length
						? 'The recent runs below say how backing it up went.'
						: 'The first backup appears after the next backup run.'}
					compact
				/>
				{#if runs.length}
					<Table
						label="Recent Runs That Included {subject}"
						rows={runs}
						columns={runColumns}
						rowKey={(r) => `${r.setId}-${r.member.repositoryId}-${r.member.item}`}
						sort={{ column: 'when', direction: 'desc' }}
					/>
				{/if}
			{:else}
				<EmptyState
					icon={DatabaseBackup}
					color="teal"
					level={3}
					title="No Backups of {subject} Yet"
					description={st
						? `Backups leave ${subject} out, or have no Primary repository yet.`
						: undefined}
				>
					{#snippet actions()}
						{#if canEditBackups}<Button href={routes.backupsEdit()} variant="primary"
								>Edit Backups</Button
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
	<FilePickerDialog
		bind:open={pickerOpen}
		backup={picking}
		{volume}
		onnext={(paths) => {
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
