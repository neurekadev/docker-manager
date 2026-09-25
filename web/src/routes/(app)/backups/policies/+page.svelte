<script lang="ts">
	// Backup policies (#10): what is backed up, where, when and for how
	// long. Policies belong to the instance and keep running after their
	// creator is disabled or removed.
	import { createQuery } from '@tanstack/svelte-query';
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';
	import Plus from '@lucide/svelte/icons/plus';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import { Badge, Button, Card, EmptyState, Table, formatDateTime, type Column } from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import ScheduleSummary from '$lib/features/common/ScheduleSummary.svelte';
	import BackupsHeader from '$lib/features/backups/BackupsHeader.svelte';
	import {
		retentionText,
		scopeText,
		setState,
		type BackupPolicy
	} from '$lib/features/backups/model';
	import { backupPoliciesQuery, repositoriesQuery } from '$lib/features/backups/queries';

	usePage({
		title: 'Backup policies',
		crumbs: [{ label: 'Backups', href: routes.backups() }, { label: 'Policies' }]
	});

	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const policies = createQuery(() => backupPoliciesQuery());
	const repos = createQuery(() => repositoriesQuery());

	const columns: Column<BackupPolicy>[] = [
		{ id: 'name', header: 'Policy', cell: nameCell, sortValue: (p) => p.name, stack: 'title' },
		{ id: 'last', header: 'Last set', cell: lastCell, width: '220px', stack: 'status' },
		{ id: 'schedule', header: 'Schedule', cell: scheduleCell, width: '230px' },
		{ id: 'retention', header: 'Retention', cell: retentionCell }
	];
</script>

{#snippet nameCell(p: BackupPolicy)}
	<NameCell
		name={p.name}
		href={routes.backupPolicy(p.id)}
		sub="Backs up {scopeText(p)} to {repos.data?.find((r) => r.id === p.repositoryId)?.name ??
			'a repository'}"
	/>
{/snippet}
{#snippet lastCell(p: BackupPolicy)}
	{@const s = p.recentSets?.[0]}
	{#if s}
		{@const st = setState(s.state)}
		<Badge tone={st.tone} dot>{st.label}</Badge>
		<span class="muted num">{formatDateTime(s.startedAt)}</span>
	{:else}<span class="muted">Never run</span>{/if}
{/snippet}
{#snippet scheduleCell(p: BackupPolicy)}
	{#if p.schedule}<ScheduleSummary compact {...p.schedule} />{:else}<span class="muted">—</span
		>{/if}
{/snippet}
{#snippet retentionCell(p: BackupPolicy)}<span class="muted">{retentionText(p.retention)}</span
	>{/snippet}

<Page>
	<BackupsHeader>
		{#snippet actions()}
			{#if can(access, 'backup_policy.manage')}
				<Button variant="primary" icon={Plus} href={routes.backupPolicyNew()}
					>Create backup policy</Button
				>
			{/if}
		{/snippet}
	</BackupsHeader>
	<Card title="Policies" padding="none">
		<QueryView query={policies} errorTitle="The backup policies could not be loaded.">
			{#snippet children(rows)}
				<Table
					label="Backup policies"
					{rows}
					{columns}
					rowKey={(p) => p.id}
					sort={{ column: 'name', direction: 'asc' }}
				>
					{#snippet empty()}
						<EmptyState
							icon={CalendarClock}
							color="teal"
							title="No backup policies yet."
							description="A policy picks the manager state, stacks and volumes to back up, when, and how long to keep them."
							level={3}
							compact
						>
							{#snippet actions()}
								{#if can(access, 'backup_policy.manage')}
									<Button
										variant="primary"
										icon={Plus}
										href={routes.backupPolicyNew()}>Create backup policy</Button
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
