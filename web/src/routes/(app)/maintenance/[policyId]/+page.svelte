<script lang="ts">
	// Maintenance policy detail (#14): its last run, rules and schedule, a
	// preview of exactly what a run removes (with protected and excluded
	// objects and why) in a dialog, and a manual run with confirmation and
	// the "Run in background" choice. Both modes are the same durable job;
	// leaving never cancels it. Editing opens the policy dialog
	// (routes.maintenanceEdit() links here with it open).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Eye from '@lucide/svelte/icons/eye';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Pencil from '@lucide/svelte/icons/pencil';
	import Server from '@lucide/svelte/icons/server';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Wrench from '@lucide/svelte/icons/wrench';
	import { api, unwrap, unwrapEmpty, type Job } from '$lib/api/client';
	import { environmentsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
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
		JobProgress,
		KpiCard,
		Menu,
		Notice,
		OfflineEnvironment,
		PageHeader,
		Skeleton,
		StatusBadge,
		Switch,
		formatBytes,
		formatDateTime,
		formatRelative,
		toast,
		type MenuEntry
	} from '$lib/ui';
	import { has } from '$lib/features/common/access';
	import { environmentName, ifMatch, newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import Columns from '$lib/features/common/Columns.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import MaintenancePolicyDialog from '$lib/features/maintenance/MaintenancePolicyDialog.svelte';
	import PrunePreviewView from '$lib/features/maintenance/PrunePreviewView.svelte';
	import {
		categoryLabel,
		enabledRules,
		normalizeRules,
		ruleSummary,
		type MaintenancePolicy,
		type PrunePreview
	} from '$lib/features/maintenance/model';
	import {
		maintenanceDefaultsQuery,
		maintenanceKeys,
		maintenancePolicyQuery
	} from '$lib/features/maintenance/queries';

	const id = $derived(page.params.policyId ?? '');
	const qc = useQueryClient();
	const policy = createQuery(() => maintenancePolicyQuery(id));
	const envs = createQuery(() => environmentsQuery());
	const defaults = createQuery(() => maintenanceDefaultsQuery());
	const info = $derived(defaults.data?.categories);

	usePage(() => ({
		title: policy.data?.name ?? 'Maintenance policy',
		crumbs: [
			{ label: 'Maintenance', href: routes.maintenance() },
			{ label: policy.data?.name ?? 'Maintenance policy' }
		]
	}));

	let preview = $state<PrunePreview | null>(null);
	let environmentPreviews = $state<{ environmentId: string; preview: PrunePreview }[]>([]);
	let environmentJobs = $state<Job[]>([]);
	let previewing = $state(false);
	let previewOpen = $state(false);
	const editDialog = urlDialog('edit');
	let previewError = $state<unknown>(null);
	let runOpen = $state(false);
	let background = $state(false);
	let job = $state<Job | null>(null);
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

	async function run(p: MaintenancePolicy) {
		const bg = background;
		try {
			if (p.scope === 'all') {
				environmentJobs = (
					await unwrap(
						api.POST('/api/v1/maintenance-policies/{policyId}/environment-runs', {
							params: {
								path: { policyId: p.id },
								header: { 'Idempotency-Key': newIdempotencyKey() }
							},
							body: { confirm: true }
						})
					)
				).jobs;
				job = environmentJobs[0] ?? null;
				return;
			}
			job = await unwrap(
				api.POST('/api/v1/maintenance-policies/{policyId}/runs', {
					params: {
						path: { policyId: p.id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { confirm: true, background: bg }
				})
			);
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
		if (bg && job) {
			const jobId = job.id;
			toast.info(`Pruning ${p.name} in the background`, {
				body: 'You can leave this page; the result appears in Jobs and here.',
				action: { label: 'Open job', onclick: () => void goto(routes.job(jobId)) }
			});
		}
	}

	function finished(j: Job) {
		void qc.invalidateQueries({ queryKey: maintenanceKeys.detail(id) });
		void qc.invalidateQueries({ queryKey: ['policies', 'list'] });
		preview = null;
		const name = policy.data?.name ?? 'the policy';
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
			{ label: 'Edit policy', icon: Pencil, onSelect: () => (editDialog.open = true) },
			{ separator: true },
			{
				label: 'Delete policy',
				icon: Trash2,
				tone: 'danger',
				onSelect: () => (deleteOpen = true)
			}
		];
	}

	const OUTCOME_TONE: Record<string, 'ok' | 'warn' | 'danger' | 'neutral'> = {
		enqueued: 'ok',
		pending: 'neutral',
		missed: 'warn',
		skipped: 'neutral',
		rejected: 'warn',
		failed: 'danger'
	};
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
			<PageHeader
				title={p.name}
				icon={Wrench}
				color="slate"
				description={p.description ||
					'A prune policy: only the rules turned on below remove anything.'}
				meta={[
					{
						icon: Server,
						label:
							p.scope === 'all'
								? 'All environments'
								: environmentName(envs.data, p.environmentId)
					},
					{ label: `${on.length} of 7 rules on` }
				]}
			>
				{#snippet status()}
					{#if p.schedule?.enabled}<Badge tone="ok" dot>Scheduled</Badge>{:else}<Badge dot
							>Manual</Badge
						>{/if}
				{/snippet}
				{#snippet actions()}
					{#if has(p, 'maintenance.preview')}
						<Button icon={Eye} loading={previewing} onclick={() => loadPreview(p)}
							>Preview</Button
						>
					{/if}
					{#if has(p, 'maintenance.run')}
						<Button
							variant="danger-soft"
							onclick={() => (runOpen = true)}
							disabled={on.length === 0}>Run now</Button
						>
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

			{#if env && !env.online}
				<OfflineEnvironment name={env.name} since={env.connectionChangedAt} />
			{/if}
			{#if on.length === 0}
				<Notice tone="info" title="Every rule is off" live="none">
					This policy removes nothing until you turn on at least one rule.
					{#snippet actions()}
						{#if has(p, 'maintenance_policy.manage')}<Button
								size="sm"
								onclick={() => (editDialog.open = true)}>Edit rules</Button
							>{/if}
					{/snippet}
				</Notice>
			{/if}

			{#if job}
				<JobProgress
					jobId={job.id}
					title="Prune {p.name}"
					variant={background ? 'inline' : 'panel'}
					onfinish={finished}
				/>
			{/if}
			{#if environmentJobs.length > 1}
				<Card title="Environment jobs" subtitle="One prune job per environment in scope.">
					<ul class="env-jobs" role="list">
						{#each environmentJobs as environmentJob (environmentJob.id)}
							<li>
								<span class="env-name"
									>{environmentName(
										envs.data,
										environmentJob.environmentId
									)}</span
								>
								<a href={routes.job(environmentJob.id)}
									><StatusBadge status={environmentJob.state} kind="job" /></a
								>
							</li>
						{/each}
					</ul>
				</Card>
			{/if}

			{#if p.lastRun}
				<KpiRow>
					<KpiCard
						label="Last run"
						value={formatRelative(p.lastRun.finishedAt)}
						secondary={p.lastRun.origin === 'scheduled'
							? 'Scheduled'
							: 'Started by hand'}
						icon={Wrench}
						color="slate"
						tone={p.lastRun.state === 'succeeded'
							? 'ok'
							: p.lastRun.state === 'partial'
								? 'warn'
								: 'danger'}
					/>
					<KpiCard
						label="Removed"
						value={String(p.lastRun.removed)}
						secondary="{p.lastRun.skipped} skipped, {p.lastRun
							.deferred} left for next time"
						icon={Trash2}
						color="rose"
					/>
					<KpiCard
						label="Space reclaimed"
						value={formatBytes(p.lastRun.bytesReclaimed)}
						secondary="Approximate"
						icon={HardDrive}
						color="green"
					/>
					<KpiCard
						label="Failed removals"
						value={String(p.lastRun.failed)}
						tone={p.lastRun.failed ? 'danger' : undefined}
						secondary={p.lastRun.failed ? 'See the job for each reason' : 'None'}
					>
						{#snippet bar()}<a href={routes.job(p.lastRun!.jobId)}>Open the run</a
							>{/snippet}
					</KpiCard>
				</KpiRow>
			{/if}

			<Dialog
				bind:open={previewOpen}
				title="Preview of {p.name}"
				description="What a run would remove now. A run checks every object again right before removing it."
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
								<h3 class="sub">
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
							}}>Run now</Button
						>
					{/if}
				{/snippet}
			</Dialog>

			<Columns ratio="equal">
				<Card title="Rules">
					{#snippet actions()}
						{#if has(p, 'maintenance_policy.manage')}<Button
								size="sm"
								icon={Pencil}
								onclick={() => (editDialog.open = true)}>Edit policy</Button
							>{/if}
					{/snippet}
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
						{#if p.schedule.recentRuns.length}
							<h3 class="sub">Recent scheduled runs</h3>
							<ul class="runs" role="list">
								{#each p.schedule.recentRuns as r (r.scheduledFor)}
									<li>
										<span class="num"
											>{formatDateTime(
												r.scheduledFor,
												p.schedule.timeZone
											)}</span
										>
										<Badge tone={OUTCOME_TONE[r.outcome] ?? 'neutral'}
											>{r.outcome}</Badge
										>
										{#each r.jobs as j (j.jobId)}
											<a href={routes.job(j.jobId)}
												><StatusBadge status={j.state} kind="job" /></a
											>
										{/each}
										{#if r.reason}<span class="muted">{r.reason}</span>{/if}
									</li>
								{/each}
							</ul>
						{:else}
							<p class="muted sub-note">No scheduled runs yet.</p>
						{/if}
					{:else}
						<EmptyState icon={Wrench} title="No schedule." level={3} compact />
					{/if}
				</Card>
			</Columns>

			<ConfirmDialog
				bind:open={runOpen}
				title="Run {p.name} now?"
				message={preview
					? `Removes ${preview.remove} ${preview.remove === 1 ? 'object' : 'objects'} on ${environmentName(envs.data, p.environmentId)}, about ${formatBytes(preview.bytes)}.`
					: `Removes what the turned-on rules find on ${p.scope === 'all' ? 'all environments' : environmentName(envs.data, p.environmentId)} now.`}
				consequences={[
					...on.map((r) => ruleSummary(r, info)),
					'Every object is checked again right before it is removed; protected objects are always kept.',
					'A completed removal cannot be undone.'
				]}
				confirmLabel="Run {p.name}"
				tone="danger"
				onconfirm={() => run(p)}
			>
				<Switch
					label="Run in background"
					description="The run continues if you leave this page either way; background just keeps this page quiet."
					bind:checked={background}
				/>
			</ConfirmDialog>
			{#if editDialog.open}
				<MaintenancePolicyDialog bind:open={editDialog.open} policy={p} />
			{/if}
			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete maintenance policy {p.name}"
				consequences={[
					'Removes the policy and its schedule; queued scheduled runs are dropped.',
					'A run in progress finishes. Nothing on the environment is removed by deleting the policy.',
					'Job history and the audit log stay.'
				]}
				confirmText={p.name}
				confirmLabel="Delete policy"
				onconfirm={() => remove(p)}
			/>
		{/snippet}
	</QueryView>
</Page>

<style>
	.env-jobs {
		display: grid;
		gap: var(--space-2);
	}

	.env-jobs li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding-top: var(--space-2);
		border-top: 1px solid var(--border-subtle);
	}

	.env-jobs li:first-child {
		padding-top: 0;
		border-top: none;
	}

	.env-name {
		color: var(--text-strong);
	}

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
		font-size: var(--text-control);
		color: var(--text-strong);
	}

	.sub-note {
		margin-top: var(--space-3);
	}

	.env-previews {
		display: grid;
		gap: var(--space-5);
	}

	.env-previews .sub {
		margin-top: 0;
	}
</style>
