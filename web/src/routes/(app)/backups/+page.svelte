<script lang="ts">
	// Backups overview (#10, #246): until backups can run, the setup steps
	// (a repository with a confirmed Recovery Key, then the settings);
	// afterwards one status sentence with Back Up Now, Edit and Apply
	// Retention, how backups stand (KPIs), what runs now (progress and the
	// file being read), the settings (where to, what, when, how long), the
	// recent runs (Details opens a drawer), and the storage the
	// repositories use and how it grew. Editing opens BackupSettingsDialog
	// (routes.backupsEdit() opens it). Every backup and the repositories
	// have their own tabs.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import Check from '@lucide/svelte/icons/check';
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Eraser from '@lucide/svelte/icons/eraser';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		ConfirmDialog,
		EmptyState,
		IconButton,
		KpiCard,
		Menu,
		Notice,
		Skeleton,
		formatBytes,
		formatDateTime,
		formatRelative,
		toast,
		type MenuEntry
	} from '$lib/ui';
	import { can, has } from '$lib/features/common/access';
	import { environmentName, newIdempotencyKey, stacksQuery } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import { jobKindLabel } from '$lib/features/jobs/labels';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import { BACK_UP_ERRORS } from '$lib/features/backups/actions';
	import BackupSettingsDialog from '$lib/features/backups/BackupSettingsDialog.svelte';
	import BackupsHeader from '$lib/features/backups/BackupsHeader.svelte';
	import { onJobsFinished } from '$lib/features/backups/finished.svelte';
	import RetentionPreviewPanel from '$lib/features/backups/RetentionPreviewPanel.svelte';
	import RunningBackups from '$lib/features/backups/RunningBackups.svelte';
	import SetsTable from '$lib/features/backups/SetsTable.svelte';
	import StorageCard from '$lib/features/backups/StorageCard.svelte';
	import StorageHistoryCard from '$lib/features/backups/StorageHistoryCard.svelte';
	import {
		coverageSummary,
		isRetentionActivity,
		retentionActive,
		retentionShort,
		retentionText,
		scheduleWords,
		setBytes,
		settingsSentence,
		storageTotals,
		type BackupSettings
	} from '$lib/features/backups/model';
	import {
		backupActivityQuery,
		backupSettingsQuery,
		backupsQuery,
		repositoriesQuery
	} from '$lib/features/backups/queries';

	usePage({ title: 'Backups', crumbs: [{ label: 'Backups' }], environmentScoped: true });

	/** Recent runs listed here; the Backups tab has every backup. */
	const RECENT_RUNS = 8;

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const settings = createQuery(() => backupSettingsQuery());
	const stacks = createQuery(() => stacksQuery());
	const backups = createQuery(() =>
		backupsQuery(environmentSelection.id ? { environmentId: environmentSelection.id } : {})
	);
	const envName = (id: string) => environmentName(envs.data, id);
	const repoName = (id: string) =>
		repos.data?.find((r) => r.id === id)?.name ?? 'A removed repository';
	const editDialog = urlDialog('edit');

	const canAddRepository = $derived(access.owner || can(access, 'backup_repository.manage'));
	const readyRepos = $derived((repos.data ?? []).filter((r) => r.state === 'ready'));
	const awaiting = $derived(
		(repos.data ?? []).filter((r) => r.state === 'awaiting_confirmation')
	);
	const st = $derived(settings.data);
	const primary = $derived(repos.data?.find((r) => r.id === st?.primaryRepositoryId));
	const secondary = $derived(repos.data?.find((r) => r.id === st?.secondaryRepositoryId));
	const sets = $derived(
		(st?.recentSets ?? []).filter(
			(s) =>
				!environmentSelection.id ||
				s.members.some((m) => m.environmentId === environmentSelection.id)
		)
	);
	const lastComplete = $derived(sets.find((s) => s.state === 'complete'));
	const lastBytes = $derived(lastComplete ? setBytes(backups.data, lastComplete.id) : undefined);
	const troubled = $derived(
		sets.filter((s) => s.state === 'partial' || s.state === 'failed').length
	);
	const repoCount = $derived(repos.data?.length ?? 0);
	const setupDone = $derived(readyRepos.length > 0 && !!st?.primaryRepositoryId);

	// Running backups: polled every second while one runs or a set is
	// still pending; when a job ends, the sets, backups and storage refresh.
	const tracked = useTrackedJobs(() => (st ? { policyId: st.id } : null));
	const activity = createQuery(() =>
		backupActivityQuery(() => sets.some((s) => s.state === 'pending') || tracked.busy)
	);
	const running = $derived(
		(activity.data ?? []).filter(
			(a) =>
				!environmentSelection.id ||
				!a.environmentId ||
				a.environmentId === environmentSelection.id
		)
	);
	const backingUp = $derived(
		running.some((a) => !isRetentionActivity(a)) ||
			!!tracked.runningOf('backup.run', 'manager.backup')
	);
	onJobsFinished(() => (activity.data ?? []).map((a) => a.jobId), finished);
	const storage = $derived(storageTotals(repos.data ?? [], environmentSelection.id));

	let starting = $state(false);
	let retentionOpen = $state(false);
	let retentionState = $state({ ready: false, forget: 0 });

	async function backUp() {
		starting = true;
		try {
			const out = await unwrap(
				api.POST('/api/v1/backup-settings/runs', {
					params: { header: { 'Idempotency-Key': newIdempotencyKey() } },
					body: {}
				})
			);
			for (const j of out.jobs) tracked.add(j, jobTitle(j));
			toast.info('Started a backup', {
				body: `${out.jobs.length} ${out.jobs.length === 1 ? 'job' : 'jobs'}`
			});
		} catch (e) {
			toast.error('Nothing was backed up', { body: actionError(e, BACK_UP_ERRORS) });
		} finally {
			starting = false;
		}
	}

	async function applyRetention() {
		try {
			const out = await unwrap(
				api.POST('/api/v1/backup-settings/retention-runs', {
					params: { header: { 'Idempotency-Key': newIdempotencyKey() } },
					body: { confirm: true }
				})
			);
			for (const j of out.jobs) tracked.add(j, jobTitle(j));
			toast.info('Applying the retention');
		} catch (e) {
			throw new Error(actionError(e), { cause: e });
		}
	}

	function finished(j: Job) {
		void qc.invalidateQueries({ queryKey: ['policies'] });
		void qc.invalidateQueries({ queryKey: ['backups'] });
		const where = j.environmentId ? envName(j.environmentId) : 'Docker Manager';
		if (j.kind === 'backup.retention' || j.kind === 'manager.retention') {
			if (j.state === 'succeeded') toast.success(`Applied the retention on ${where}`);
			else if (j.state === 'partial')
				toast.warn(`Applied the retention on ${where} partly`, { body: j.error?.recovery });
			else
				toast.error(`The retention was not applied on ${where}`, {
					body: j.error?.recovery ?? j.error?.message
				});
			return;
		}
		if (j.state === 'succeeded') toast.success(`Backed up ${where}`);
		else if (j.state === 'partial')
			toast.warn(`Backed up ${where} partly`, { body: j.error?.recovery });
		else
			toast.error(`${where} was not backed up`, {
				body: j.error?.recovery ?? j.error?.message
			});
	}

	function jobTitle(j: Job): string {
		const where = j.kind.startsWith('manager')
			? 'Docker Manager'
			: envName(j.environmentId ?? '');
		return `${jobKindLabel(j.kind)}: ${where}`;
	}

	function menuFor(s: BackupSettings): MenuEntry[] {
		return has(s, 'backup.retention') && retentionActive(s.retention)
			? [
					{
						label: 'Apply Retention Now',
						icon: Eraser,
						onSelect: () => (retentionOpen = true)
					}
				]
			: [];
	}

	function stackName(stackId: string) {
		const s = stacks.data?.find((x) => x.id === stackId);
		return s
			? `${s.displayName || s.name}${s.environmentId ? ` (${envName(s.environmentId)})` : ''}`
			: 'A stack you cannot see';
	}

	function volumeName(key: string) {
		const [env, ...rest] = key.split('/');
		return `${rest.join('/')} (${envName(env)})`;
	}
