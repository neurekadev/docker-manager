<script lang="ts">
	// Activity (#22 stack tab): the stack's jobs (who started what, how it
	// ended; the hourly update checks hidden by default so deploys lead) and,
	// for holders of audit.read, its audit records with a job's queued,
	// started and finished records as one row, 50 at a time ("Load more").
	// Jobs refresh live (#23 job events); the audit list is re-read with
	// them.
	import { createQuery, keepPreviousData } from '@tanstack/svelte-query';
	import Activity from '@lucide/svelte/icons/activity';
	import type { Job } from '$lib/api/client';
	import { myPermissionsQuery, sessionQuery } from '$lib/api/queries';
	import { jobHeadline, stackNames } from '$lib/features/jobs/labels';
	import { auditRows, visibleJobs, type AuditRow } from '$lib/features/stacks/activity';
	import { useStackPage } from '$lib/features/stacks/context';
	import { canAnywhere, stackTitle } from '$lib/features/stacks/model';
	import { stackAuditQuery, stackJobsQuery } from '$lib/features/stacks/queries';
	import { routes } from '$lib/routes';
	import {
		Button,
		Card,
		EmptyState,
		ErrorState,
		Skeleton,
		StatusBadge,
		Switch,
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
	let auditPages = $state(1);
	const audit = createQuery(() => ({
		...stackAuditQuery(ctx.id, auditPages),
		enabled: canAudit,
		placeholderData: keepPreviousData
	}));
	const rows = $derived(auditRows(audit.data?.items ?? []));
	const session = createQuery(() => sessionQuery());
	const me = $derived(session.data?.user?.id);

	let hideChecks = $state(true);
	const listed = $derived(visibleJobs(jobs.data ?? [], hideChecks));
	const nameOf = $derived(stackNames([stack]));

	/** The job's kind first; its target only when it is not this stack. */
	function headline(j: Job): { title: string; subtitle: string } {
		const h = jobHeadline(j, { nameOf });
		return h.title === title || !h.subtitle
			? { title: h.subtitle || h.title, subtitle: '' }
			: h;
	}

	function who(j: Job): string {
		if (j.origin === 'scheduled') return 'Schedule';
		const mine = j.initiatorUserId && j.initiatorUserId === me;
		if (j.origin === 'api_token') return mine ? 'You (API token)' : 'API token';
		return mine ? 'You' : j.initiatorUserId ? 'Another user' : 'Docker Manager';
	}

	function actor(r: AuditRow): string {
		switch (r.actor.kind) {
			case 'user':
				return r.actor.userId === me ? 'You' : 'Another user';
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
			sortValue: (j) => headline(j).title,
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
		{ id: 'result', header: 'Result', cell: resultCell, maxWidth: '360px' }
	];
	const auditColumns: Column<AuditRow>[] = [
		{
			id: 'action',
			header: 'Action',
			cell: actionCell,
			sortValue: (r) => r.label,
			stack: 'title'
		},
		{
			id: 'outcome',
			header: 'Outcome',
			cell: outcomeCell,
			sortValue: (r) => r.status,
			stack: 'status',
			width: '130px'
		},
		{ id: 'actor', header: 'By', cell: actorCell, width: '150px' },
		{ id: 'at', header: 'When', cell: auditAtCell, sortValue: (r) => r.at, width: '150px' }
	];
</script>

{#snippet kindCell(j: Job)}
	{@const h = headline(j)}
	<a class="link" href={routes.job(j.id)}>{h.title}</a>
	{#if h.subtitle}<span class="muted"> {h.subtitle}</span>{/if}
	{#if j.progress?.message && !['succeeded', 'failed', 'partial', 'cancelled', 'interrupted'].includes(j.state)}
		<span class="muted"> {j.progress.message}</span>
	{/if}
{/snippet}
{#snippet stateCell(j: Job)}<StatusBadge status={j.state} kind="job" />{/snippet}
{#snippet whoCell(j: Job)}{who(j)}{/snippet}
{#snippet atCell(j: Job)}<time datetime={j.createdAt} title={formatDateTime(j.createdAt)}
		>{formatRelative(j.createdAt)}</time
	>{/snippet}
{#snippet resultCell(j: Job)}
	{#if j.error}
		<span class="err">{j.error.message}</span>
	{:else if j.items.length}
		<span class="muted num"
			>{j.items.filter((i) => i.status === 'succeeded').length} of {j.items.length} steps done</span
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionCell(r: AuditRow)}
	{#if r.jobId}
		<a class="link" href={routes.job(r.jobId)}>{r.label}</a>
	{:else}
		<span class="action">{r.label}</span>
	{/if}
{/snippet}
{#snippet outcomeCell(r: AuditRow)}
	<StatusBadge status={r.status} kind="job" label={r.denied ? 'Denied' : undefined} />
{/snippet}
{#snippet actorCell(r: AuditRow)}{actor(r)}{/snippet}
{#snippet auditAtCell(r: AuditRow)}<time datetime={r.at} title={formatDateTime(r.at)}
		>{formatRelative(r.at)}</time
	>{/snippet}

<Card title="Jobs" padding="none" id="jobs" subtitle="Newest first">
	{#snippet actions()}
		<Switch bind:checked={hideChecks} label="Hide update checks" />
	{/snippet}
	{#if jobs.isPending}
		<div class="pad" aria-busy="true"><Skeleton lines={5} height="20px" /></div>
	{:else if jobs.isError}
		<div class="pad">
			<ErrorState
				error={jobs.error}
				title="The jobs could not be loaded."
				onretry={() => jobs.refetch()}
				compact
				bare
			/>
		</div>
	{:else}
		<Table
			label="Jobs of {title}"
			rows={listed.shown}
			columns={jobColumns}
			rowKey={(j) => j.id}
		>
			{#snippet empty()}
				<EmptyState
					icon={Activity}
					color="violet"
					title={listed.hidden
						? `Only update checks so far.`
						: `No jobs for ${title} yet.`}
					description={listed.hidden
						? 'Turn off “Hide update checks” to see them.'
						: 'Deploys, restarts, updates and backups of this stack show up here.'}
					level={3}
					compact
				/>
			{/snippet}
		</Table>
		{#if listed.hidden && listed.shown.length}
			<p class="note muted">
				{listed.hidden}
				{listed.hidden === 1 ? 'update check is' : 'update checks are'} hidden.
			</p>
		{/if}
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
					bare
				/>
			</div>
		{:else}
			<Table
				label="Audit records of {title}"
				{rows}
				columns={auditColumns}
				rowKey={(r) => r.id}
			>
				{#snippet empty()}<p class="pad muted">
						No audit records for {title} yet.
					</p>{/snippet}
			</Table>
			{#if audit.data?.more}
				<div class="more">
					<Button size="sm" loading={audit.isFetching} onclick={() => (auditPages += 1)}
						>Load more</Button
					>
				</div>
			{/if}
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
		color: var(--text-strong);
	}

	.note {
		padding: var(--space-3) var(--space-5);
		border-top: 1px solid var(--border-subtle);
		font-size: var(--text-caption);
	}

	.more {
		display: flex;
		justify-content: center;
		padding: var(--space-3) var(--space-5);
		border-top: 1px solid var(--border-subtle);
	}
</style>
