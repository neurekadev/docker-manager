<script lang="ts">
	import { createQuery } from '@tanstack/svelte-query';
	import Plus from '@lucide/svelte/icons/plus';
	import { environmentsQuery, myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Button, Card, EmptyState, PageHeader, Table, type Column } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { environmentName } from '$lib/features/common/data';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import {
		environmentUpdatePoliciesQuery,
		type EnvironmentUpdatePolicy
	} from '$lib/features/updates/queries';

	usePage({ title: 'Updates', crumbs: [{ label: 'Updates' }] });
	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const envs = createQuery(() => environmentsQuery());
	const policies = createQuery(() => environmentUpdatePoliciesQuery());
	const columns: Column<EnvironmentUpdatePolicy>[] = [
		{ id: 'name', header: 'Policy', cell: nameCell, sortValue: (p) => p.name, stack: 'title' },
		{
			id: 'scope',
			header: 'Scope',
			cell: scopeCell,
			sortValue: (p) => p.environmentId || '',
			width: '200px'
		},
		{ id: 'check', header: 'Checks', cell: checkCell, width: '220px' },
		{ id: 'run', header: 'Automatic updates', cell: runCell, width: '220px' }
	];
</script>

{#snippet nameCell(p: EnvironmentUpdatePolicy)}<NameCell
		name={p.name}
		href={routes.updatePolicy(p.id)}
	/>{/snippet}
{#snippet scopeCell(p: EnvironmentUpdatePolicy)}{p.scope === 'all'
		? 'All Environments'
		: environmentName(envs.data, p.environmentId)}{/snippet}
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

<Page>
	<PageHeader
		title="Updates"
		description="Check and apply image updates across all environments or one environment. Policies cannot overlap."
	>
		{#snippet actions()}{#if can(access, 'update_policy.manage')}<Button
					variant="primary"
					icon={Plus}
					href={routes.updatePolicyNew()}>Create update policy</Button
				>{/if}{/snippet}
	</PageHeader>
	<Card title="Environment update policies" padding="none">
		<QueryView query={policies} errorTitle="The update policies could not be loaded.">
			{#snippet children(rows)}
				<Table
					label="Update policies"
					{rows}
					{columns}
					rowKey={(p) => p.id}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}<EmptyState
							title="No update policies yet."
							description="Create a policy for All Environments or a Single Environment."
							level={3}
							compact
						/>{/snippet}
				</Table>
			{/snippet}
		</QueryView>
	</Card>
</Page>
