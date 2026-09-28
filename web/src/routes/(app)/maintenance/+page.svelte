<script lang="ts">
	// Maintenance (#14): prune policies per environment, with what their last
	// runs reclaimed. Nothing is pruned on install: every rule and schedule
	// starts off. Creating a policy and the default rules open as dialogs
	// (routes.maintenanceNew() / maintenanceDefaults() link here with them
	// open).
	import { createQuery } from '@tanstack/svelte-query';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import Plus from '@lucide/svelte/icons/plus';
	import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Wrench from '@lucide/svelte/icons/wrench';
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
		StatusBadge,
		Table,
		formatBytes,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { environmentName } from '$lib/features/common/data';
	import { singleEnvironment } from '$lib/features/common/environments.svelte';
	import KpiRow from '$lib/features/common/KpiRow.svelte';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { urlDialog } from '$lib/features/common/urlDialog.svelte';
	import MaintenanceDefaultsDialog from '$lib/features/maintenance/MaintenanceDefaultsDialog.svelte';
	import MaintenancePolicyDialog from '$lib/features/maintenance/MaintenancePolicyDialog.svelte';
	import {
		enabledRules,
		lastRunTotals,
		rulesOnText,
		rulesText,
		runSummaryText,
		type MaintenancePolicy
	} from '$lib/features/maintenance/model';
	import { maintenancePoliciesQuery } from '$lib/features/maintenance/queries';

	usePage({ title: 'Maintenance', crumbs: [{ label: 'Maintenance' }], environmentScoped: true });

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const policies = createQuery(() => maintenancePoliciesQuery(environmentSelection.id));
	const envs = createQuery(() => environmentsQuery());
	const createDialog = urlDialog('create');
	const defaultsDialog = urlDialog('defaults');
	const canManage = $derived(can(access, 'maintenance_policy.manage'));
	const single = singleEnvironment();

	const list = $derived(policies.data ?? []);
	const totals = $derived(lastRunTotals(list));
	const scheduled = $derived(list.filter((p) => p.schedule?.enabled).length);

	const columns: Column<MaintenancePolicy>[] = $derived([
		{
			id: 'name',
			header: 'Policy',
			cell: nameCell,
			sortValue: (p) => p.name,
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
						sortValue: (p: MaintenancePolicy) =>
							environmentName(envs.data, p.environmentId),
						width: '160px',
						stack: 'meta'
					} satisfies Column<MaintenancePolicy>
				]),
		{ id: 'rules', header: 'Cleans', cell: rulesCell, stack: 'status' },
		{ id: 'schedule', header: 'Schedule', cell: scheduleCell, width: '220px', stack: 'meta' },
		{
			id: 'last',
			header: 'Last run',
			cell: lastCell,
			sortValue: (p) => p.lastRun?.finishedAt ?? '',
			width: '280px',
			stack: 'meta'
		}
	]);
</script>

{#snippet nameCell(p: MaintenancePolicy)}
	<NameCell name={p.name} href={routes.maintenancePolicy(p.id)} sub={p.description} />
{/snippet}
{#snippet rulesCell(p: MaintenancePolicy)}
	{#if p.view === 'full'}
		{@const n = enabledRules(p).length}
		<div class="rules">
			<Badge tone={n ? 'accent' : 'neutral'}>{rulesOnText(p)}</Badge>
			<span class="muted">{rulesText(p)}</span>
		</div>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet envCell(p: MaintenancePolicy)}{p.scope === 'all'
		? 'All environments'
		: environmentName(envs.data, p.environmentId)}{/snippet}
{#snippet scheduleCell(p: MaintenancePolicy)}
	{#if p.schedule}<ScheduleSummary compact {...p.schedule} />{:else}<span class="muted">—</span
		>{/if}
{/snippet}
{#snippet lastCell(p: MaintenancePolicy)}
	{#if p.lastRun}
		<div class="last">
			<span class="last-head">
				<StatusBadge status={p.lastRun.state} kind="job" />
				<span class="muted num" title={formatDateTime(p.lastRun.finishedAt)}
					>{formatRelative(p.lastRun.finishedAt)}</span
				>
			</span>
			<span class="muted">{runSummaryText(p.lastRun)}</span>
		</div>
	{:else}<span class="muted">Never run</span>{/if}
{/snippet}

<Page>
	<PageHeader
		title="Maintenance"
		description="Clean up Docker objects you no longer need, with a preview of everything first."
	>
		{#snippet actions()}
			{#if can(access, 'settings.read')}
				<Button icon={SlidersHorizontal} onclick={() => (defaultsDialog.open = true)}
					>Default rules</Button
				>
			{/if}
			{#if canManage && list.length > 0}
				<Button variant="primary" icon={Plus} onclick={() => (createDialog.open = true)}
					>Create maintenance policy</Button
				>
			{/if}
		{/snippet}
	</PageHeader>

	<QueryView query={policies} errorTitle="The maintenance policies could not be loaded.">
		{#if list.length === 0}
			<Card>
				<EmptyState
					icon={Wrench}
					color="slate"
					title="No maintenance policies yet."
					description="A policy removes stopped containers, unused images and other leftovers on a schedule. You see a preview first, and nothing runs until you turn it on."
					level={2}
				>
					{#snippet actions()}
						{#if canManage}
							<Button
								variant="primary"
								icon={Plus}
								onclick={() => (createDialog.open = true)}
								>Create maintenance policy</Button
							>
						{/if}
					{/snippet}
				</EmptyState>
			</Card>
		{:else}
			<KpiRow>
				<KpiCard
					label="Policies"
					value={String(list.length)}
					secondary="{scheduled} on a schedule"
					icon={CalendarClock}
					color="slate"
				/>
				<KpiCard
					label="Last run"
					value={totals.latest ? formatRelative(totals.latest.finishedAt) : 'Never'}
					secondary={totals.latest
						? formatDateTime(totals.latest.finishedAt)
						: 'Preview and run a policy'}
					icon={Wrench}
					color="slate"
					tone={totals.latest
						? totals.latest.state === 'succeeded'
							? 'ok'
							: 'warn'
						: undefined}
				/>
				<KpiCard
					label="Removed"
					value={String(totals.removed)}
					secondary={totals.failed
						? `${totals.failed} failed in the last runs`
						: 'Objects in the last runs'}
					icon={Trash2}
					color="rose"
					tone={totals.failed ? 'warn' : undefined}
				/>
				<KpiCard
					label="Space reclaimed"
					value={formatBytes(totals.bytes)}
					secondary="By the last run of each policy"
					icon={HardDrive}
					color="green"
				/>
			</KpiRow>

			<Card title="Policies" padding="none">
				<Table
					label="Maintenance policies"
					rows={list}
					{columns}
					rowKey={(p) => p.id}
					sort={{ column: 'name', direction: 'asc' }}
				/>
			</Card>
		{/if}
	</QueryView>
</Page>

{#if createDialog.open}
	<MaintenancePolicyDialog
		bind:open={createDialog.open}
		environmentId={environmentSelection.id}
		allowAll={can(access, 'maintenance_policy.manage_all')}
	/>
{/if}
{#if defaultsDialog.open}
	<MaintenanceDefaultsDialog
		bind:open={defaultsDialog.open}
		canEdit={can(access, 'settings.manage')}
	/>
{/if}

<style>
	.rules {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-caption);
	}

	.last {
		display: grid;
		gap: 2px;
		font-size: var(--text-caption);
	}

	.last-head {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
	}
</style>
