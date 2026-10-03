<script lang="ts">
	// Updates (#20, #240): the one update setup for every environment. A
	// status sentence and the actions (Preview Updates, Check Now, Edit),
	// the KPIs, then what needs attention (updates available, failed checks
	// or runs; each with a preview of its update), everything the settings
	// cover (left-out and no longer found ones included), the schedules and
	// the recent runs. The KPIs and Needs Attention come from the target
	// records the caller may see (also with grants on one environment); the
	// settings, what they cover and the actions need grants on all
	// environments. Check runs a digest check on every target (never
	// pulls); Preview Updates opens what a run would do and applies it.
	// Edit opens the settings dialog (routes.updatesEdit() links here with
	// it open). Running checks and updates show under the header, from the
	// running jobs list, so they are still there after a reload (docs/
	// internal/web.md, "Job progress after reload").
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Clock from '@lucide/svelte/icons/clock';
	import Eye from '@lucide/svelte/icons/eye';
	import Pencil from '@lucide/svelte/icons/pencil';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap, type Job, type Schema } from '$lib/api/client';
	import { environmentsQuery, recentJobsQuery, schedulesQuery } from '$lib/api/queries';
	import { resourceIcon } from '$lib/features/common/resourceIcons';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		Dialog,
		EmptyState,
		ErrorState,
		KpiCard,
		Notice,
		PageHeader,
		Skeleton,
		Table,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import { environmentName, newIdempotencyKey, stacksQuery } from '$lib/features/common/data';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import { actionError } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import ActiveJobs from '$lib/features/jobs/ActiveJobs.svelte';
	import { stackNames } from '$lib/features/jobs/labels';
	import RunsTable from '$lib/features/jobs/RunsTable.svelte';
	import { groupRuns } from '$lib/features/jobs/runs';
	import { useTrackedJobs } from '$lib/features/jobs/tracked.svelte';
	import TargetName from '$lib/features/updates/TargetName.svelte';
	import UpdatePreviewDialog from '$lib/features/updates/UpdatePreviewDialog.svelte';
	import UpdateSettingsDialog from '$lib/features/updates/UpdateSettingsDialog.svelte';
	import {
		candidateStatus,
		imageLabel,
		inactiveReason,
		policyStatusText,
		publishedText,
		reasonLabel,
		recordRow,
		summarizeTargets,
		summaryState,
		targetState,
		targetsUpdateText,
		windowText,
		withExclusions,
		type TargetRow,
		type UpdateCandidate,
		type UpdatePolicy
	} from '$lib/features/updates/model';
	import {
		updatePoliciesQuery,
		updateSettingsQuery,
		updateTargetsQuery,
		type UpdateSettings
	} from '$lib/features/updates/queries';

	type Preview = Schema<'UpdateSettingsPreviewOutputBody'>;
	interface PreviewRow {
		key: string;
		type: string;
		id: string;
		environmentId: string;
		item: UpdateCandidate;
	}

	usePage({ title: 'Updates', crumbs: [{ label: 'Updates' }], environmentScoped: true });
	const qc = useQueryClient();
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	const single = singleEnvironment();
	const settings = createQuery(() => ({ ...updateSettingsQuery(), retry: false }));
	const id = $derived(settings.data?.id ?? '');
	const canRead = $derived(!!settings.data);
	// The covered targets' records the caller may see (candidate
	// summaries, actions), in the selected environment.
	const records = createQuery(() => updatePoliciesQuery(environmentSelection.id));
	const targets = createQuery(() => ({ ...updateTargetsQuery(), enabled: canRead }));
	const schedules = createQuery(() => ({ ...schedulesQuery(), enabled: canRead }));
	// Checks and updates of the settings (scheduled or by hand).
	const settingsJobs = createQuery(() => ({
		...recentJobsQuery(50, { policyId: id }),
		enabled: !!id
	}));
	const editDialog = urlDialog('edit');
	const envName = (envId?: string) => environmentName(envs.data, envId ?? '');
	const has = (action: string) => !!settings.data?.actions.includes(action);

	const byId = $derived(new Map((records.data ?? []).map((p) => [p.id, p])));
	const covered = $derived((records.data ?? []).map(recordRow));
	const totals = $derived(summarizeTargets(covered.map((t) => t.candidateSummary)));
	const updates = $derived(targetsUpdateText(covered));
	const attention = $derived(
		covered.filter((t) => {
			const s = t.candidateSummary;
			return s.available > 0 || s.failed + s.quarantined > 0;
		})
	);
	const runs = $derived(groupRuns(settingsJobs.data?.items ?? []).slice(0, 10));
	const mySchedules = $derived((schedules.data ?? []).filter((s) => s.policyId === id));
	const scheduleOf = (kind: string) => mySchedules.find((s) => s.kind === kind);
	const nextRun = $derived(
		mySchedules
			.filter((s) => s.enabled && s.nextRun && !s.invalidReason)
			.sort((a, b) => a.nextRun!.utc.localeCompare(b.nextRun!.utc))[0]
	);

	// Running checks and updates in the selected environment (every
	// target's, scheduled or by hand).
	const running = useTrackedJobs(() => ({
		kinds: ['update.check', 'update.run'],
		environmentId: environmentSelection.id ?? undefined
	}));
	const checksRunning = $derived(!!running.runningOf('update.check'));
	// Finished ones make room after a moment (Recent Runs keeps them);
	// failed ones stay until dismissed.
	function runFinished(job: Job) {
		void qc.invalidateQueries({ queryKey: ['policies'] });
		if (job.state === 'succeeded') setTimeout(() => running.dismiss(job.id), 4000);
	}

	function stackName(stackId: string): string {
		const stack = stacks.data?.find((s) => s.id === stackId);
		return stack?.displayName || stack?.name || '';
	}

	function targetName(t: TargetRow): string {
		return t.type === 'container' ? t.id : stackName(t.id);
	}

	function plural(n: number, one: string, many: string): string {
		return `${n} ${n === 1 ? one : many}`;
	}

	let error = $state<unknown>(null);
	let checking = $state(false);
	let previewing = $state(false);
	let applying = $state(false);
	let preview = $state<Preview | null>(null);
	let previewOpen = $state(false);

	async function check() {
		checking = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/update-settings/checks', {
					params: { header: { 'Idempotency-Key': newIdempotencyKey() } }
				})
			);
			for (const j of out.jobs) running.add(j);
			toast.success(
				`Checking ${plural(out.jobs.length, 'stack or container', 'stacks and containers')} for updates`
			);
			await qc.invalidateQueries({ queryKey: ['policies'] });
		} catch (e) {
			error = e;
		} finally {
			checking = false;
		}
	}

	async function loadPreview() {
		previewing = true;
		error = null;
		try {
			preview = await unwrap(api.POST('/api/v1/update-settings/previews'));
			previewOpen = true;
		} catch (e) {
			error = e;
		} finally {
			previewing = false;
		}
	}

	async function apply() {
		if (!preview) return;
		applying = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/update-settings/runs', {
					params: { header: { 'Idempotency-Key': newIdempotencyKey() } },
					body: { fingerprint: preview.fingerprint }
				})
			);
			for (const j of out.jobs) running.add(j);
			preview = null;
			previewOpen = false;
			toast.success(`Started ${plural(out.jobs.length, 'update', 'updates')}`);
			await qc.invalidateQueries({ queryKey: ['policies'] });
		} catch (e) {
			error = e;
		} finally {
			applying = false;
		}
	}

	// One target's update preview (Needs Attention).
	let previewOf = $state<{ policy: UpdatePolicy; name: string } | null>(null);
	let targetPreviewOpen = $state(false);
	function previewTarget(t: TargetRow, p: UpdatePolicy) {
		previewOf = { policy: p, name: p.targetName || targetName(t) || 'this target' };
		targetPreviewOpen = true;
	}

	const previewRows = $derived<PreviewRow[]>(
		(preview?.targets ?? []).flatMap((t) =>
			t.items.map((item) => ({
				key: `${t.policyId}/${item.id}`,
				type: t.type,
				id: t.id,
				environmentId: t.environmentId,
				item
			}))
		)
	);
	const drifted = $derived((preview?.targets ?? []).filter((t) => t.sourceDrift));

	const envColumn = <T extends { environmentId: string }>(): Column<T>[] =>
		single.current
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (t: T) => envName(t.environmentId),
						width: '180px',
						stack: 'meta'
					}
				];
	const attentionColumns: Column<TargetRow>[] = $derived([
		{
			id: 'target',
			header: 'Stack or Container',
			cell: targetCell,
			sortValue: (t) => targetName(t),
			maxWidth: '320px',
			stack: 'title'
		},
		...envColumn<TargetRow>(),
		{ id: 'state', header: 'Status', cell: stateCell, width: '200px', stack: 'status' },
		{
			id: 'checked',
			header: 'Last Check',
			cell: checkedCell,
			sortValue: (t) => t.candidateSummary.lastCheckAt ?? '',
			width: '150px',
			stack: 'meta'
		},
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			align: 'end',
			width: '170px',
			pin: 'end',
			stack: 'actions'
		}
	]);
	const coverageColumns: Column<TargetRow>[] = $derived([
		{
			id: 'target',
			header: 'Stack or Container',
			cell: targetCell,
			sortValue: (t) => targetName(t),
			maxWidth: '320px',
			stack: 'title'
		},
		...envColumn<TargetRow>(),
		{
			id: 'status',
			header: 'Status',
			cell: coverageStatusCell,
			sortValue: (t) =>
				t.inactive ? -1 : t.candidateSummary.available + t.candidateSummary.failed,
			width: '220px',
			stack: 'status'
		}
	]);
	const previewColumns: Column<PreviewRow>[] = [
		{
			id: 'target',
			header: 'Stack or Container',
			cell: previewTargetCell,
			maxWidth: '260px',
			stack: 'title'
		},
		{ id: 'service', header: 'Service', cell: serviceCell, width: '160px', stack: 'meta' },
		{
			id: 'image',
			header: 'Image',
			cell: imageCell,
			maxWidth: '320px',
			truncate: true,
			title: (r) =>
				`${r.item.reference}\nRunning: ${r.item.currentDigest ?? 'unknown'}\nNew: ${r.item.candidateDigest ?? 'unknown'}`,
			stack: 'meta'
		},
		{
			id: 'status',
			header: 'Status',
			cell: previewStatusCell,
			width: '200px',
			stack: 'status'
		}
	];

	/** Every target of the settings in the selected environment, left-out ones by name too. */
	function coverage(st: UpdateSettings): TargetRow[] {
		const rows = withExclusions(
			(targets.data ?? []) as TargetRow[],
			st,
			(sid) => stacks.data?.find((s) => s.id === sid)?.environmentId
		);
		return rows.filter(
			(t) => !environmentSelection.id || t.environmentId === environmentSelection.id
		);
	}
