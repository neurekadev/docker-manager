<script lang="ts">
	// Maintenance (#14, #238): the one maintenance setup, for every
	// environment it does not leave out. A status sentence and the actions
	// (Run Now, Preview, Edit), the KPIs (last run, next run, rules on,
	// space reclaimed), then its rules, environments, schedule and recent
	// runs (manual and scheduled prune jobs). Nothing is pruned on install:
	// every rule and the schedule start off. A preview shows exactly what a
	// run removes per environment (with protected and excluded objects and
	// why); a run is confirmed and is one durable job per environment
	// (leaving never cancels them). Every running prune job of maintenance
	// shows a progress bar, found again after a reload (docs/internal/web.md,
	// "Job progress after reload"). Edit opens the settings dialog
	// (routes.maintenanceEdit() links here with it open).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import Clock from '@lucide/svelte/icons/clock';
	import Eye from '@lucide/svelte/icons/eye';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import ListChecks from '@lucide/svelte/icons/list-checks';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Play from '@lucide/svelte/icons/play';
	import Server from '@lucide/svelte/icons/server';
	import { api, unwrap, type Job } from '$lib/api/client';
	import { environmentsQuery, recentJobsQuery } from '$lib/api/queries';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		Dialog,
		EmptyState,
		KpiCard,
		Notice,
		PageHeader,
		Skeleton,
		StatusBadge,
		formatBytes,
		formatDateTime,
		formatRelative,
		toast
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { environmentName, newIdempotencyKey } from '$lib/features/common/data';
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
	import MaintenanceSettingsDialog from '$lib/features/maintenance/MaintenanceSettingsDialog.svelte';
	import PrunePreviewView from '$lib/features/maintenance/PrunePreviewView.svelte';
	import {
		categoryLabel,
		coveredEnvironmentsText,
		enabledRules,
		maintenanceStatusText,
		normalizeRules,
		ruleSummary,
		rulesOnText,
		rulesText,
		type MaintenanceSettings,
		type PrunePreview
	} from '$lib/features/maintenance/model';
	import { maintenanceKeys, maintenanceSettingsQuery } from '$lib/features/maintenance/queries';
	import { runReason, runStatus } from '$lib/features/schedules/model';

	usePage({ title: 'Maintenance', crumbs: [{ label: 'Maintenance' }] });

	const qc = useQueryClient();
	const settings = createQuery(() => maintenanceSettingsQuery());
	const envs = createQuery(() => environmentsQuery());
	const id = $derived(settings.data?.id ?? '');
	// Manual and scheduled runs: the prune jobs maintenance started.
	const jobs = createQuery(() => ({
		...recentJobsQuery(50, { kind: 'prune.run', policyId: id }),
		enabled: !!id
	}));
	const runs = $derived(groupRuns(jobs.data?.items ?? []).slice(0, 10));
	// Maintenance's running prune jobs: started here, on the schedule or
	// elsewhere.
	const active = useTrackedJobs(() => (id ? { kinds: ['prune.run'], policyId: id } : null));

	interface EnvironmentPreview {
		environmentId: string;
		preview?: PrunePreview;
		errorClass?: string;
		errorMessage?: string;
	}

	let previews = $state<EnvironmentPreview[]>([]);
	let previewing = $state(false);
	let previewOpen = $state(false);
	const editDialog = urlDialog('edit');
	let previewError = $state<unknown>(null);
	let runOpen = $state(false);
	const previewTotal = $derived(
		previews.reduce(
			(t, p) => ({
				remove: t.remove + (p.preview?.remove ?? 0),
				bytes: t.bytes + (p.preview?.bytes ?? 0)
			}),
			{ remove: 0, bytes: 0 }
		)
	);

	async function loadPreview() {
		previewing = true;
		previewError = null;
		previews = [];
		previewOpen = true;
		try {
			previews = (await unwrap(api.POST('/api/v1/maintenance-settings/previews'))).items;
		} catch (e) {
			previewError = e;
		} finally {
			previewing = false;
		}
	}

	/** What a run on one environment is called. */
	function runTitle(j: Pick<Job, 'environmentId'>): string {
		return `Prune ${environmentName(envs.data, j.environmentId)}`;
	}

	async function run() {
		try {
			const out = await unwrap(
				api.POST('/api/v1/maintenance-settings/runs', {
					params: { header: { 'Idempotency-Key': newIdempotencyKey() } },
					body: { confirm: true }
				})
			);
			for (const j of out.jobs) active.add(j, runTitle(j));
		} catch (e) {
			throw new Error(
				actionError(e, {
					maintenance_empty: 'Turn on at least one rule before running maintenance.',
					maintenance_no_environments:
						'Every environment is left out. Include one in the settings first.',
					maintenance_run_active:
						'Maintenance is already running. Wait for that run to finish.',
					prune_confirmation_required: 'The run needs your confirmation.'
				}),
				{ cause: e }
			);
		}
	}

	function finished(j: Job) {
		void qc.invalidateQueries({ queryKey: maintenanceKeys.settings });
		void jobs.refetch();
		previews = [];
		const name = environmentName(envs.data, j.environmentId);
		if (j.state === 'succeeded') toast.success(`Pruned ${name}`);
		else if (j.state === 'partial')
			toast.warn(`Pruned ${name} partly`, {
				body: j.error?.recovery ?? 'Some objects could not be removed.'
			});
		else toast.error(`${name} was not pruned`, { body: j.error?.recovery ?? j.error?.message });
	}
