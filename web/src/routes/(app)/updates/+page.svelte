<script lang="ts">
	// Updates (#20): policies that follow the digest behind a stack's or a
	// managed container's existing tag. Lists filter by the selected
	// environment; creating needs update_policy.manage.
	import { createQuery } from '@tanstack/svelte-query';
	import PackageCheck from '@lucide/svelte/icons/package-check';
	import Plus from '@lucide/svelte/icons/plus';
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
		Table,
		formatRelative,
		type Column
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { environmentName, stacksQuery } from '$lib/features/common/data';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import { summaryText, type UpdatePolicy } from '$lib/features/updates/model';
	import { updatePoliciesQuery } from '$lib/features/updates/queries';

	usePage({ title: 'Updates', crumbs: [{ label: 'Updates' }], environmentScoped: true });

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const policies = createQuery(() => updatePoliciesQuery(environmentSelection.id));
	const envs = createQuery(() => environmentsQuery());
	const stacks = createQuery(() => stacksQuery());

	function targetName(p: UpdatePolicy): string {
		if (p.target.type === 'container') return p.target.id;
		const s = stacks.data?.find((x) => x.id === p.target.id);
		return s?.displayName || s?.name || 'Stack';
	}

	const columns: Column<UpdatePolicy>[] = [
		{ id: 'name', header: 'Policy', cell: nameCell, sortValue: (p) => p.name, stack: 'title' },
		{
			id: 'status',
			header: 'Status',
			cell: statusCell,
			sortValue: (p) => summaryText(p).text,
			stack: 'status',
			width: '190px'
		},
		{
			id: 'env',
			header: 'Environment',
			cell: envCell,
			sortValue: (p) => environmentName(envs.data, p.environmentId),
			width: '140px'
		},
		{ id: 'check', header: 'Checks', cell: checkCell, width: '220px' },
		{ id: 'run', header: 'Automatic updates', cell: runCell, width: '220px' },
		{
			id: 'last',
			header: 'Last check',
			cell: lastCell,
			sortValue: (p) => p.summary?.lastCheckAt ?? '',
			width: '130px'
		}
	];
</script>

{#snippet nameCell(p: UpdatePolicy)}
	<NameCell
		name={p.name}
		href={routes.updatePolicy(p.id)}
		sub="{p.target.type === 'stack' ? 'Stack' : 'Container'} {targetName(p)}"
	/>
{/snippet}
{#snippet statusCell(p: UpdatePolicy)}
	{@const s = summaryText(p)}
	<Badge tone={s.tone} dot>{s.text}</Badge>
{/snippet}
{#snippet envCell(p: UpdatePolicy)}{environmentName(envs.data, p.environmentId)}{/snippet}
{#snippet checkCell(p: UpdatePolicy)}
	{#if p.checkSchedule}
		<ScheduleSummary compact {...p.checkSchedule} />
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet runCell(p: UpdatePolicy)}
	{#if p.runSchedule}
		<ScheduleSummary compact {...p.runSchedule} />
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet lastCell(p: UpdatePolicy)}
	{#if p.summary?.lastCheckAt}
		<span class="num" title={p.summary.lastCheckAt}
			>{formatRelative(p.summary.lastCheckAt)}</span
		>
	{:else}<span class="muted">Never</span>{/if}
{/snippet}

<Page>
	<PageHeader
		title="Updates"
		description="Follow new image digests behind the tags you already use. DockYard never edits your Compose files."
	>
		{#snippet actions()}
			{#if can(access, 'update_policy.manage')}
				<Button variant="primary" icon={Plus} href={routes.updatePolicyNew()}
					>Create update policy</Button
				>
			{/if}
		{/snippet}
	</PageHeader>

	<Card title="Update policies" padding="none">
		<QueryView query={policies} errorTitle="The update policies could not be loaded.">
			{#snippet children(rows)}
				<Table
					label="Update policies"
					{rows}
					{columns}
					rowKey={(p) => p.id}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}
						<EmptyState
							icon={PackageCheck}
							color="violet"
							title="No update policies yet."
							description="Create one to follow new digests for a stack or a DockYard-managed container. Nothing is checked or updated until you turn its schedules on."
							level={3}
							compact
						>
							{#snippet actions()}
								{#if can(access, 'update_policy.manage')}
									<Button
										variant="primary"
										icon={Plus}
										href={routes.updatePolicyNew()}>Create update policy</Button
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