</script>

{#snippet targetCell(t: TargetRow)}<TargetName
		type={t.type}
		id={t.id}
		environmentId={t.environmentId}
	/>{/snippet}
{#snippet envCell(t: { environmentId: string })}{envName(t.environmentId)}{/snippet}
{#snippet stateCell(t: TargetRow)}
	{@const st = summaryState(t.candidateSummary)}
	<Badge tone={st.tone} dot>{st.label}</Badge>
{/snippet}
{#snippet coverageStatusCell(t: TargetRow)}
	{@const st = targetState(t.candidateSummary, inactiveReason(t))}
	<Badge tone={st.tone} dot>{st.label}</Badge>
{/snippet}
{#snippet checkedCell(t: TargetRow)}
	{#if t.candidateSummary.lastCheckAt}<span
			class="num"
			title={formatDateTime(t.candidateSummary.lastCheckAt)}
			>{formatRelative(t.candidateSummary.lastCheckAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet actionsCell(t: TargetRow)}
	{@const p = byId.get(t.policyId)}
	{#if p && p.view === 'full'}
		<Button size="sm" variant="secondary" icon={Eye} onclick={() => previewTarget(t, p)}
			>Preview Update</Button
		>
	{/if}
{/snippet}
{#snippet previewTargetCell(r: PreviewRow)}<TargetName
		type={r.type}
		id={r.id}
		environmentId={r.environmentId}
		sub={single.current ? undefined : envName(r.environmentId)}
	/>{/snippet}
{#snippet serviceCell(r: PreviewRow)}{r.item.service}{/snippet}
{#snippet imageCell(r: PreviewRow)}
	<span class="mono">{imageLabel(r.item)}</span>
	{#if publishedText(r.item)}<span
			class="muted published"
			title="Published {formatDateTime(r.item.publishedAt)}">{publishedText(r.item)}</span
		>{/if}
{/snippet}
{#snippet previewStatusCell(r: PreviewRow)}
	{@const st = candidateStatus(r.item.status)}
	<Badge tone={st.tone} dot>{st.label}</Badge>
	{#if reasonLabel(r.item)}<span class="muted reason">{reasonLabel(r.item)}</span>{/if}
{/snippet}

<Page>
	<PageHeader
		title="Updates"
		{...resourceIcon('updatePolicy')}
		description={records.data
			? policyStatusText(totals, covered.length, updates)
			: 'Looks for newer images of your stacks and containers.'}
	>
		{#snippet actions()}
			{#if has('update.check')}
				<Button variant="primary" icon={Eye} loading={previewing} onclick={loadPreview}
					>Preview Updates</Button
				>
				<Button icon={RefreshCw} loading={checking || checksRunning} onclick={check}
					>Check Now</Button
				>
			{/if}
			{#if has('update_policy.manage')}
				<Button icon={Pencil} onclick={() => (editDialog.open = true)}>Edit</Button>
			{/if}
		{/snippet}
	</PageHeader>

	<ActiveJobs
		jobs={running}
		nameOf={stackNames(stacks.data)}
		onfinish={runFinished}
		label="Running Checks and Updates"
	/>

	{#if error}
		<Notice tone="danger" title="The action did not start" live="alert"
			>{actionError(error)}</Notice
		>
	{/if}

	<QueryView query={records} errorTitle="The updates could not be loaded.">
		<KpiRow>
			<KpiCard
				label="Updates Available"
				value={String(totals.withUpdates)}
				secondary={updates}
				{...resourceIcon('updatePolicy')}
				tone={totals.withUpdates ? 'warn' : undefined}
			/>
			<KpiCard
				label="Failing"
				value={String(totals.failing)}
				secondary={totals.failing
					? 'Failed checks or updates'
					: 'No failed checks or updates'}
				icon={TriangleAlert}
				color="rose"
				tone={totals.failing ? 'danger' : undefined}
			/>
			<KpiCard
				label="Up to Date"
				value={String(totals.upToDate)}
				secondary={totals.unchecked ? `${totals.unchecked} not checked yet` : 'All checked'}
				icon={CircleCheck}
				color="green"
				tone={totals.upToDate ? 'ok' : undefined}
			/>
			<KpiCard
				label="Last Check"
				value={totals.lastCheckAt ? formatRelative(totals.lastCheckAt) : 'Never'}
				secondary={totals.lastCheckAt
					? formatDateTime(totals.lastCheckAt)
					: nextRun?.nextRun
						? `Next ${nextRun.kind === 'update_run' ? 'update' : 'check'} ${formatRelative(nextRun.nextRun.utc)}`
						: 'Check now to start'}
				icon={Clock}
				color="slate"
			/>
		</KpiRow>

		<Card title="Needs Attention" padding="none">
			<Table
				label="Stacks and containers that need attention"
				rows={attention}
				columns={attentionColumns}
				rowKey={(t) => t.policyId}
				sort={{ column: 'target', direction: 'asc' }}
			>
				{#snippet empty()}<EmptyState
						icon={CircleCheck}
						color="green"
						title="Nothing needs attention."
						description={totals.unchecked
							? 'Some stacks or containers were never checked. Check them now.'
							: 'Every covered stack and container runs the newest image of its tag.'}
						level={3}
						compact
					/>{/snippet}
			</Table>
		</Card>
	</QueryView>

	{#if settings.data}
		{@const st = settings.data}
		<Card title="What It Covers" padding="none">
			{#if targets.isPending}
				<div class="inset"><Skeleton lines={3} height="20px" /></div>
			{:else if targets.isError}
				<ErrorState
					error={targets.error}
					title="The stacks and containers could not be loaded."
					onretry={() => targets.refetch()}
					bare
					compact
				/>
			{:else}
				<Table
					label="What the Update Settings Cover"
					rows={coverage(st)}
					columns={coverageColumns}
					rowKey={(t) => t.policyId}
					sort={{ column: 'target', direction: 'asc' }}
				>
					{#snippet empty()}<EmptyState
							{...resourceIcon('updatePolicy')}
							title="Nothing to update yet."
							description="Stacks deployed by Docker Manager and standalone containers it created appear here."
							level={3}
							compact
						/>{/snippet}
				</Table>
			{/if}
		</Card>

		<Card title="Schedule">
			<Facts
				columns={2}
				items={[
					{ label: 'Check Automatically', render: checkSched },
					{ label: 'Update Automatically', render: runSched },
					{ label: 'Update Window', value: windowText(st.window) },
					{
						label: 'Health Wait',
						value: st.waitTimeoutSeconds ? `${st.waitTimeoutSeconds} s` : 'Default'
					},
					{
						label: 'Environments',
						value: st.excludeEnvironments.length
							? `All but ${st.excludeEnvironments.map((e) => envName(e)).join(', ')}`
							: 'All Environments'
					}
				]}
			/>
		</Card>

		{#snippet checkSched()}
			<ScheduleSummary
				cron={st.checkSchedule.cron ?? ''}
				timeZone={st.checkSchedule.timeZone ?? ''}
				enabled={st.checkSchedule.enabled}
				nextRun={scheduleOf('update_check')?.nextRun}
			/>
		{/snippet}
		{#snippet runSched()}
			<ScheduleSummary
				cron={st.runSchedule.cron ?? ''}
				timeZone={st.runSchedule.timeZone ?? ''}
				enabled={st.runSchedule.enabled}
				nextRun={scheduleOf('update_run')?.nextRun}
			/>
		{/snippet}

		<Card title="Recent Runs" padding="none">
			{#if settingsJobs.isPending}
				<div class="inset"><Skeleton lines={3} height="20px" /></div>
			{:else}
				<RunsTable {runs} label="Recent Update Runs">
					{#snippet empty()}<EmptyState
							icon={Clock}
							color="slate"
							title="No runs yet."
							description="Checks and updates of everything appear here."
							level={3}
							compact
						/>{/snippet}
				</RunsTable>
			{/if}
		</Card>

		{#if editDialog.open}
			<UpdateSettingsDialog bind:open={editDialog.open} settings={st} />
		{/if}
	{/if}
</Page>

<Dialog
	bind:open={previewOpen}
	title="Update Preview"
	description="Applying pulls the new images and recreates the services that changed, dependencies first."
	size="xl"
	dismissible={!applying}
>
	{#if drifted.length}
		<div class="notice">
			<Notice tone="warn" icon={TriangleAlert} title="Undeployed Source Changes" live="none">
				Deploy the changes of {drifted
					.map((t) => (t.type === 'stack' ? stackName(t.id) || 'a stack' : t.id))
					.join(', ')} before updating; they are skipped until then.
			</Notice>
		</div>
	{/if}
	<Table
		label="Update Preview"
		rows={previewRows}
		columns={previewColumns}
		rowKey={(r) => r.key}
		maxHeight="60vh"
	>
		{#snippet empty()}<EmptyState
				icon={CircleCheck}
				color="green"
				title="Nothing to update."
				description="Check now first, or everything already runs the newest image of its tag."
				level={3}
				compact
			/>{/snippet}
	</Table>
	<p class="muted small">Only images whose digest changed are pulled.</p>
	{#snippet footer()}
		<Button variant="ghost" disabled={applying} onclick={() => (previewOpen = false)}
			>Close</Button
		>
		{#if has('update.run')}
			<Button
				variant="primary"
				loading={applying}
				disabled={!previewRows.some(
					(r) => r.item.eligible && r.item.status === 'update_available'
				)}
				onclick={apply}>Apply Updates</Button
			>
		{/if}
	{/snippet}
</Dialog>

{#if previewOf}
	{#key previewOf.policy.id}
		<UpdatePreviewDialog
			bind:open={targetPreviewOpen}
			policy={previewOf.policy}
			name={previewOf.name}
			canRun={previewOf.policy.actions.includes('update.run')}
			onstart={(job, title) => running.add(job, title)}
		/>
	{/key}
{/if}

<style>
	.reason,
	.published {
		display: block;
		margin-top: var(--space-1);
		font-size: var(--text-caption);
	}

	.small {
		margin-top: var(--space-3);
		font-size: var(--text-caption);
	}

	.inset {
		padding: var(--space-4);
	}

	.notice {
		margin-bottom: var(--space-4);
	}
</style>
