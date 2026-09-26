<script lang="ts">
	// Updates (#20): what needs attention across every covered stack and
	// container first (updates available, failed checks or runs), then the
	// environment update policies. Creating a policy opens a dialog
	// (routes.updatePolicyNew() links here with it open).
	import { createQuery } from '@tanstack/svelte-query';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import Clock from '@lucide/svelte/icons/clock';
	import Layers from '@lucide/svelte/icons/layers';
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
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import PolicyTargetStatus from '$lib/features/updates/PolicyTargetStatus.svelte';
	import UpdatePolicyDialog from '$lib/features/updates/UpdatePolicyDialog.svelte';
	import { summarizeTargets, summaryState, type UpdatePolicy } from '$lib/features/updates/model';
	import {
		environmentUpdatePoliciesQuery,
		updatePoliciesQuery,
		type EnvironmentUpdatePolicy
	} from '$lib/features/updates/queries';

	usePage({ title: 'Updates', crumbs: [{ label: 'Updates' }], environmentScoped: true });
	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());
	const policies = createQuery(() => environmentUpdatePoliciesQuery());
	const targets = createQuery(() => updatePoliciesQuery(environmentSelection.id));
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
	const covered = $derived((targets.data ?? []).filter((t) => t.view === 'full'));
	const totals = $derived(summarizeTargets(covered.map((t) => t.summary)));
	const attention = $derived(
		covered.filter((t) => {
			const s = t.summary;
			return !!s && (s.available > 0 || s.failed + s.quarantined > 0);
		})
	);

	function targetName(t: UpdatePolicy): string {
		if (t.target.type === 'container') return t.target.id;
		const s = stacks.data?.find((x) => x.id === t.target.id);
		return s?.displayName || s?.name || t.name;
	}
	function targetHref(t: UpdatePolicy): string {
		return t.target.type === 'stack'
			? routes.stack(t.target.id, 'policies')
			: routes.container(t.environmentId, t.target.id);
	}

	const policyColumns: Column<EnvironmentUpdatePolicy>[] = [
		{ id: 'name', header: 'Policy', cell: nameCell, sortValue: (p) => p.name, stack: 'title' },
		{ id: 'status', header: 'Targets', cell: statusCell, width: '300px', stack: 'status' },
		{ id: 'check', header: 'Checks', cell: checkCell, width: '220px' },
		{ id: 'run', header: 'Automatic updates', cell: runCell, width: '220px' }
	];
	const attentionColumns: Column<UpdatePolicy>[] = [
		{
			id: 'target',
			header: 'Stack or container',
			cell: targetCell,
			sortValue: (t) => targetName(t),
			stack: 'title'
		},
		{
			id: 'env',
			header: 'Environment',
			cell: envCell,
			sortValue: (t) => envName(t.environmentId),
			width: '200px',
			stack: 'meta'
		},
		{ id: 'state', header: 'Status', cell: stateCell, width: '220px', stack: 'status' },
		{
			id: 'checked',
			header: 'Last check',
			cell: checkedCell,
			sortValue: (t) => t.summary?.lastCheckAt ?? '',
			width: '200px'
		}
	];
</script>

{#snippet nameCell(p: EnvironmentUpdatePolicy)}<NameCell
		name={p.name}
		href={routes.updatePolicy(p.id)}
		sub={p.scope === 'all' ? 'All environments' : envName(p.environmentId)}
	/>{/snippet}
{#snippet statusCell(p: EnvironmentUpdatePolicy)}<PolicyTargetStatus policyId={p.id} />{/snippet}
{#snippet checkCell(p: EnvironmentUpdatePolicy)}<ScheduleSummary
		compact
		cron={p.checkSchedule.cron ?? ''}
		timeZone={p.checkSchedule.timeZone ?? ''}
		enabled={p.checkSchedule.enabled}
	/>{/snippet}
{#snippet runCell(p: EnvironmentUpdatePolicy)}<ScheduleSummary
		compact
		cron={p.runSchedule.cron ?? ''}
		timeZone={p.runSchedule.timeZone ?? ''}
		enabled={p.runSchedule.enabled}
	/>{/snippet}
{#snippet targetCell(t: UpdatePolicy)}
	<span class="target">
		{#if t.target.type === 'stack'}<Layers size={14} aria-hidden="true" />{:else}<PackageCheck
				size={14}
				aria-hidden="true"
			/>{/if}
		<a href={targetHref(t)}>{targetName(t)}</a>
	</span>
{/snippet}
{#snippet envCell(t: UpdatePolicy)}{envName(t.environmentId)}{/snippet}
{#snippet stateCell(t: UpdatePolicy)}
	{@const st = summaryState(t.summary)}
	<Badge tone={st.tone} dot>{st.label}</Badge>
{/snippet}
{#snippet checkedCell(t: UpdatePolicy)}
	{#if t.summary?.lastCheckAt}<span class="num" title={formatDateTime(t.summary.lastCheckAt)}
			>{formatRelative(t.summary.lastCheckAt)}</span
		>{:else}<span class="muted">Never</span>{/if}
{/snippet}

<Page>
	<PageHeader
		title="Updates"
		description="Follow the digests behind your image tags and apply new images in dependency order. Checks never pull; updates run only when you apply them or turn automatic updates on."
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
					description="A policy covers all environments or one environment. It checks every managed stack and Docker Manager-managed container for new digests and, if you want, applies them on a schedule."
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
					secondary={totals.withUpdates
						? 'Stacks and containers to update'
						: 'Nothing waiting'}
					icon={PackageCheck}
					color="violet"
					tone={totals.withUpdates ? 'warn' : undefined}
				/>
				<KpiCard
					label="Failing"
					value={String(totals.failing)}
					secondary="Failed checks or quarantined digests"
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
						: 'Run a check from a policy'}
					icon={Clock}
					color="slate"
				/>
			</KpiRow>

			<Card
				title="Needs attention"
				subtitle="Covered stacks and containers with a new digest or a failed check or update. Open one to preview and apply its update."
				padding="none"
			>
				<Table
					label="Stacks and containers that need attention"
					rows={attention}
					columns={attentionColumns}
					rowKey={(t) => t.id}
					sort={{ column: 'target', direction: 'asc' }}
				>
					{#snippet empty()}<EmptyState
							icon={CircleCheck}
							color="green"
							title="Nothing needs attention."
							description={totals.unchecked
								? 'Some targets were never checked: run a check from their policy.'
								: 'Every covered stack and container runs the newest digest of its tag.'}
							level={3}
							compact
						/>{/snippet}
				</Table>
			</Card>

			<Card
				title="Policies"
				subtitle="Policies can't overlap: one for all environments, or one per environment."
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

<style>
	.target {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--text-strong);
	}
</style>
