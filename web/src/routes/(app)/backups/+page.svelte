<script lang="ts">
	// Backups overview (#10): until backups run, the two setup steps (a
	// repository with a confirmed Recovery Key, then a policy); afterwards
	// how backups stand (KPIs), what runs now (progress and the file being
	// read), the policies (the main table: each opens its page), the recent
	// runs (Details opens a drawer), the storage the repositories use and
	// how it grew (the last 30 days by default).
	// Every backup and the repositories have their own tabs.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import Check from '@lucide/svelte/icons/check';
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		EmptyState,
		KpiCard,
		Notice,
		Skeleton,
		formatBytes,
		formatDateTime,
		formatRelative
	} from '$lib/ui';
	import { can, has } from '$lib/features/common/access';
	import { environmentName } from '$lib/features/common/data';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import BackupsHeader from '$lib/features/backups/BackupsHeader.svelte';
	import PolicyTable from '$lib/features/backups/PolicyTable.svelte';
	import RunningBackups from '$lib/features/backups/RunningBackups.svelte';
	import SetsTable from '$lib/features/backups/SetsTable.svelte';
	import StorageCard from '$lib/features/backups/StorageCard.svelte';
	import StorageHistoryCard from '$lib/features/backups/StorageHistoryCard.svelte';
	import {
		isRetentionActivity,
		nextPolicyRun,
		recentSets,
		setBytes,
		storageTotals
	} from '$lib/features/backups/model';
	import {
		backupActivityQuery,
		backupPoliciesQuery,
		backupsQuery,
		repositoriesQuery
	} from '$lib/features/backups/queries';

	usePage({ title: 'Backups', crumbs: [{ label: 'Backups' }], environmentScoped: true });

	/** Recent runs listed here; the Backups tab has every backup. */
	const RECENT_RUNS = 8;

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const envs = createQuery(() => environmentsQuery());
	const repos = createQuery(() => repositoriesQuery());
	const policies = createQuery(() => backupPoliciesQuery());
	const backups = createQuery(() =>
		backupsQuery(environmentSelection.id ? { environmentId: environmentSelection.id } : {})
	);
	const envName = (id: string) => environmentName(envs.data, id);
	// The setup card opens the header's create wizard (?create=1).
	const createDialog = urlDialog('create');

	const canCreatePolicy = $derived(can(access, 'backup_policy.manage'));
	const canAddRepository = $derived(access.owner || can(access, 'backup_repository.manage'));
	const readyRepos = $derived((repos.data ?? []).filter((r) => r.state === 'ready'));
	const awaiting = $derived(
		(repos.data ?? []).filter((r) => r.state === 'awaiting_confirmation')
	);
	const policyList = $derived(
		(policies.data ?? []).filter(
			(p) =>
				!environmentSelection.id ||
				p.scope === 'all' ||
				p.environmentId === environmentSelection.id
		)
	);
	const sets = $derived(
		recentSets(policies.data ?? []).filter(
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
	const nextRun = $derived(nextPolicyRun(policyList));
	const repoCount = $derived(repos.data?.length ?? 0);
	const setupDone = $derived(readyRepos.length > 0 && (policies.data ?? []).length > 0);

	// Running backups: polled every second while one runs or a set is
	// still pending; when a job ends, the sets, backups and storage refresh.
	const qc = useQueryClient();
	const activity = createQuery(() =>
		backupActivityQuery(() => sets.some((s) => s.state === 'pending'))
	);
	const running = $derived(
		(activity.data ?? []).filter(
			(a) =>
				!environmentSelection.id ||
				!a.environmentId ||
				a.environmentId === environmentSelection.id
		)
	);
	// Policies with a backup running (their Back up now waits; a retention
	// does not count).
	const runningPolicies = $derived(
		new Set(
			running
				.filter((a) => !isRetentionActivity(a))
				.map((a) => a.policyId)
				.filter((id): id is string => !!id)
		)
	);
	let seenJobs = new Set<string>();
	$effect(() => {
		const now = new Set((activity.data ?? []).map((a) => a.jobId));
		if ([...seenJobs].some((id) => !now.has(id))) {
			void qc.invalidateQueries({ queryKey: ['policies'] });
			void qc.invalidateQueries({ queryKey: ['backups'] });
		}
		seenJobs = now;
	});
	const policyName = (id: string | undefined) =>
		(id && policies.data?.find((p) => p.id === id)?.name) || 'Backup';
	const storage = $derived(storageTotals(repos.data ?? [], environmentSelection.id));
</script>

<Page>
	<BackupsHeader />

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

	{#if repos.isPending || policies.isPending}
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
				<li class:done={(policies.data ?? []).length > 0}>
					<span class="marker" aria-hidden="true">2</span>
					<div class="step">
						<h3>Create a Backup Policy</h3>
						<p class="muted">Choose what to back up, when and for how long.</p>
						{#if canCreatePolicy}
							<div>
								<Button
									variant={readyRepos.length ? 'primary' : 'secondary'}
									disabled={!readyRepos.length}
									onclick={() => (createDialog.open = true)}
									>Create Backup Policy</Button
								>
							</div>
						{/if}
					</div>
				</li>
			</ol>
		</Card>
	{/if}

	{#if setupDone}
		<KpiRow>
			<KpiCard
				label="Last Complete Run"
				value={lastComplete ? formatRelative(lastComplete.startedAt) : 'None yet'}
				secondary={lastComplete
					? lastBytes !== undefined
						? `${formatBytes(lastBytes)} backed up`
						: formatDateTime(lastComplete.startedAt)
					: 'Run a policy to create one'}
				icon={DatabaseBackup}
				color="teal"
				tone={lastComplete ? 'ok' : undefined}
			/>
			<KpiCard
				label="Next Run"
				value={nextRun ? formatRelative(nextRun) : 'Not scheduled'}
				secondary={nextRun ? formatDateTime(nextRun) : 'No schedule is on'}
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
				<RunningBackups jobs={running} {policyName} environmentName={envName} />
			</Card>
		{/if}

		<Card title="Policies" padding="none">
			{#if policyList.length}
				<PolicyTable
					policies={policyList}
					repositories={repos.data}
					environmentName={envName}
					running={runningPolicies}
				/>
			{:else}
				<EmptyState
					icon={DatabaseBackup}
					color="teal"
					title="No policy covers this environment."
					description="Create a policy for it, or for all environments."
					level={3}
					compact
				/>
			{/if}
		</Card>

		<Card title="Recent Runs" padding="none">
			{#snippet actions()}
				<Button size="sm" variant="ghost" href={routes.backupList()}>All Backups</Button>
			{/snippet}
			<QueryView query={policies} errorTitle="The backup runs could not be loaded.">
				{#if sets.length}
					<SetsTable
						sets={sets.slice(0, RECENT_RUNS)}
						label="Recent Runs"
						environmentName={envName}
						backups={backups.data}
						activity={running}
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
						title="No runs yet."
						description="Back up a policy now, or turn its schedule on."
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
	{/if}
</Page>

<style>
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
