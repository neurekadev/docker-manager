<script lang="ts">
	// Backup policy detail (#10), laid out like every policy page: one
	// status sentence (what, where, when, how the last run went) with Back
	// up now, Edit and the overflow menu; KPIs (last run, next run,
	// coverage, retention in words); then what it covers, its schedule with
	// the next runs, and its recent runs with their sizes. Editing opens the
	// one-screen editor in a dialog (routes.backupPolicyEdit() links here
	// with it open); retention runs with its preview and confirmation.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Clock from '@lucide/svelte/icons/clock';
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Eraser from '@lucide/svelte/icons/eraser';
	import History from '@lucide/svelte/icons/rotate-ccw-clock';
	import Layers from '@lucide/svelte/icons/layers';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { api, unwrap, unwrapEmpty, type Job } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery, schedulePreviewQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		ConfirmDialog,
		DestructiveConfirm,
		EmptyState,
		IconButton,
		JobProgress,
		KpiCard,
		Menu,
		PageHeader,
		Skeleton,
		formatBytes,
		formatDateTime,
		formatRelative,
		toast,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import {
		environmentName,
		ifMatch,
		newIdempotencyKey,
		stacksQuery
	} from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Columns from '$lib/features/common/Columns.svelte';
	import Facts from '$lib/features/common/Facts.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import { jobKindLabel } from '$lib/features/jobs/labels';
	import BackupPolicyDialog from '$lib/features/backups/BackupPolicyDialog.svelte';
	import RetentionPreviewPanel from '$lib/features/backups/RetentionPreviewPanel.svelte';
	import SetsTable from '$lib/features/backups/SetsTable.svelte';
	import {
		coverageSummary,
		retentionActive,
		policySentence,
		retentionShort,
		retentionText,
		scheduleWords,
		setBytes,
		setState,
		type BackupPolicy
	} from '$lib/features/backups/model';
	import {
		backupActivityQuery,
		backupPolicyQuery,
		backupsQuery,
		repositoriesQuery
	} from '$lib/features/backups/queries';

	const id = $derived(page.params.policyId ?? '');
	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const policy = createQuery(() => backupPolicyQuery(id));
	const repos = createQuery(() => repositoriesQuery());
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	// The policy's backups: the sizes of its runs.
	const backups = createQuery(() => backupsQuery({ policyId: id }));
	const nextRuns = createQuery(() => ({
		...schedulePreviewQuery(
			policy.data?.schedule?.cron ?? '',
			policy.data?.schedule?.timeZone ?? '',
			'backup'
		),
		enabled: !!policy.data?.schedule?.enabled
	}));
	const envName = (e: string) => environmentName(envs.data, e);
	const editDialog = urlDialog('edit');

	usePage(() => ({
		title: policy.data?.name ?? 'Backup policy',
		crumbs: [
			{ label: 'Backups', href: routes.backups() },
			{ label: policy.data?.name ?? 'Policy' }
		]
	}));

	let running = $state(false);
	let jobs = $state<Job[]>([]);
	// A run of this policy that is queued or running, however it started
	// (schedule, another tab, the API): the button spins and waits for it
	// (the manager refuses a second run with backup_run_active).
	const activity = createQuery(() =>
		backupActivityQuery(() => policy.data?.recentSets?.[0]?.state === 'pending')
	);
	const active = $derived((activity.data ?? []).some((a) => a.policyId === id));
	let retentionOpen = $state(false);
	let deleteOpen = $state(false);

	async function run(p: BackupPolicy) {
		running = true;
		try {
			const out = await unwrap(
				api.POST('/api/v1/backup-policies/{policyId}/runs', {
					params: {
						path: { policyId: p.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: {}
				})
			);
			jobs = out.jobs;
			toast.info(`Started a backup of ${p.name}`, {
				body: `${out.jobs.length} ${out.jobs.length === 1 ? 'job' : 'jobs'}`
			});
		} catch (e) {
			toast.error(`${p.name} was not backed up`, {
				body: actionError(e, {
					recovery_key_not_confirmed:
						'Confirm the Recovery Key of the repositories this policy uses first.',
					backup_run_active:
						'A backup of this policy is already running. Wait for it to finish.'
				})
			});
		} finally {
			running = false;
		}
	}

	function finished(j: Job) {
		void qc.invalidateQueries({ queryKey: ['policies'] });
		void qc.invalidateQueries({ queryKey: ['backups'] });
		const where = j.environmentId ? envName(j.environmentId) : 'Docker Manager';
		if (j.state === 'succeeded') toast.success(`Backed up ${where}`);
		else if (j.state === 'partial')
			toast.warn(`Backed up ${where} partly`, { body: j.error?.recovery });
		else
			toast.error(`${where} was not backed up`, {
				body: j.error?.recovery ?? j.error?.message
			});
	}

	async function applyRetention(p: BackupPolicy) {
		try {
			const out = await unwrap(
				api.POST('/api/v1/backup-policies/{policyId}/retention-runs', {
					params: {
						path: { policyId: p.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { confirm: true }
				})
			);
			jobs = out.jobs;
			toast.info(`Applying retention of ${p.name}`);
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		}
	}

	async function remove(p: BackupPolicy) {
		await unwrapEmpty(
			api.DELETE('/api/v1/backup-policies/{policyId}', {
				params: { path: { policyId: p.id }, header: { 'If-Match': ifMatch(p.revision) } }
			})
		);
		toast.success(`Deleted backup policy ${p.name}`);
		await qc.invalidateQueries({ queryKey: ['policies'] });
		await goto(routes.backups());
	}

	function menuFor(p: BackupPolicy): MenuEntry[] {
		const items: MenuEntry[] = [];
		if (has(p, 'backup.retention') && retentionActive(p.retention))
			items.push({
				label: 'Apply retention now',
				icon: Eraser,
				onSelect: () => (retentionOpen = true)
			});
		if (has(p, 'backup_policy.manage')) {
			if (items.length) items.push({ separator: true });
			items.push({
				label: 'Delete policy',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleteOpen = true)
			});
		}
		return items;
	}

	function stackName(stackId: string) {
		const s = stacks.data?.find((x) => x.id === stackId);
		return s
			? `${s.displayName || s.name}${s.environmentId ? ` (${envName(s.environmentId)})` : ''}`
			: 'A stack you cannot see';
	}

	/** An excluded volume key (environmentID/name for all environments). */
	function volumeName(p: BackupPolicy, key: string) {
		if (p.scope !== 'all') return key;
		const [env, ...rest] = key.split('/');
		return `${rest.join('/')} (${envName(env)})`;
	}

	function jobTitle(j: Job): string {
		const where = j.kind.startsWith('manager')
			? 'Docker Manager'
			: envName(j.environmentId ?? '');
		return `${jobKindLabel(j.kind)}: ${where}`;
	}
</script>

<Page>
	<QueryView
		query={policy}
		errorTitle="The backup policy could not be loaded."
		notFoundTitle="This backup policy does not exist."
	>
		{#snippet children(p: BackupPolicy)}
			{@const menu = menuFor(p)}
			{@const repo = repos.data?.find((r) => r.id === p.repositoryId)}
			{@const last = p.recentSets?.[0]}
			{@const lastBytes = last ? setBytes(backups.data, last.id) : undefined}
			{@const coverage = coverageSummary(p, envName)}
			{@const explicit = p.stacks.length > 0 || p.volumes.length > 0}
			{@const manager = p.includeManagerState
				? p.includeMetrics
					? 'Yes, with metrics'
					: 'Yes, without metrics'
				: 'No'}
			{@const containers = p.shutdown ? 'Stopped, then started again' : 'Keep running'}
			<PageHeader
				title={p.name}
				{...resourceIcon('backupPolicy')}
				description={policySentence(p, {
					repository: repo?.name,
					environmentName: envName,
					running: running || active || jobs.length > 0
				})}
			>
				{#snippet actions()}
					{#if has(p, 'backup.run')}
						<Button
							variant="primary"
							icon={Play}
							loading={running || active}
							onclick={() => run(p)}>{active ? 'Backing up…' : 'Back up now'}</Button
						>
					{/if}
					{#if has(p, 'backup_policy.manage')}
						<Button icon={Pencil} onclick={() => (editDialog.open = true)}>Edit</Button>
					{/if}
					{#if menu.length}
						<Menu items={menu} label="More actions for {p.name}">
							{#snippet trigger(props)}
								<IconButton
									{...props}
									label="More actions"
									icon={Ellipsis}
									variant="secondary"
								/>
							{/snippet}
						</Menu>
					{/if}
				{/snippet}
			</PageHeader>

			{#each jobs as j (j.id)}
				<JobProgress jobId={j.id} title={jobTitle(j)} onfinish={finished} />
			{/each}

			<KpiRow>
				<KpiCard
					label="Last run"
					value={last ? formatRelative(last.finishedAt ?? last.startedAt) : 'Not run yet'}
					secondary={last
						? `${setState(last.state).label}${lastBytes !== undefined ? `, ${formatBytes(lastBytes)}` : ''}`
						: 'Back up now to start'}
					icon={DatabaseBackup}
					color="teal"
					tone={last
						? last.state === 'complete'
							? 'ok'
							: last.state === 'pending'
								? undefined
								: 'warn'
						: undefined}
				/>
				<KpiCard
					label="Next run"
					value={p.schedule?.enabled && p.schedule.nextRun
						? formatRelative(p.schedule.nextRun)
						: 'Not scheduled'}
					secondary={p.schedule?.enabled && p.schedule.nextRun
						? formatDateTime(p.schedule.nextRun)
						: 'Only when started'}
					icon={Clock}
					color="slate"
				/>
				<KpiCard
					label="Coverage"
					value={coverage.value}
					secondary={coverage.secondary}
					icon={Layers}
					color="blue"
				/>
				<KpiCard
					label="Retention"
					value={retentionShort(p.retention)}
					secondary={retentionActive(p.retention)
						? p.retention?.afterBackup
							? 'Applied after every backup'
							: 'Applied by hand'
						: 'Nothing is forgotten'}
					icon={History}
					color="violet"
				/>
			</KpiRow>

			<Columns ratio="equal">
				<Card title="What it covers">
					<Facts
						columns={1}
						items={[
							...(explicit
								? [
										{
											label: 'Stacks',
											value: p.stacks.length
												? p.stacks
														.map((s) => stackName(s.stackId))
														.join(', ')
												: 'None'
										},
										{
											label: 'Standalone volumes',
											value: p.volumes.length
												? p.volumes
														.map(
															(v) =>
																`${v.volume} (${envName(v.environmentId)})`
														)
														.join(', ')
												: 'None'
										},
										{
											label: 'Anonymous volumes',
											value: p.stacks.some((s) => s.anonymousVolumes)
												? 'Included for some stacks'
												: 'Not backed up'
										}
									]
								: [
										{
											label: 'Stacks',
											value: (p.excludeStacks ?? []).length
												? `Every managed stack except ${p.excludeStacks.map(stackName).join(', ')}`
												: 'Every managed stack, with its files and volumes'
										},
										{
											label: 'Volumes',
											value: (p.excludeVolumes ?? []).length
												? `Every volume except ${p.excludeVolumes.map((v) => volumeName(p, v)).join(', ')}`
												: 'Every stack and standalone volume'
										},
										{
											label: 'Anonymous volumes',
											value: p.anonymousVolumes
												? 'Backed up'
												: 'Not backed up'
										},
										{
											label: 'Buildx builder volumes',
											value: p.buildxVolumes ? 'Backed up' : 'Not backed up'
										}
									]),
							{ label: 'Manager state', value: manager },
							{ label: 'Containers during backups', value: containers },
							{
								label: 'Stored in',
								value: [
									repo?.name ?? '—',
									...Object.entries(p.environmentRepositories ?? {}).map(
										([env, r]) =>
											`${repos.data?.find((x) => x.id === r)?.name ?? '—'} for ${envName(env)}`
									)
								].join('; ')
							}
						]}
					/>
				</Card>
				<Card title="Schedule">
					{#if p.schedule?.enabled}
						<ScheduleSummary {...p.schedule} nextRun={undefined} />
						<h3 class="subsection-title next">Next runs</h3>
						{#if nextRuns.isPending}
							<Skeleton lines={3} height="16px" />
						{:else if nextRuns.data?.runs.length}
							<ol class="runs" role="list">
								{#each nextRuns.data.runs.slice(0, 3) as r (r.utc)}
									<li class="num">
										{formatDateTime(r.utc, p.schedule.timeZone)}
									</li>
								{/each}
							</ol>
						{:else}
							<p class="muted">The next runs could not be calculated.</p>
						{/if}
					{:else}
						<p>The schedule is off: backups run only when you start them.</p>
						{#if p.schedule?.cron}
							<p
								class="muted small"
								title="{p.schedule.cron} ({p.schedule.timeZone})"
							>
								Turned on, it would back up {scheduleWords(
									p.schedule.cron,
									p.schedule.timeZone
								)}.
							</p>
						{/if}
					{/if}
				</Card>
			</Columns>

			<Card title="Recent runs" padding="none">
				{#if p.recentSets?.length}
					<SetsTable
						sets={p.recentSets.map((s) => ({
							...s,
							policyId: p.id,
							policyName: p.name
						}))}
						label="Recent runs of {p.name}"
						showPolicy={false}
						environmentName={envName}
						backups={backups.data}
						canRetry={() => has(p, 'backup.run')}
					/>
				{:else}
					<EmptyState
						{...resourceIcon('backupPolicy')}
						title="Not run yet."
						description="Back up now to create the first backups, or turn the schedule on."
						level={3}
						compact
					/>
				{/if}
			</Card>

			{#if editDialog.open}
				<BackupPolicyDialog
					bind:open={editDialog.open}
					policy={p}
					owner={!!perms.data?.owner}
				/>
			{/if}
			<ConfirmDialog
				bind:open={retentionOpen}
				title="Apply the retention of {p.name}?"
				message="Forgets the backups the rules no longer keep, then frees their space in the repositories. Review the preview first."
				consequences={[
					retentionText(p.retention) + '.',
					'The minimum recovery floor and the newest backup of each stack and volume are always kept.',
					'Forgotten backups cannot be restored afterwards. Object Lock may refuse some deletions.'
				]}
				confirmLabel="Apply retention"
				tone="danger"
				size="lg"
				onconfirm={() => applyRetention(p)}
			>
				<RetentionPreviewPanel policyId={p.id} auto />
			</ConfirmDialog>
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete backup policy {p.name}"
				consequences={[
					'Scheduled backups of this policy stop.',
					'Its backups stay and can still be restored.',
					'Repositories and the Recovery Key are not touched.'
				]}
				confirmText={p.name}
				confirmLabel="Delete policy"
				onconfirm={() => remove(p)}
			/>
		{/snippet}
	</QueryView>
</Page>

<style>
	.next {
		margin-top: var(--space-4);
	}

	.runs {
		display: grid;
		gap: var(--space-1);
		margin: var(--space-2) 0 0;
		padding: 0;
		list-style: none;
	}

	.small {
		margin-top: var(--space-2);
		font-size: var(--text-caption);
	}
</style>
