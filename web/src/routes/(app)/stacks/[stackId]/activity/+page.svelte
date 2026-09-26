<script lang="ts">
	// Activity (#22 stack tab): the stack's jobs (who started what, how it
	// ended) and, for holders of audit.read, its audit records. Jobs refresh
	// live (#23 job events); the audit list is re-read with them.
	import { createQuery } from '@tanstack/svelte-query';
	import Activity from '@lucide/svelte/icons/activity';
	import type { Job } from '$lib/api/client';
	import { myPermissionsQuery, sessionQuery } from '$lib/api/queries';
	import { useStackPage } from '$lib/features/stacks/context';
	import {
		auditActionLabel,
		canAnywhere,
		jobKindLabel,
		stackTitle
	} from '$lib/features/stacks/model';
	import { stackAuditQuery, stackJobsQuery, type AuditEvent } from '$lib/features/stacks/queries';
	import { routes } from '$lib/routes';
	import {
		Card,
		EmptyState,
		ErrorState,
		Skeleton,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		type Column
	} from '$lib/ui';

	const ctx = useStackPage();
	const stack = $derived(ctx.stack!);
	const title = $derived(stackTitle(stack));
	const jobs = createQuery(() => stackJobsQuery(ctx.id));
	const perms = createQuery(() => myPermissionsQuery());
	const canAudit = $derived(canAnywhere(perms.data, 'audit.read'));
	const audit = createQuery(() => ({ ...stackAuditQuery(ctx.id), enabled: canAudit }));
	const session = createQuery(() => sessionQuery());
	const me = $derived(session.data?.user?.id);

	function who(j: Job): string {
		if (j.origin === 'scheduled') return 'Schedule';
		const mine = j.initiatorUserId && j.initiatorUserId === me;
		if (j.origin === 'api_token') return mine ? 'You (API token)' : 'API token';
		return mine ? 'You' : j.initiatorUserId ? 'Another user' : 'Docker Manager';
	}

	function actor(e: AuditEvent): string {
		switch (e.actor.kind) {
			case 'user':
				return e.actor.userId === me ? 'You' : 'Another user';
			case 'api_token':
				return 'API token';
			case 'service':
				return 'Docker Manager';
			case 'agent':
				return 'Agent';
		}
		return 'Anonymous';
	}

	const jobColumns: Column<Job>[] = [
		{
			id: 'kind',
			header: 'Job',
			cell: kindCell,
			sortValue: (j) => jobKindLabel(j.kind),
			stack: 'title'
		},
		{
			id: 'state',
			header: 'State',
			cell: stateCell,
			sortValue: (j) => j.state,
			stack: 'status',
			width: '150px'
		},
		{ id: 'who', header: 'Started by', cell: whoCell, width: '150px' },
		{
			id: 'at',
			header: 'Started',
			cell: atCell,
			sortValue: (j) => j.createdAt,
			width: '150px'
		},
		{ id: 'result', header: 'Result', cell: resultCell }
	];
	const auditColumns: Column<AuditEvent>[] = [
		{
			id: 'action',
			header: 'Action',
			cell: actionCell,
			sortValue: (e) => e.action,
			stack: 'title'
		},
		{
			id: 'outcome',
			header: 'Outcome',
			cell: outcomeCell,
			sortValue: (e) => e.outcome,
			stack: 'status',
			width: '130px'
		},
		{ id: 'actor', header: 'By', cell: actorCell, width: '150px' },
		{ id: 'at', header: 'When', cell: auditAtCell, sortValue: (e) => e.at, width: '150px' }
	];
	const OUTCOME: Record<string, string> = {
		success: 'succeeded',
		partial: 'partial',
		failure: 'failed',
		denied: 'failed',
		error: 'failed'
	};
</script>

{#snippet kindCell(j: Job)}
	<a class="link" href={routes.job(j.id)}>{jobKindLabel(j.kind)}</a>
	{#if j.progress?.message && !['succeeded', 'failed', 'partial', 'cancelled', 'interrupted'].includes(j.state)}
		<span class="muted"> {j.progress.message}</span>
	{/if}
{/snippet}
{#snippet stateCell(j: Job)}<StatusBadge status={j.state} kind="job" />{/snippet}
{#snippet whoCell(j: Job)}<span title={j.initiatorUserId}>{who(j)}</span>{/snippet}
{#snippet atCell(j: Job)}<span title={formatDateTime(j.createdAt)}
		>{formatRelative(j.createdAt)}</span
	>{/snippet}
{#snippet resultCell(j: Job)}
	{#if j.error}
		<span class="err">{j.error.message}</span>
	{:else if j.items.length}
		<span class="muted num"
			>{j.items.filter((i) => i.status === 'succeeded').length} of {j.items.length} items done</span
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionCell(e: AuditEvent)}
	<span class="action">{auditActionLabel(e.action)}</span>
	<span class="mono muted">{e.action}</span>
{/snippet}
{#snippet outcomeCell(e: AuditEvent)}
	<StatusBadge
		status={OUTCOME[e.outcome] ?? e.outcome}
		kind="job"
		label={e.outcome === 'denied' ? 'Denied' : undefined}
	/>
{/snippet}
{#snippet actorCell(e: AuditEvent)}{actor(e)}{/snippet}
{#snippet auditAtCell(e: AuditEvent)}<span title={formatDateTime(e.at)}>{formatRelative(e.at)}</span
	>{/snippet}

<Card title="Jobs" padding="none" id="jobs" subtitle="Newest first">
	{#if jobs.isPending}
		<div class="pad" aria-busy="true"><Skeleton lines={5} height="20px" /></div>
	{:else if jobs.isError}
		<div class="pad">
			<ErrorState
				error={jobs.error}
				title="The jobs could not be loaded."
				onretry={() => jobs.refetch()}
				compact
			/>
		</div>
	{:else}
		<Table label="Jobs of {title}" rows={jobs.data} columns={jobColumns} rowKey={(j) => j.id}>
			{#snippet empty()}
				<EmptyState
					icon={Activity}
					color="violet"
					title="No jobs for {title} yet."
					description="Deploys, restarts, updates and backups of this stack show up here."
					level={3}
					compact
				/>
			{/snippet}
		</Table>
	{/if}
</Card>

{#if canAudit}
	<Card
		title="Audit log"
		padding="none"
		id="audit"
		subtitle="Every recorded action touching this stack"
	>
		{#if audit.isPending}
			<div class="pad" aria-busy="true"><Skeleton lines={4} height="20px" /></div>
		{:else if audit.isError}
			<div class="pad">
				<ErrorState
					error={audit.error}
					title="The audit log could not be loaded."
					onretry={() => audit.refetch()}
					compact
				/>
			</div>
		{:else}
			<Table
				label="Audit records of {title}"
				rows={audit.data}
				columns={auditColumns}
				rowKey={(e) => e.id}
			>
				{#snippet empty()}<p class="pad muted">
						No audit records for {title} yet.
					</p>{/snippet}
			</Table>
		{/if}
	</Card>
{/if}

<style>
	.pad {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}

	.link {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.err {
		color: var(--danger);
	}

	.action {
		margin-right: var(--space-2);
		color: var(--text-strong);
	}
</style>