</script>

<Page>
	<QueryView
		query={settings}
		errorTitle="The maintenance settings could not be loaded."
		deniedTitle="You can't view maintenance."
	>
		{#snippet children(p: MaintenanceSettings)}
			{@const on = enabledRules(p)}
			{@const notStarted = p.schedule.recentRuns.filter(
				(r) => r.outcome !== 'enqueued' && r.outcome !== 'pending'
			)}
			<PageHeader
				title="Maintenance"
				{...resourceIcon('maintenancePolicy')}
				description={maintenanceStatusText(p)}
				meta={[
					{ icon: Server, label: coveredEnvironmentsText(p, envs.data) },
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
						<Button icon={Eye} loading={previewing} onclick={loadPreview}
							>Preview</Button
						>
					{/if}
					{#if has(p, 'maintenance_policy.manage')}
						<Button icon={Pencil} onclick={() => (editDialog.open = true)}>Edit</Button>
					{/if}
				{/snippet}
			</PageHeader>

			<ActiveJobs
				jobs={active}
				titleOf={runTitle}
				onfinish={finished}
				label="Running Prunes"
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
					value={p.enabled && p.schedule.nextRun
						? formatRelative(p.schedule.nextRun.utc)
						: 'Not scheduled'}
					secondary={p.enabled && p.schedule.nextRun
						? formatDateTime(p.schedule.nextRun.utc)
						: 'Runs only when you start it'}
					icon={CalendarClock}
					color="slate"
				/>
				<KpiCard
					label="Rules On"
					value="{on.length} of {normalizeRules(p.rules).length}"
					secondary={rulesText(p, p.categories)}
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

			<Card title="What It Cleans">
				<ul class="rules" role="list">
					{#each normalizeRules(p.rules) as r (r.category)}
						<li>
							{#if r.enabled}
								<Badge tone="accent" dot>On</Badge>
								<span>{ruleSummary(r, p.categories)}</span>
							{:else}
								<Badge dot>Off</Badge>
								<span class="muted">{categoryLabel(r.category, p.categories)}</span>
							{/if}
						</li>
					{/each}
				</ul>
			</Card>

			<Card title="Schedule">
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
			</Card>

			<Card title="Recent Runs" padding="none">
				{#if jobs.isPending}
					<div class="inset"><Skeleton lines={3} height="20px" /></div>
				{:else}
					<RunsTable {runs} label="Recent Maintenance Runs">
						{#snippet empty()}<EmptyState
								icon={resourceIcon('maintenancePolicy').icon}
								title="No runs yet."
								description="Run it now, or turn it on."
								level={3}
								compact
							/>{/snippet}
					</RunsTable>
				{/if}
			</Card>

			<Dialog
				bind:open={previewOpen}
				title="Maintenance Preview"
				description="What a run would remove now."
				size="xl"
			>
				{#if previewError}
					<Notice tone="danger" title="The preview could not be computed" live="alert">
						{actionError(previewError)}
					</Notice>
				{:else if previewing}
					<Skeleton lines={6} height="20px" />
				{:else}
					<div class="env-previews">
						{#each previews as item (item.environmentId)}
							<section aria-label={environmentName(envs.data, item.environmentId)}>
								<h3 class="subsection-title">
									{environmentName(envs.data, item.environmentId)}
								</h3>
								{#if item.preview}
									<PrunePreviewView preview={item.preview} info={p.categories} />
								{:else}
									<Notice tone="warn" title="Not previewed" live="none">
										{item.errorMessage ?? 'The environment did not answer.'}
									</Notice>
								{/if}
							</section>
						{:else}
							<p class="muted">Maintenance covers no environment.</p>
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
								previews.length === 0}
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
				title="Run maintenance now?"
				message={previews.some((x) => x.preview)
					? `Removes about ${previewTotal.remove} ${previewTotal.remove === 1 ? 'object' : 'objects'}, about ${formatBytes(previewTotal.bytes)}, on ${coveredEnvironmentsText(p, envs.data).toLowerCase()}.`
					: `Removes what the turned-on rules find on ${coveredEnvironmentsText(p, envs.data).toLowerCase()} now.`}
				consequences={[
					...on.map((r) => ruleSummary(r, p.categories)),
					'Every object is checked again right before it is removed; protected objects are always kept.',
					'A completed removal cannot be undone. The run continues if you leave this page.'
				]}
				confirmLabel="Run Maintenance"
				tone="danger"
				onconfirm={run}
			/>
			{#if editDialog.open}
				<MaintenanceSettingsDialog bind:open={editDialog.open} settings={p} />
			{/if}
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