</script>

<Page>
	<BackupsHeader>
		{#snippet actions()}
			{#if st && setupDone}
				{@const menu = menuFor(st)}
				{#if has(st, 'backup.run')}
					<Button
						variant="primary"
						icon={Play}
						loading={starting || backingUp}
						onclick={backUp}>{backingUp ? 'Backing Up…' : 'Back Up Now'}</Button
					>
				{/if}
				{#if has(st, 'backup_policy.manage')}
					<Button icon={Pencil} onclick={() => (editDialog.open = true)}>Edit</Button>
				{/if}
				{#if menu.length}
					<Menu items={menu} label="More Backup Actions">
						{#snippet trigger(props)}
							<IconButton
								{...props}
								label="More Actions"
								icon={Ellipsis}
								variant="secondary"
							/>
						{/snippet}
					</Menu>
				{/if}
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
				<Button size="sm" href={routes.backupRepository(r.id)}>Confirm the Key</Button>
			{/snippet}
		</Notice>
	{/each}

	{#if st?.enabled && !st.primaryRepositoryId}
		<Notice tone="danger" icon={TriangleAlert} title="Backups Are Paused" live="polite">
			No repository is the Primary one, so nothing is backed up. Make a repository the Primary
			one.
			{#snippet actions()}
				<Button size="sm" href={routes.backupRepositories()}>Open Repositories</Button>
			{/snippet}
		</Notice>
	{/if}

	{#if repos.isPending || settings.isPending}
		<Skeleton lines={4} height="72px" />
	{:else if !setupDone}
		<Card title="Set Up Backups">
			<ol class="setup" role="list">
				<li class:done={readyRepos.length > 0}>
					<span class="marker" aria-hidden="true"
						>{#if readyRepos.length > 0}<Check
								size={14}
								strokeWidth={2}
							/>{:else}1{/if}</span
					>
					<div class="step">
						<h3>Add a Backup Repository</h3>
						<p class="muted">
							An S3-compatible bucket. Save the Recovery Key it shows.
						</p>
						{#if readyRepos.length > 0}
							<span class="state"
								>Done: {readyRepos.map((r) => r.name).join(', ')}</span
							>
						{:else if awaiting.length}
							<span class="state">Confirm the Recovery Key above to finish.</span>
						{:else if canAddRepository}
							<div>
								<Button href={routes.backupRepositoryNew()}>Add Repository</Button>
							</div>
						{:else}
							<span class="state"
								>Ask the owner of this Docker Manager to add one.</span
							>
						{/if}
					</div>
				</li>
				<li class:done={!!st?.primaryRepositoryId}>
					<span class="marker" aria-hidden="true">2</span>
					<div class="step">
						<h3>Choose the Primary Repository</h3>
						<p class="muted">
							Every backup goes there; a Secondary repository gets a second copy.
						</p>
						{#if st && has(st, 'backup_policy.manage')}
							<div>
								<Button
									variant={readyRepos.length ? 'primary' : 'secondary'}
									disabled={!readyRepos.length}
									onclick={() => (editDialog.open = true)}>Edit Backups</Button
								>
							</div>
						{/if}
					</div>
				</li>
			</ol>
		</Card>
	{/if}

	{#if st && setupDone}
		<p class="sentence">
			{settingsSentence(st, {
				primary: primary?.name,
				secondary: secondary?.name,
				running: backingUp
			})}
		</p>
		<KpiRow>
			<KpiCard
				label="Last Complete Run"
				value={lastComplete ? formatRelative(lastComplete.startedAt) : 'None yet'}
				secondary={lastComplete
					? lastBytes !== undefined
						? `${formatBytes(lastBytes)} backed up`
						: formatDateTime(lastComplete.startedAt)
					: 'Back up now to create one'}
				icon={DatabaseBackup}
				color="teal"
				tone={lastComplete ? 'ok' : undefined}
			/>
			<KpiCard
				label="Next Run"
				value={st.enabled && st.schedule.nextRun
					? formatRelative(st.schedule.nextRun)
					: 'Not scheduled'}
				secondary={st.enabled && st.schedule.nextRun
					? formatDateTime(st.schedule.nextRun)
					: 'Backups are off'}
				icon={CalendarClock}
				color="slate"
			/>
			<KpiCard
				label="Partial or Failed Runs"
				value={String(troubled)}
				secondary="Of the last {sets.length} runs"
				icon={TriangleAlert}
				color="rose"
				tone={troubled ? 'warn' : undefined}
			/>
			<KpiCard
				label="Stored"
				value={storage ? formatBytes(storage.sizeBytes) : 'Not measured'}
				secondary={awaiting.length
					? `${awaiting.length} awaiting key confirmation`
					: `In ${repoCount} ${repoCount === 1 ? 'repository' : 'repositories'}`}
				icon={HardDrive}
				color="blue"
				tone={awaiting.length ? 'warn' : undefined}
			/>
		</KpiRow>

		{#if running.length}
			<Card title="Running Now">
				<RunningBackups
					jobs={running}
					policyName={() => 'Backups'}
					environmentName={envName}
				/>
			</Card>
		{/if}

		<Card title="Settings">
			{@const coverage = coverageSummary(st, envs.data ?? [])}
			<Facts
				columns={2}
				items={[
					{
						label: 'Backups',
						value: st.enabled ? 'On' : 'Off: only when you start them'
					},
					{
						label: 'Schedule',
						value: scheduleWords(st.schedule.cron, st.schedule.timeZone),
						title: `${st.schedule.cron} (${st.schedule.timeZone})`
					},
					{ label: 'Primary Repository', value: primary?.name ?? 'None' },
					{ label: 'Secondary Repository', value: secondary?.name ?? 'None' },
					{
						label: 'Environments',
						value: st.excludeEnvironments.length
							? `${coverage.value}; left out: ${st.excludeEnvironments.map(envName).join(', ')}`
							: coverage.value
					},
					{
						label: 'Stacks',
						value: st.excludeStacks.length
							? `Every managed stack except ${st.excludeStacks.map(stackName).join(', ')}`
							: 'Every managed stack, with its files and volumes'
					},
					{
						label: 'Volumes',
						value: st.excludeVolumes.length
							? `Every volume except ${st.excludeVolumes.map(volumeName).join(', ')}`
							: 'Every stack and standalone volume'
					},
					{
						label: 'Manager State',
						value: st.includeMetrics
							? 'Backed up, with metrics'
							: 'Backed up, without metrics'
					},
					{
						label: 'Containers During Backups',
						value: st.shutdown ? 'Stopped, then started again' : 'Keep running'
					},
					{
						label: 'Retention',
						value: retentionActive(st.retention)
							? `${retentionShort(st.retention)}${st.retention.afterBackup ? ', after every backup' : ''}`
							: 'Nothing is removed'
					}
				]}
			/>
		</Card>

		<Card title="Recent Runs" padding="none">
			{#snippet actions()}
				<Button size="sm" variant="ghost" href={routes.backupList()}>All Backups</Button>
			{/snippet}
			<QueryView query={settings} errorTitle="The backup runs could not be loaded.">
				{#if sets.length}
					<SetsTable
						sets={sets.slice(0, RECENT_RUNS)}
						label="Recent Runs"
						environmentName={envName}
						repositoryName={repoName}
						backups={backups.data}
						activity={running}
						canRetry={has(st, 'backup.run')}
					/>
				{:else}
					<EmptyState
						icon={DatabaseBackup}
						color="teal"
						title="No runs yet."
						description="Back up now, or turn backups on."
						level={3}
						compact
					/>
				{/if}
			</QueryView>
		</Card>

		<StorageCard totals={storage} />

		{#if can(access, 'backup_repository.read')}
			<StorageHistoryCard environmentId={environmentSelection.id} />
		{/if}

		<ConfirmDialog
			bind:open={retentionOpen}
			title="Apply the retention now?"
			message="{retentionText(
				st.retention
			)}. It covers every backup, also those of earlier backup policies. Removed backups can't be restored; the newest backup of each stack and volume always stays."
			confirmLabel={retentionState.ready && retentionState.forget
				? `Remove ${retentionState.forget} ${retentionState.forget === 1 ? 'Backup' : 'Backups'}`
				: 'Apply Retention'}
			canConfirm={retentionState.ready && retentionState.forget > 0}
			tone="danger"
			size="lg"
			onconfirm={applyRetention}
		>
			{#if retentionOpen}
				<RetentionPreviewPanel
					auto
					environmentName={envName}
					repositoryName={(r) => repos.data?.find((x) => x.id === r)?.name}
					stackName={(s) => {
						const x = stacks.data?.find((y) => y.id === s);
						return x?.displayName || x?.name;
					}}
					onstate={(s) => (retentionState = s)}
				/>
			{/if}
		</ConfirmDialog>
	{/if}

	{#if st && editDialog.open}
		<BackupSettingsDialog bind:open={editDialog.open} settings={st} />
	{/if}
</Page>

<style>
	.sentence {
		margin: 0;
		color: var(--text-default);
	}

	.setup {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		align-items: start;
		gap: var(--space-4);
		margin: 0;
	}

	.setup li {
		display: flex;
		gap: var(--space-3);
		padding: var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
	}

	.marker {
		display: grid;
		flex: none;
		place-items: center;
		width: 24px;
		height: 24px;
		border: 1px solid var(--accent);
		border-radius: var(--radius-full);
		background: var(--accent);
		color: var(--text-on-accent);
		font-size: 12px;
		font-weight: var(--weight-semibold);
	}

	.done .marker {
		border-color: var(--ok-border);
		background: var(--ok-soft);
		color: var(--ok);
	}

	.step {
		display: grid;
		gap: var(--space-2);
		align-content: start;
	}

	.step h3 {
		color: var(--text-strong);
		font-size: var(--text-control);
		font-weight: var(--weight-semibold);
	}

	.state {
		color: var(--text-default);
		font-size: var(--text-caption);
	}

	@media (max-width: 767px) {
		.setup {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
