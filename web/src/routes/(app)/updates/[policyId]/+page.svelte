<script lang="ts">
	// Environment update policy detail (#20), in the policy page layout: a
	// status sentence and the actions (Preview updates, Check now, Edit, the
	// rest in the menu), the KPIs (last check, next run, coverage, updates
	// available), then what it covers (every target by name, excluded and
	// no longer found ones included), its schedules and its recent runs.
	// Check runs a digest check on every target (never pulls); Preview
	// updates opens what a run would do and applies it. Editing opens the
	// policy dialog (routes.updatePolicyEdit() links here with it open).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Clock from '@lucide/svelte/icons/clock';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Eye from '@lucide/svelte/icons/eye';
	import Layers from '@lucide/svelte/icons/layers';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import Pencil from '@lucide/svelte/icons/pencil';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Server from '@lucide/svelte/icons/server';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, unwrap, unwrapEmpty, type Schema } from '$lib/api/client';
	import {
		environmentsQuery,
		myPermissionsQuery,
		recentJobsQuery,
		schedulesQuery
	} from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		DestructiveConfirm,
		Dialog,
		EmptyState,
		ErrorState,
		IconButton,
		KpiCard,
		Menu,
		Notice,
		PageHeader,
		Skeleton,
		Table,
		formatDateTime,
		formatRelative,
		toast,
		type Column,
		type MenuEntry
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import {
		environmentName,
		ifMatch,
		newIdempotencyKey,
		stacksQuery
	} from '$lib/features/common/data';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import { actionError } from '$lib/features/common/errors';
	import Facts from '$lib/features/common/Facts.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import RunsTable from '$lib/features/jobs/RunsTable.svelte';
	import { groupRuns } from '$lib/features/jobs/runs';
	import TargetName from '$lib/features/updates/TargetName.svelte';
	import UpdatePolicyDialog from '$lib/features/updates/UpdatePolicyDialog.svelte';
	import {
		candidateStatus,
		imageLabel,
		inactiveReason,
		policyStatusText,
		publishedText,
		reasonLabel,
		summarizeTargets,
		targetState,
		targetsUpdateText,
		windowText,
		withExclusions,
		type UpdateCandidate
	} from '$lib/features/updates/model';
	import {
		environmentUpdatePolicyQuery,
		environmentUpdateTargetsQuery,
		type EnvironmentUpdatePolicy
	} from '$lib/features/updates/queries';

	type Preview = Schema<'EnvironmentPreviewOutputBody'>;
	type Target = Schema<'EnvironmentTarget'>;
	interface PreviewRow {
		key: string;
		type: string;
		id: string;
		environmentId: string;
		item: UpdateCandidate;
	}

	const id = $derived(page.params.policyId ?? '');
	const qc = useQueryClient();
	const policy = createQuery(() => environmentUpdatePolicyQuery(id));
	const perms = createQuery(() => myPermissionsQuery());
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	const targets = createQuery(() => environmentUpdateTargetsQuery(id));
	const schedules = createQuery(() => schedulesQuery());
	// Checks and updates this policy started (scheduled or by hand).
	const policyJobs = createQuery(() => recentJobsQuery(50, { policyId: id }));
	const single = singleEnvironment();
	const manage = $derived(can(accessOf(perms.data), 'update_policy.manage'));
	const editDialog = urlDialog('edit');
	const active = $derived((targets.data ?? []).filter((t) => !t.inactive));
	const totals = $derived(summarizeTargets(active.map((t) => t.candidateSummary)));
	const updates = $derived(targetsUpdateText(active));
	const runs = $derived(groupRuns(policyJobs.data?.items ?? []).slice(0, 10));
	const mySchedules = $derived((schedules.data ?? []).filter((s) => s.policyId === id));
	const nextRun = $derived(
		mySchedules
			.filter((s) => s.enabled && s.nextRun && !s.invalidReason)
			.sort((a, b) => a.nextRun!.utc.localeCompare(b.nextRun!.utc))[0]
	);
	const scheduleOf = (kind: string) => mySchedules.find((s) => s.kind === kind);

	let preview = $state<Preview | null>(null);
	let previewOpen = $state(false);
	let error = $state<unknown>(null);
	let checking = $state(false);
	let previewing = $state(false);
	let applying = $state(false);
	let deleteOpen = $state(false);

	usePage(() => ({
		title: policy.data?.name ?? 'Update policy',
		crumbs: [
			{ label: 'Updates', href: routes.updates() },
			{ label: policy.data?.name ?? 'Update policy' }
		]
	}));

	function scopeLabel(p: EnvironmentUpdatePolicy): string {
		return p.scope === 'all' ? 'All environments' : environmentName(envs.data, p.environmentId);
	}

	function stackName(stackId: string): string {
		const stack = stacks.data?.find((s) => s.id === stackId);
		return stack?.displayName || stack?.name || '';
	}

	function plural(n: number, one: string, many: string): string {
		return `${n} ${n === 1 ? one : many}`;
	}

	async function check() {
		checking = true;
		error = null;
		try {
			const out = await unwrap(
				api.POST('/api/v1/environment-update-policies/{policyId}/checks', {
					params: {
						path: { policyId: id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					}
				})
			);
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
			preview = await unwrap(
				api.POST('/api/v1/environment-update-policies/{policyId}/previews', {
					params: { path: { policyId: id } }
				})
			);
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
				api.POST('/api/v1/environment-update-policies/{policyId}/runs', {
					params: {
						path: { policyId: id },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { fingerprint: preview.fingerprint }
				})
			);
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

	async function remove() {
		const p = policy.data;
		if (!p) return;
		await unwrapEmpty(
			api.DELETE('/api/v1/environment-update-policies/{policyId}', {
				params: { path: { policyId: id }, header: { 'If-Match': ifMatch(p.revision) } }
			})
		);
		toast.success(`Deleted update policy ${p.name}`);
		await qc.invalidateQueries({ queryKey: ['policies'] });
		await goto(routes.updates());
	}

	const menu = $derived.by<MenuEntry[]>(() =>
		manage
			? [
					{
						label: 'Delete policy',
						icon: Trash2,
						tone: 'danger',
						onSelect: () => (deleteOpen = true)
					}
				]
			: []
	);

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

	const targetColumns: Column<Target>[] = $derived([
		{
			id: 'target',
			header: 'Stack or container',
			cell: targetCell,
			sortValue: (t) => (t.type === 'stack' ? stackName(t.id) : t.id),
			maxWidth: '320px',
			stack: 'title'
		},
		...(single.current
			? []
			: [
					{
						id: 'environment',
						header: 'Environment',
						cell: envCell,
						sortValue: (t: Target) => environmentName(envs.data, t.environmentId),
						width: '200px',
						stack: 'meta'
					} satisfies Column<Target>
				]),
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (t) =>
				t.inactive ? -1 : t.candidateSummary.available + t.candidateSummary.failed,
			width: '220px',
			stack: 'status'
		}
	]);

	const previewColumns: Column<PreviewRow>[] = [
		{
			id: 'target',
			header: 'Stack or container',
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
</script>

{#snippet targetCell(t: Target)}<TargetName
		type={t.type}
		id={t.id}
		environmentId={t.environmentId}
	/>{/snippet}
{#snippet envCell(t: Target)}{environmentName(envs.data, t.environmentId)}{/snippet}
{#snippet statusCell(t: Target)}
	{@const st = targetState(t.candidateSummary, inactiveReason(t))}
	<Badge tone={st.tone} dot>{st.label}</Badge>
{/snippet}
{#snippet previewTargetCell(r: PreviewRow)}<TargetName
		type={r.type}
		id={r.id}
		environmentId={r.environmentId}
		sub={single.current ? undefined : environmentName(envs.data, r.environmentId)}
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
	<QueryView
		query={policy}
		errorTitle="The update policy could not be loaded."
		notFoundTitle="This update policy does not exist."
		notFoundDescription="It was deleted, or you no longer have access to it."
	>
		{#snippet children(p: EnvironmentUpdatePolicy)}
			{@const all = withExclusions(
				targets.data ?? [],
				p,
				(sid) => stacks.data?.find((s) => s.id === sid)?.environmentId
			)}
			{@const excluded = all.filter((t) => inactiveReason(t) === 'excluded').length}
			{@const missing = all.filter((t) => inactiveReason(t) === 'missing').length}
			<PageHeader
				title={p.name}
				icon={PackageCheck}
				color="violet"
				description={targets.data
					? policyStatusText(totals, active.length, updates)
					: 'Looks for newer images of the stacks and containers it covers.'}
				meta={[{ icon: Server, label: scopeLabel(p) }]}
			>
				{#snippet actions()}
					<Button variant="primary" icon={Eye} loading={previewing} onclick={loadPreview}
						>Preview updates</Button
					>
					<Button icon={RefreshCw} loading={checking} onclick={check}>Check now</Button>
					{#if manage}
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

			{#if error}
				<Notice tone="danger" title="The action did not start" live="alert"
					>{actionError(error)}</Notice
				>
			{/if}

			<KpiRow>
				<KpiCard
					label="Last check"
					value={totals.lastCheckAt ? formatRelative(totals.lastCheckAt) : 'Never'}
					secondary={totals.lastCheckAt
						? formatDateTime(totals.lastCheckAt)
						: 'Check now to start'}
					icon={Clock}
					color="slate"
				/>
				<KpiCard
					label="Next run"
					value={nextRun?.nextRun ? formatRelative(nextRun.nextRun.utc) : 'Not scheduled'}
					secondary={nextRun?.nextRun
						? `${nextRun.kind === 'update_run' ? 'Update' : 'Check'}, ${formatDateTime(nextRun.nextRun.utc)}`
						: 'Runs only when you start it'}
					icon={CalendarClock}
					color="slate"
				/>
				<KpiCard
					label="Coverage"
					value={String(active.length)}
					secondary={[
						plural(excluded, 'excluded', 'excluded'),
						missing ? `${missing} no longer found` : ''
					]
						.filter(Boolean)
						.join(', ')}
					icon={Layers}
					color="violet"
				/>
				<KpiCard
					label="Updates available"
					value={String(totals.withUpdates)}
					secondary={updates}
					icon={totals.failing ? TriangleAlert : PackageCheck}
					color="violet"
					tone={totals.failing ? 'danger' : totals.withUpdates ? 'warn' : undefined}
				/>
			</KpiRow>

			<Card
				title="What it covers"
				subtitle="Stacks and standalone containers Docker Manager manages in {p.scope ===
				'all'
					? 'every environment'
					: scopeLabel(p)}. Excluded ones are listed too."
				padding="none"
			>
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
						label="What {p.name} covers"
						rows={all as Target[]}
						columns={targetColumns}
						rowKey={(t) => t.policyId}
						sort={{ column: 'target', direction: 'asc' }}
					>
						{#snippet empty()}<EmptyState
								icon={PackageCheck}
								color="violet"
								title="Nothing to update in this scope."
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
						{ label: 'Checks', render: checkSched },
						{ label: 'Automatic updates', render: runSched },
						{ label: 'Update window', value: windowText(p.window) },
						{
							label: 'Health wait',
							value: p.waitTimeoutSeconds ? `${p.waitTimeoutSeconds} s` : 'Default'
						}
					]}
				/>
			</Card>

			{#snippet checkSched()}
				<ScheduleSummary
					cron={p.checkSchedule.cron ?? ''}
					timeZone={p.checkSchedule.timeZone ?? ''}
					enabled={p.checkSchedule.enabled}
					nextRun={scheduleOf('update_check')?.nextRun}
				/>
			{/snippet}
			{#snippet runSched()}
				<ScheduleSummary
					cron={p.runSchedule.cron ?? ''}
					timeZone={p.runSchedule.timeZone ?? ''}
					enabled={p.runSchedule.enabled}
					nextRun={scheduleOf('update_run')?.nextRun}
				/>
			{/snippet}

			<Card title="Recent runs" padding="none">
				{#if policyJobs.isPending}
					<div class="inset"><Skeleton lines={3} height="20px" /></div>
				{:else}
					<RunsTable {runs} label="Recent runs of {p.name}">
						{#snippet empty()}<EmptyState
								icon={Clock}
								color="slate"
								title="No runs yet."
								description="Checks and updates of this policy appear here, manual and scheduled."
								level={3}
								compact
							/>{/snippet}
					</RunsTable>
				{/if}
			</Card>

			<Dialog
				bind:open={previewOpen}
				title="Update preview"
				description="What updating {p.name} would change now. Applying pulls the new images and recreates the services that changed, dependencies first."
				size="xl"
				dismissible={!applying}
			>
				{#if drifted.length}
					<div class="notice">
						<Notice
							tone="warn"
							icon={TriangleAlert}
							title="Undeployed source changes"
							live="none"
						>
							Deploy the changes of {drifted
								.map((t) =>
									t.type === 'stack' ? stackName(t.id) || 'a stack' : t.id
								)
								.join(', ')} before updating; they are skipped until then.
						</Notice>
					</div>
				{/if}
				<Table
					label="Update preview of {p.name}"
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
				<p class="muted small">
					Hover an image for its digests. Only images whose digest changed are pulled.
				</p>
				{#snippet footer()}
					<Button
						variant="ghost"
						disabled={applying}
						onclick={() => (previewOpen = false)}>Close</Button
					>
					<Button
						variant="primary"
						loading={applying}
						disabled={!previewRows.some(
							(r) => r.item.eligible && r.item.status === 'update_available'
						)}
						onclick={apply}>Apply updates</Button
					>
				{/snippet}
			</Dialog>

			{#if editDialog.open}
				<UpdatePolicyDialog bind:open={editDialog.open} policy={p} />
			{/if}

			<DestructiveConfirm
				bind:open={deleteOpen}
				title="Delete update policy {p.name}"
				consequences={[
					'Removes the policy with its candidates, quarantine lists and update history.',
					'Nothing in its scope is checked or updated afterwards.',
					'Containers, images and your Compose files are not touched; the audit log and job history stay.'
				]}
				confirmText={p.name}
				confirmLabel="Delete policy"
				onconfirm={remove}
			/>
		{/snippet}
	</QueryView>
</Page>

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
