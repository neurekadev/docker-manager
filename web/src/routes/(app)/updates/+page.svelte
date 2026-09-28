<script lang="ts">
	// Updates (#20): what needs attention across every covered stack and
	// container first (updates available, failed checks or runs), each with
	// a preview of its update, then the environment update policies.
	// Everything here counts the covered (active) targets of the policies,
	// like the policy pages. Creating a policy opens a dialog
	// (routes.updatePolicyNew() links here with it open).
	import { createQueries, createQuery } from '@tanstack/svelte-query';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Clock from '@lucide/svelte/icons/clock';
	import Eye from '@lucide/svelte/icons/eye';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import Plus from '@lucide/svelte/icons/plus';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { environmentSelection } from '$lib/shell/environment.svelte';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		KpiCard,
		PageHeader,
		Table,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { environmentName, stacksQuery } from '$lib/features/common/data';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import PolicyTargetStatus from '$lib/features/updates/PolicyTargetStatus.svelte';
	import TargetName from '$lib/features/updates/TargetName.svelte';
	import UpdatePolicyDialog from '$lib/features/updates/UpdatePolicyDialog.svelte';
	import UpdatePreviewDialog from '$lib/features/updates/UpdatePreviewDialog.svelte';
	import {
		coveredTargets,
		policySchedulesText,
		summarizeTargets,
		summaryState,
		targetsUpdateText,
		type UpdatePolicy
	} from '$lib/features/updates/model';
	import {
		environmentUpdatePoliciesQuery,
		environmentUpdateTargetsQuery,
		updatePoliciesQuery,
		type EnvironmentTarget,
		type EnvironmentUpdatePolicy
	} from '$lib/features/updates/queries';

	usePage({ title: 'Updates', crumbs: [{ label: 'Updates' }], environmentScoped: true });
	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	const single = singleEnvironment();
	const policies = createQuery(() => environmentUpdatePoliciesQuery());
	// The per-target policies (candidate summaries, actions) for previews.
	const targetPolicies = createQuery(() => updatePoliciesQuery(environmentSelection.id));
	const createDialog = urlDialog('create');
	const canManage = $derived(can(access, 'update_policy.manage'));
	const envName = (id?: string) => environmentName(envs.data, id ?? '');

	const visiblePolicies = $derived(
		(policies.data ?? []).filter(
			(p) =>
				!environmentSelection.id ||
				p.scope === 'all' ||
				p.environmentId === environmentSelection.id
		)
	);
	const targetLists = createQueries(() => ({
		queries: visiblePolicies.map((p) => environmentUpdateTargetsQuery(p.id))
	}));
	// Covered targets of the visible policies, in the selected environment.
	const covered = $derived(
		coveredTargets(
			targetLists.map((q) => q.data),
			environmentSelection.id
		)
	);
	const totals = $derived(summarizeTargets(covered.map((t) => t.candidateSummary)));
	const attention = $derived(
		covered.filter((t) => {
			const s = t.candidateSummary;
			return s.available > 0 || s.failed + s.quarantined > 0;
		})
	);
	const byId = $derived(new Map((targetPolicies.data ?? []).map((p) => [p.id, p])));

	function targetName(t: EnvironmentTarget): string {
		if (t.type === 'container') return t.id;
		const s = stacks.data?.find((x) => x.id === t.id);
		return s?.displayName || s?.name || '';
	}

	let previewOf = $state<{ policy: UpdatePolicy; name: string } | null>(null);
	let previewOpen = $state(false);
	function preview(t: EnvironmentTarget, p: UpdatePolicy) {
		previewOf = { policy: p, name: targetName(t) || 'this target' };
		previewOpen = true;
	}

	const policyColumns: Column<EnvironmentUpdatePolicy>[] = $derived([
		{
			id: 'name',
			header: 'Policy',
			cell: nameCell,
			sortValue: (p) => p.name,
			maxWidth: '320px',
			stack: 'title'
		},
		{ id: 'status', header: 'Targets', cell: statusCell, width: '320px', stack: 'status' },
		{ id: 'schedule', header: 'Schedule', cell: scheduleCell, stack: 'meta' }
	]);
	const attentionColumns: Column<EnvironmentTarget>[] = $derived([
		{
			id: 'target',
			header: 'Stack or container',
			cell: targetCell,
			sortValue: (t) => targetName(t),
			maxWidth: '320px',
			stack: 'title'
		},
		...(single.current
			? []
			: [
					{
						id: 'env',
						header: 'Environment',
						cell: envCell,
						sortValue: (t: EnvironmentTarget) => envName(t.environmentId),
						width: '180px',
						stack: 'meta'
					} satisfies Column<EnvironmentTarget>
				]),
		{ id: 'state', header: 'Status', cell: stateCell, width: '200px', stack: 'status' },
		{
			id: 'checked',
			header: 'Last check',
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
</script>

{#snippet nameCell(p: EnvironmentUpdatePolicy)}<NameCell
		name={p.name}
		href={routes.updatePolicy(p.id)}
		sub={p.scope === 'all' ? 'All environments' : envName(p.environmentId)}
	/>{/snippet}
{#snippet statusCell(p: EnvironmentUpdatePolicy)}<PolicyTargetStatus
		policyId={p.id}
		environmentId={environmentSelection.id}
	/>{/snippet}
{#snippet scheduleCell(p: EnvironmentUpdatePolicy)}
	<span
		title="Checks: {p.checkSchedule.cron} ({p.checkSchedule.timeZone})&#10;Updates: {p
			.runSchedule.cron} ({p.runSchedule.timeZone})"
		>{policySchedulesText(p.checkSchedule, p.runSchedule)}</span
	>
{/snippet}
{#snippet targetCell(t: EnvironmentTarget)}<TargetName
		type={t.type}
		id={t.id}
		environmentId={t.environmentId}
	/>{/snippet}
{#snippet envCell(t: EnvironmentTarget)}{envName(t.environmentId)}{/snippet}
{#snippet stateCell(t: EnvironmentTarget)}
	{@const st = summaryState(t.candidateSummary)}
	<Badge tone={st.tone} dot>{st.label}</Badge>
{/snippet}
{#snippet checkedCell(t: EnvironmentTarget)}
	{#if t.candidateSummary.lastCheckAt}<span
			class="num"
			title={formatDateTime(t.candidateSummary.lastCheckAt)}
			>{formatRelative(t.candidateSummary.lastCheckAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}
{#snippet actionsCell(t: EnvironmentTarget)}
	{@const p = byId.get(t.policyId)}
	{#if p && p.view === 'full'}
		<Button size="sm" variant="secondary" icon={Eye} onclick={() => preview(t, p)}
			>Preview update</Button
		>
	{/if}
{/snippet}

<Page>
	<PageHeader
		title="Updates"
		description="Checks for newer images of the tags you use and updates your stacks and containers when you choose."
	>
		{#snippet actions()}{#if canManage}<Button
					variant="primary"
					icon={Plus}
					onclick={() => (createDialog.open = true)}>Create update policy</Button
				>{/if}{/snippet}
	</PageHeader>

	<QueryView query={policies} errorTitle="The update policies could not be loaded.">
		{#if visiblePolicies.length === 0}
			<Card>
				<EmptyState
					icon={PackageCheck}
					color="violet"
					title="No update policies yet."
					description="A policy looks for newer images of your stacks and the containers Docker Manager created, on one environment or all of them, and can apply them on a schedule."
					level={2}
				>
					{#snippet actions()}
						{#if canManage}<Button
								variant="primary"
								icon={Plus}
								onclick={() => (createDialog.open = true)}
								>Create update policy</Button
							>{/if}
					{/snippet}
				</EmptyState>
			</Card>
		{:else}
			<KpiRow>
				<KpiCard
					label="Updates available"
					value={String(totals.withUpdates)}
					secondary={targetsUpdateText(covered)}
					icon={PackageCheck}
					color="violet"
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
					label="Up to date"
					value={String(totals.upToDate)}
					secondary={totals.unchecked
						? `${totals.unchecked} not checked yet`
						: 'All checked'}
					icon={CircleCheck}
					color="green"
					tone={totals.upToDate ? 'ok' : undefined}
				/>
				<KpiCard
					label="Last check"
					value={totals.lastCheckAt ? formatRelative(totals.lastCheckAt) : 'Never'}
					secondary={totals.lastCheckAt
						? formatDateTime(totals.lastCheckAt)
						: 'Check a policy to start'}
					icon={Clock}
					color="slate"
				/>
			</KpiRow>

			<Card
				title="Needs attention"
				subtitle="Stacks and containers with a newer image or a failed check or update."
				padding="none"
			>
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
								? 'Some stacks or containers were never checked. Check their policy to see if they have updates.'
								: 'Every covered stack and container runs the newest image of its tag.'}
							level={3}
							compact
						/>{/snippet}
				</Table>
			</Card>

			<Card
				title="Policies"
				subtitle="One policy covers all environments, or one per environment."
				padding="none"
			>
				<Table
					label="Update policies"
					rows={visiblePolicies}
					columns={policyColumns}
					rowKey={(p) => p.id}
					sort={{ column: 'name', direction: 'asc' }}
				/>
			</Card>
		{/if}
	</QueryView>
</Page>

{#if createDialog.open}
	<UpdatePolicyDialog
		bind:open={createDialog.open}
		environmentId={environmentSelection.id}
		allowAll={can(access, 'update_policy.manage_all')}
	/>
{/if}
{#if previewOf}
	{#key previewOf.policy.id}
		<UpdatePreviewDialog
			bind:open={previewOpen}
			policy={previewOf.policy}
			name={previewOf.name}
			canRun={previewOf.policy.actions.includes('update.run')}
		/>
	{/key}
{/if}
