<script lang="ts">
	// Backup policy detail (#10): scope, schedule and retention, recent sets
	// with per-host snapshot times, a manual run, retention with its preview
	// and confirmation, and editing through the setup wizard.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Eraser from '@lucide/svelte/icons/eraser';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		DestructiveConfirm,
		EmptyState,
		IconButton,
		JobProgress,
		Menu,
		PageHeader,
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
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import PolicyWizard from '$lib/features/backups/PolicyWizard.svelte';
	import RetentionPreviewPanel from '$lib/features/backups/RetentionPreviewPanel.svelte';
	import SetsTable from '$lib/features/backups/SetsTable.svelte';
	import {
		hasRetentionRules,
		retentionText,
		scopeText,
		type BackupPolicy
	} from '$lib/features/backups/model';
	import { backupPolicyQuery, repositoriesQuery } from '$lib/features/backups/queries';

	const id = $derived(page.params.policyId ?? '');
	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const policy = createQuery(() => backupPolicyQuery(id));
	const repos = createQuery(() => repositoriesQuery());
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	const envName = (e: string) => environmentName(envs.data, e);

	usePage(() => ({
		title: policy.data?.name ?? 'Backup policy',
		crumbs: [
			{ label: 'Backups', href: routes.backups() },
			{ label: 'Policies', href: routes.backupPolicies() },
			{ label: policy.data?.name ?? 'Policy' }
		]
	}));

	let editing = $state(false);
	let running = $state(false);
	let jobs = $state<Job[]>([]);
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
						'Confirm the Recovery Key of the repositories this policy uses first.'
				})
			});
		} finally {
			running = false;
		}
	}

	function finished(j: Job) {
		void qc.invalidateQueries({ queryKey: ['policies'] });
		void qc.invalidateQueries({ queryKey: ['backups'] });
		const where = j.environmentId ? envName(j.environmentId) : 'the manager';
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
		await unwrap(
			api.DELETE('/api/v1/backup-policies/{policyId}', {
				params: { path: { policyId: p.id }, header: { 'If-Match': ifMatch(p.revision) } }
			})
		);
		toast.success(`Deleted backup policy ${p.name}`);
		await qc.invalidateQueries({ queryKey: ['policies'] });
		await goto(routes.backupPolicies());
	}

	function menuFor(p: BackupPolicy): MenuEntry[] {
		const items: MenuEntry[] = [];
		if (has(p, 'backup.retention') && hasRetentionRules(p.retention))
			items.push({
				label: 'Apply retention now',
				icon: Eraser,
				onSelect: () => (retentionOpen = true)
			});
		if (has(p, 'backup_policy.manage')) {
			items.push({ label: 'Edit policy', icon: Pencil, onSelect: () => (editing = true) });
			items.push({ separator: true });
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
			? `${s.displayName || s.name} (${envName(s.environmentId)})`
			: 'A stack you cannot see';
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
			<PageHeader
				title={p.name}
				icon={CalendarClock}
				color="teal"
				description="Backs up {scopeText(p)} to {repo?.name ?? 'its repository'}."
			>
				{#snippet status()}
					{#if p.schedule?.enabled}<Badge tone="ok" dot>Scheduled</Badge>{:else}<Badge dot
							>Manual</Badge
						>{/if}
				{/snippet}
				{#snippet actions()}
					{#if has(p, 'backup.run') && !editing}
						<Button
							variant="primary"
							icon={Play}
							loading={running}
							onclick={() => run(p)}>Back up now</Button
						>
					{/if}
					{#if menu.length && !editing}
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

			{#if editing}
				<Card>
					{#key p.id}<PolicyWizard policy={p} owner={!!perms.data?.owner} />{/key}
				</Card>
				<div>
					<Button variant="ghost" onclick={() => (editing = false)}>Stop editing</Button>
				</div>
			{:else}
				{#each jobs as j (j.id)}
					<JobProgress
						jobId={j.id}
						title="{j.kind.startsWith('manager')
							? 'Manager'
							: envName(j.environmentId ?? '')}: {j.kind.replace('.', ' ')}"
						onfinish={finished}
					/>
				{/each}

				<Card title="Recent backup sets" padding="none">
					{#if p.recentSets?.length}
						<SetsTable
							sets={p.recentSets.map((s) => ({
								...s,
								policyId: p.id,
								policyName: p.name
							}))}
							label="Recent backup sets of {p.name}"
							showPolicy={false}
							environmentName={envName}
							canRetry={() => has(p, 'backup.run')}
						/>
					{:else}
						<EmptyState
							icon={CalendarClock}
							color="teal"
							title="Not run yet."
							description="Back up now to create the first set, or turn the schedule on."
							level={3}
							compact
						/>
					{/if}
				</Card>

				<Columns ratio="equal">
					<Card title="What is backed up">
						<Facts
							columns={1}
							items={[
								{
									label: 'Manager state',
									value: p.includeManagerState
										? p.includeMetrics
											? 'Yes, with metrics'
											: 'Yes, without metrics'
										: 'No'
								},
								{
									label: 'Stacks',
									value: p.stacks.length
										? p.stacks.map((s) => stackName(s.stackId)).join(', ')
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
								},
								{
									label: 'Paths outside project directories',
									value:
										p.stacks.flatMap((s) => s.externalPaths ?? []).join(', ') ||
										'None opted in',
									mono: p.stacks.some((s) => s.externalPaths?.length)
								},
								{
									label: 'Containers during backups',
									value: p.shutdown
										? 'Stopped, then started again'
										: 'Keep running (live)'
								}
							]}
						/>
					</Card>
					<Card title="Schedule and retention">
						<Facts
							columns={1}
							items={[
								{ label: 'Schedule', render: sched },
								{ label: 'Retention', value: retentionText(p.retention) },
								{
									label: 'After every backup',
									value: p.retention?.afterBackup
										? 'Retention applied'
										: 'Retention applied by hand'
								},
								{ label: 'Repository', value: repo?.name ?? '—' }
							]}
						/>
					</Card>
				</Columns>
				{#snippet sched()}
					{#if p.schedule}<ScheduleSummary
							{...p.schedule}
							nextRun={p.schedule.nextRun}
						/>{/if}
				{/snippet}
			{/if}

			<ConfirmDialog
				bind:open={retentionOpen}
				title="Apply the retention of {p.name}?"
				message="Forgets the snapshots the rules no longer keep, then prunes the repositories. Review the preview first."
				consequences={[
					retentionText(p.retention) + '.',
					'The minimum recovery floor and the newest snapshot of each stack and volume are always kept.',
					'Forgotten snapshots cannot be restored afterwards. Object Lock may refuse some deletions.'
				]}
				confirmLabel="Apply retention"
				tone="danger"
				onconfirm={() => applyRetention(p)}
			>
				<RetentionPreviewPanel policyId={p.id} auto />
			</ConfirmDialog>
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete backup policy {p.name}"
				consequences={[
					'Scheduled backups of this policy stop.',
					'Its backup sets and snapshots stay and can still be restored.',
					'Repositories and the Recovery Key are not touched.'
				]}
				confirmText={p.name}
				confirmLabel="Delete policy"
				onconfirm={() => remove(p)}
			/>
		{/snippet}
	</QueryView>
</Page>
