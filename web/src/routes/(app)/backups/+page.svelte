<script lang="ts">
	// Backups overview (#10): until backups run, the two setup steps (a
	// repository with a confirmed Recovery Key, then a policy); afterwards
	// how backups stand (KPIs), the storage the repositories use, what
	// runs now (progress and the file being read), every policy on one line
	// and the recent sets on one line each (Details opens a drawer). All
	// backups and repositories have their own tabs.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import Archive from '@lucide/svelte/icons/archive';
	import Check from '@lucide/svelte/icons/check';
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
		Button,
		Card,
		EmptyState,
		KpiCard,
		Notice,
		Skeleton,
		formatDateTime,
		formatRelative
	} from '$lib/ui';
	import { can, has } from '$lib/features/common/access';
	import { environmentName } from '$lib/features/common/data';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import BackupPolicyDialog from '$lib/features/backups/BackupPolicyDialog.svelte';
	import BackupsHeader from '$lib/features/backups/BackupsHeader.svelte';
	import PolicyTable from '$lib/features/backups/PolicyTable.svelte';
	import RunningBackups from '$lib/features/backups/RunningBackups.svelte';
	import SetsTable from '$lib/features/backups/SetsTable.svelte';
	import StorageCard from '$lib/features/backups/StorageCard.svelte';
	import { recentSets, storageTotals } from '$lib/features/backups/model';
	import {
		backupActivityQuery,
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
	const troubled = $derived(
		sets.filter((s) => s.state === 'partial' || s.state === 'failed').length
	);
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
	const runningPolicies = $derived(
		new Set(running.map((a) => a.policyId).filter((id): id is string => !!id))
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
	<BackupsHeader>
		{#snippet actions()}
			{#if canCreatePolicy && readyRepos.length > 0}
				<Button variant="primary" icon={Plus} onclick={() => (createDialog.open = true)}
					>Create backup policy</Button
				>
			{:else if canAddRepository && (repos.data?.length ?? 0) === 0}
				<Button variant="primary" icon={Plus} href={routes.backupRepositoryNew()}
					>Add backup repository</Button
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

	{#if repos.isPending || policies.isPending}
		<Skeleton lines={4} height="72px" />
	{:else if !setupDone}
		<Card title="Set up backups">
			<ol class="setup" role="list">
				<li class:done={readyRepos.length > 0}>
					<span class="marker" aria-hidden="true"
						>{#if readyRepos.length > 0}<Check
								size={14}
								strokeWidth={2}
							/>{:else}1{/if}</span
					>
					<div class="step">
						<h3>Add a backup repository</h3>
						<p class="muted">
							A local disk on a host or an S3 bucket. Save the Recovery Key it shows:
							every backup opens with it.
						</p>
						{#if readyRepos.length > 0}
							<span class="state"
								>Done: {readyRepos.map((r) => r.name).join(', ')}</span
							>
						{:else if awaiting.length}
							<span class="state">Confirm the Recovery Key above to finish.</span>
						{:else if canAddRepository}
							<div>
								<Button href={routes.backupRepositoryNew()}>Add repository</Button>
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
						<h3>Create a backup policy</h3>
						<p class="muted">
							Covers all environments or one: every managed stack and volume is
							included until you leave it out. Choose when it runs and how long
							backups are kept.
						</p>
						{#if canCreatePolicy}
							<div>
								<Button
									variant={readyRepos.length ? 'primary' : 'secondary'}
									disabled={!readyRepos.length}
									onclick={() => (createDialog.open = true)}
									>Create backup policy</Button
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
				label="Backups"
				value={String(backups.data?.length ?? 0)}
				secondary="Stacks, volumes and manager state kept"
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

		<StorageCard totals={storage} />

		{#if running.length}
			<Card
				title="Running now"
				subtitle="Progress and the file each backup reads, updated every second."
			>
				<RunningBackups jobs={running} {policyName} environmentName={envName} />
			</Card>
		{/if}

		<Card
			title="Policies"
			subtitle="What is backed up, how the last run went and when the next one starts."
			padding="none"
		>
			{#snippet actions()}
				<Button size="sm" variant="ghost" href={routes.backupPolicies()}
					>All policies</Button
				>
			{/snippet}
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

		<Card
			title="Recent backup sets"
			subtitle="Each run of a policy, with the backups it holds."
			padding="none"
		>
			{#snippet actions()}
				<Button size="sm" variant="ghost" href={routes.backupList()}>All backups</Button>
			{/snippet}
			<QueryView query={policies} errorTitle="The backup sets could not be loaded.">
				{#if sets.length}
					<SetsTable
						{sets}
						label="Recent backup sets"
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
						title="No backup sets yet."
						description="Back up a policy now, or turn its schedule on."
						level={3}
						compact
					/>
				{/if}
			</QueryView>
		</Card>
	{/if}
</Page>

{#if createDialog.open}
	<BackupPolicyDialog bind:open={createDialog.open} owner={!!perms.data?.owner} />
{/if}

<style>
	.setup {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
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
