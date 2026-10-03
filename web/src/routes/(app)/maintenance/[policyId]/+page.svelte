<script lang="ts">
	// Maintenance policy detail (#14), in the policy page layout: a status
	// sentence and the actions (Run now, Preview, Edit, Delete in the
	// menu), the KPIs (last run, next run, rules on, space reclaimed), then
	// its rules, its schedule and its recent runs (manual and scheduled
	// prune jobs). A preview shows exactly what a run removes (with
	// protected and excluded objects and why); a manual run is confirmed
	// and is a durable job (leaving never cancels it). Every running prune
	// job of the policy (one per environment of an all-environments policy,
	// by hand or scheduled) shows a progress bar, found again after a reload
	// (docs/internal/web.md, "Job progress after reload"). Editing opens the
	// policy dialog (routes.maintenanceEdit() links here with it open).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import Clock from '@lucide/svelte/icons/clock';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Eye from '@lucide/svelte/icons/eye';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import ListChecks from '@lucide/svelte/icons/list-checks';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import Server from '@lucide/svelte/icons/server';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { api, unwrap, unwrapEmpty, type Job } from '$lib/api/client';
	import { environmentsQuery, recentJobsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		DestructiveConfirm,
		Dialog,
		EmptyState,
		IconButton,
		KpiCard,
		Menu,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		StatusBadge,
		formatBytes,
		formatDateTime,
		formatRelative,
		toast,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { environmentName, ifMatch, newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import RunsTable from '$lib/features/jobs/RunsTable.svelte';
	import { groupRuns } from '$lib/features/jobs/runs';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import MaintenancePolicyDialog from '$lib/features/maintenance/MaintenancePolicyDialog.svelte';
	import PrunePreviewView from '$lib/features/maintenance/PrunePreviewView.svelte';
	import {
		categoryLabel,
		enabledRules,
		maintenanceStatusText,
		normalizeRules,
		ruleSummary,
		rulesOnText,
		rulesText,
		type MaintenancePolicy,
		type PrunePreview
	} from '$lib/features/maintenance/model';
	import {
		maintenanceDefaultsQuery,
		maintenanceKeys,
		maintenancePolicyQuery
	} from '$lib/features/maintenance/queries';
	import { runReason, runStatus } from '$lib/features/schedules/model';

	const id = $derived(page.params.policyId ?? '');
	const qc = useQueryClient();
	const policy = createQuery(() => maintenancePolicyQuery(id));
	const envs = createQuery(() => environmentsQuery());
	const defaults = createQuery(() => maintenanceDefaultsQuery());
	const info = $derived(defaults.data?.categories);
	// Manual and scheduled runs: the prune jobs this policy started.
	const jobs = createQuery(() => recentJobsQuery(50, { kind: 'prune.run', policyId: id }));
	const runs = $derived(groupRuns(jobs.data?.items ?? []).slice(0, 10));
	// The policy's running prune jobs: started here, on the schedule or
	// elsewhere.
	const active = useTrackedJobs(() => (id ? { kinds: ['prune.run'], policyId: id } : null));

	usePage(() => ({
		title: policy.data?.name ?? 'Maintenance Policy',
		crumbs: [
			{ label: 'Maintenance', href: routes.maintenance() },
			{ label: policy.data?.name ?? 'Maintenance Policy' }
		]
	}));

	let preview = $state<PrunePreview | null>(null);
	let environmentPreviews = $state<{ environmentId: string; preview: PrunePreview }[]>([]);
	let previewing = $state(false);
	let previewOpen = $state(false);
	const editDialog = urlDialog('edit');
	let previewError = $state<unknown>(null);
	let runOpen = $state(false);
	let deleteOpen = $state(false);

	async function loadPreview(p: MaintenancePolicy) {
		previewing = true;
		previewError = null;
		preview = null;
		environmentPreviews = [];
		previewOpen = true;
		try {
			if (p.scope === 'all') {
				environmentPreviews = (
					await unwrap(
						api.POST('/api/v1/maintenance-policies/{policyId}/environment-previews', {
							params: { path: { policyId: p.id } }
						})
					)
				).items;
				return;
			}
			preview = await unwrap(
				api.POST('/api/v1/maintenance-policies/{policyId}/previews', {
					params: { path: { policyId: p.id } },
					body: {}
				})
			);
		} catch (e) {
			previewError = e;
		} finally {
			previewing = false;
		}
	}

	/** What a run of the policy is called (per environment for all of them). */
	function runTitle(j: Pick<Job, 'environmentId'>): string {
		const name = policy.data?.name ?? 'the policy';
		return policy.data?.scope === 'all'
			? `Prune ${name} on ${environmentName(envs.data, j.environmentId)}`
			: `Prune ${name}`;
	}

	async function run(p: MaintenancePolicy) {
		try {
			if (p.scope === 'all') {
				const out = await unwrap(
					api.POST('/api/v1/maintenance-policies/{policyId}/environment-runs', {
						params: {
							path: { policyId: p.id },
							header: { 'Idempotency-Key': newIdempotencyKey() }
						},
						body: { confirm: true }
					})
				);
				for (const j of out.jobs) active.add(j, runTitle(j));
				return;
			}
			const j = await unwrap(
				api.POST('/api/v1/maintenance-policies/{policyId}/runs', {
					params: {
						path: { policyId: p.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { confirm: true }
				})
			);
			active.add(j, runTitle(j));
		} catch (e) {
			throw new Error(
				actionError(e, {
					maintenance_policy_empty:
						'Turn on at least one rule before running this policy.',
					maintenance_run_active:
						'This policy is already running. Wait for that run to finish.',
					prune_confirmation_required: 'The run needs your confirmation.'
				}),
				{ cause: e }
			);
		}
	}

	function finished(j: Job) {
		void qc.invalidateQueries({ queryKey: maintenanceKeys.detail(id) });
		void qc.invalidateQueries({ queryKey: ['policies', 'list'] });
		void jobs.refetch();
		preview = null;
		const name =
			policy.data?.scope === 'all'
				? `${policy.data.name} on ${environmentName(envs.data, j.environmentId)}`
				: (policy.data?.name ?? 'the policy');
		if (j.state === 'succeeded') toast.success(`Pruned ${name}`);
		else if (j.state === 'partial')
			toast.warn(`Pruned ${name} partly`, {
				body: j.error?.recovery ?? 'Some objects could not be removed.'
			});
		else toast.error(`${name} was not pruned`, { body: j.error?.recovery ?? j.error?.message });
	}

	async function remove(p: MaintenancePolicy) {
		await unwrapEmpty(
			api.DELETE('/api/v1/maintenance-policies/{policyId}', {
				params: { path: { policyId: p.id }, header: { 'If-Match': ifMatch(p.revision) } }
			})
		);
		toast.success(`Deleted maintenance policy ${p.name}`);
		await qc.invalidateQueries({ queryKey: ['policies', 'list'] });
		await goto(routes.maintenance());
	}

	function menuFor(p: MaintenancePolicy): MenuEntry[] {
		if (!has(p, 'maintenance_policy.manage')) return [];
		return [
			{
				label: 'Delete Policy',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleteOpen = true)
			}
		];
	}
</script>

<Page>
	<QueryView
		query={policy}
		errorTitle="The maintenance policy could not be loaded."
		notFoundTitle="This maintenance policy does not exist."
	>
		{#snippet children(p: MaintenancePolicy)}
			{@const env = envs.data?.find((e) => e.id === p.environmentId)}
			{@const on = enabledRules(p)}
			{@const menu = menuFor(p)}
			{@const notStarted = (p.schedule?.recentRuns ?? []).filter(
				(r) => r.outcome !== 'enqueued' && r.outcome !== 'pending'
			)}
			<PageHeader
				title={p.name}
				{...resourceIcon('maintenancePolicy')}
				description={maintenanceStatusText(p)}
				meta={[
					{
						icon: Server,
						label:
							p.scope === 'all'
								? 'All Environments'
								: environmentName(envs.data, p.environmentId)
					},
					{ label: rulesOnText(p) }
				]}
			>
				{#snippet actions()}
					{#if has(p, 'maintenance.run')}
						<Button
							variant="primary"
							icon={Play}
							loading={active.busy}
							onclick={() => (runOpen = true)}
							disabled={on.length === 0}>Run Now</Button
						>
					{/if}
					{#if has(p, 'maintenance.preview')}
						<Button icon={Eye} loading={previewing} onclick={() => loadPreview(p)}
							>Preview</Button
						>
					{/if}
					{#if has(p, 'maintenance_policy.manage')}
						<Button icon={Pencil} onclick={() => (editDialog.open = true)}>Edit</Button>
					{/if}
					{#if menu.length}
						<Menu items={menu} label="More Actions for {p.name}">
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
				{/snippet}
			</PageHeader>

			{#if env && !env.online}
				<OfflineEnvironment name={env.name} since={env.connectionChangedAt} />
			{/if}

			<ActiveJobs
				jobs={active}
				titleOf={runTitle}
				onfinish={finished}
				label="Running Prunes of {p.name}"
			/>

			<KpiRow>
				<KpiCard
					label="Last Run"
					value={p.lastRun ? formatRelative(p.lastRun.finishedAt) : 'Never'}
					secondary={p.lastRun
						? `${p.lastRun.origin === 'scheduled' ? 'Scheduled' : 'By hand'}, ${formatDateTime(p.lastRun.finishedAt)}`
						: 'Preview it, then run it'}
					icon={Clock}
					color="slate"
					tone={p.lastRun
						? p.lastRun.state === 'succeeded'
							? 'ok'
							: p.lastRun.state === 'partial'
								? 'warn'
								: 'danger'
						: undefined}
				/>
				<KpiCard
					label="Next Run"
					value={p.schedule?.enabled && p.schedule.nextRun
						? formatRelative(p.schedule.nextRun.utc)
						: 'Not scheduled'}
					secondary={p.schedule?.enabled && p.schedule.nextRun
						? formatDateTime(p.schedule.nextRun.utc)
						: 'Runs only when you start it'}
					icon={CalendarClock}
					color="slate"
				/>
				<KpiCard
					label="Rules On"
					value="{on.length} of {normalizeRules(p.rules).length}"
					secondary={rulesText(p, info)}
					icon={ListChecks}
					color="violet"
				/>
				<KpiCard
					label="Space Reclaimed"
					value={p.lastRun ? formatBytes(p.lastRun.bytesReclaimed) : '—'}
					secondary={p.lastRun
						? `Last run removed ${p.lastRun.removed}${p.lastRun.failed ? `, ${p.lastRun.failed} failed` : ''}`
						: 'By the last run'}
					icon={HardDrive}
					color="green"
					tone={p.lastRun?.failed ? 'warn' : undefined}
				/>
			</KpiRow>

			<Card title="What It Covers">
				<ul class="rules" role="list">
					{#each normalizeRules(p.rules) as r (r.category)}
						<li>
							{#if r.enabled}
								<Badge tone="accent" dot>On</Badge>
								<span>{ruleSummary(r, info)}</span>
							{:else}
								<Badge dot>Off</Badge>
								<span class="muted">{categoryLabel(r.category, info)}</span>
							{/if}
						</li>
					{/each}
				</ul>
			</Card>

			<Card title="Schedule">
				{#if p.schedule}
					<ScheduleSummary {...p.schedule} />
					{#if notStarted.length}
						<h3 class="subsection-title sub">Scheduled Runs That Did Not Start</h3>
						<ul class="runs" role="list">
							{#each notStarted.slice(0, 5) as r (r.scheduledFor)}
								{@const st = runStatus(r)}
								<li>
									<span class="num"
										>{formatDateTime(r.scheduledFor, p.schedule.timeZone)}</span
									>
									<StatusBadge
										status={st.status}
										kind={st.kind}
										label={st.label || undefined}
									/>
									{#if runReason(r)}<span class="muted">{runReason(r)}</span>{/if}
								</li>
							{/each}
						</ul>
					{/if}
				{:else}
					<p class="muted">No schedule.</p>
				{/if}
			</Card>

			<Card title="Recent Runs" padding="none">
				{#if jobs.isPending}
					<div class="inset"><Skeleton lines={3} height="20px" /></div>
				{:else}
					<RunsTable {runs} label="Recent Runs of {p.name}">
						{#snippet empty()}<EmptyState
								icon={resourceIcon('maintenancePolicy').icon}
								title="No runs yet."
								description="Run it now, or turn its schedule on."
								level={3}
								compact
							/>{/snippet}
					</RunsTable>
				{/if}
			</Card>

			<Dialog
				bind:open={previewOpen}
				title="Preview of {p.name}"
				description="What a run would remove now."
				size="xl"
			>
				{#if previewError}
					<Notice tone="danger" title="The preview could not be computed" live="alert">
						{actionError(previewError)}
					</Notice>
				{:else if previewing}
					<Skeleton lines={6} height="20px" />
				{:else if preview}
					<PrunePreviewView {preview} {info} />
				{:else}
					<div class="env-previews">
						{#each environmentPreviews as environmentPreview (environmentPreview.environmentId)}
							<section
								aria-label={environmentName(
									envs.data,
									environmentPreview.environmentId
								)}
							>
								<h3 class="subsection-title">
									{environmentName(envs.data, environmentPreview.environmentId)}
								</h3>
								<PrunePreviewView preview={environmentPreview.preview} {info} />
							</section>
						{:else}
							<p class="muted">No environment in scope could be previewed.</p>
						{/each}
					</div>
				{/if}
				{#snippet footer()}
					<Button variant="ghost" onclick={() => (previewOpen = false)}>Close</Button>
					{#if has(p, 'maintenance.run')}
						<Button
							variant="danger-soft"
							disabled={previewing ||
								!!previewError ||
								on.length === 0 ||
								(!!preview && preview.remove === 0)}
							onclick={() => {
								previewOpen = false;
								runOpen = true;
							}}>Run Now</Button
						>
					{/if}
				{/snippet}
			</Dialog>

			<ConfirmDialog
				bind:open={runOpen}
				title="Run {p.name} now?"
				message={preview
					? `Removes ${preview.remove} ${preview.remove === 1 ? 'object' : 'objects'} on ${environmentName(envs.data, p.environmentId)}, about ${formatBytes(preview.bytes)}.`
					: `Removes what the turned-on rules find on ${p.scope === 'all' ? 'all environments' : environmentName(envs.data, p.environmentId)} now.`}
				consequences={[
					...on.map((r) => ruleSummary(r, info)),
					'Every object is checked again right before it is removed; protected objects are always kept.',
					'A completed removal cannot be undone. The run continues if you leave this page.'
				]}
				confirmLabel="Run {p.name}"
				tone="danger"
				onconfirm={() => run(p)}
			/>
			{#if editDialog.open}
				<MaintenancePolicyDialog bind:open={editDialog.open} policy={p} />
			{/if}
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete Maintenance Policy {p.name}"
				consequences={[
					'Removes the policy and its schedule; queued scheduled runs are dropped.',
					'A run in progress finishes. Nothing on the environment is removed by deleting the policy.',
					'Job history and the audit log stay.'
				]}
				confirmText={p.name}
				confirmLabel="Delete Policy"
				onconfirm={() => remove(p)}
			/>
		{/snippet}
	</QueryView>
</Page>

<style>
	.rules,
	.runs {
		display: grid;
		gap: var(--space-2);
	}

	.rules li,
	.runs li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.sub {
		margin: var(--space-4) 0 var(--space-2);
	}

	.inset {
		padding: var(--space-4);
	}

	.env-previews {
		display: grid;
		gap: var(--space-5);
	}

	.env-previews .subsection-title {
		margin-bottom: var(--space-2);
	}
</style>
