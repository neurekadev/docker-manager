<script lang="ts">
	// Maintenance (#14): prune policies per environment. Nothing is pruned
	// on install: every rule and schedule starts off.
	import { createQuery } from '@tanstack/svelte-query';
	import Plus from '@lucide/svelte/icons/plus';
	import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
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
		PageHeader,
		StatusBadge,
		Table,
		formatRelative,
		type Column
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { environmentName } from '$lib/features/common/data';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import {
		enabledRules,
		runSummaryText,
		type MaintenancePolicy
	} from '$lib/features/maintenance/model';
	import { maintenancePoliciesQuery } from '$lib/features/maintenance/queries';

	usePage({ title: 'Maintenance', crumbs: [{ label: 'Maintenance' }], environmentScoped: true });

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const policies = createQuery(() => maintenancePoliciesQuery(environmentSelection.id));
	const envs = createQuery(() => environmentsQuery());

	const columns: Column<MaintenancePolicy>[] = [
		{ id: 'name', header: 'Policy', cell: nameCell, sortValue: (p) => p.name, stack: 'title' },
		{ id: 'rules', header: 'Rules on', cell: rulesCell, width: '120px', stack: 'status' },
		{
			id: 'env',
			header: 'Environment',
			cell: envCell,
			sortValue: (p) => environmentName(envs.data, p.environmentId),
			width: '140px'
		},
		{ id: 'schedule', header: 'Schedule', cell: scheduleCell, width: '230px' },
		{
			id: 'last',
			header: 'Last run',
			cell: lastCell,
			sortValue: (p) => p.lastRun?.finishedAt ?? ''
		}
	];
</script>

{#snippet nameCell(p: MaintenancePolicy)}
	<NameCell name={p.name} href={routes.maintenancePolicy(p.id)} sub={p.description} />
{/snippet}
{#snippet rulesCell(p: MaintenancePolicy)}
	{@const n = enabledRules(p).length}
	{#if p.view === 'full'}
		<Badge tone={n ? 'accent' : 'neutral'}>{n} of 7</Badge>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet envCell(p: MaintenancePolicy)}{p.scope === 'all'
		? 'All Environments'
		: environmentName(envs.data, p.environmentId)}{/snippet}
{#snippet scheduleCell(p: MaintenancePolicy)}
	{#if p.schedule}<ScheduleSummary compact {...p.schedule} />{:else}<span class="muted">—</span
		>{/if}
{/snippet}
{#snippet lastCell(p: MaintenancePolicy)}
	{#if p.lastRun}
		<div class="last">
			<StatusBadge status={p.lastRun.state} kind="job" />
			<span class="muted">{runSummaryText(p.lastRun)}</span>
			<span class="muted num" title={p.lastRun.finishedAt}
				>{formatRelative(p.lastRun.finishedAt)}</span
			>
		</div>
	{:else}<span class="muted">Never run</span>{/if}
{/snippet}

<Page>
	<PageHeader
		title="Maintenance"
		description="Prune what you no longer need, with a preview of every object first. Nothing is removed until you run a policy or turn its schedule on."
	>
		{#snippet actions()}
			{#if can(access, 'settings.read')}
				<Button icon={SlidersHorizontal} href={routes.maintenanceDefaults()}
					>Defaults</Button
				>
			{/if}
			{#if can(access, 'maintenance_policy.manage')}
				<Button variant="primary" icon={Plus} href={routes.maintenanceNew()}
					>Create maintenance policy</Button
				>
			{/if}
		{/snippet}
	</PageHeader>

	<Card title="Maintenance policies" padding="none">
		<QueryView query={policies} errorTitle="The maintenance policies could not be loaded.">
			{#snippet children(rows)}
				<Table
					label="Maintenance policies"
					{rows}
					{columns}
					rowKey={(p) => p.id}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}
						<EmptyState
							icon={Wrench}
							color="slate"
							title="No maintenance policies yet."
							description="Create one to clean up stopped containers, unused images, networks, volumes and build cache on an environment, with a preview before anything is removed."
							level={3}
							compact
						>
							{#snippet actions()}
								{#if can(access, 'maintenance_policy.manage')}
									<Button
										variant="primary"
										icon={Plus}
										href={routes.maintenanceNew()}
										>Create maintenance policy</Button
									>
								{/if}
							{/snippet}
						</EmptyState>
					{/snippet}
				</Table>
			{/snippet}
		</QueryView>
	</Card>
</Page>

<style>
	.last {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-caption);
	}
</style>
