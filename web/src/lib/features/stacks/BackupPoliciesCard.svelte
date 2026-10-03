<script lang="ts">
	// Backup policies that include this stack (#10): read-only here; they
	// are system policies across environments, edited on the Backups page.
	import { createQuery } from '@tanstack/svelte-query';
	import DatabaseBackup from '@lucide/svelte/icons/database-backup';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Card,
		EmptyState,
		ErrorState,
		Skeleton,
		StatusBadge,
		formatDateTime,
		formatRelative
	} from '$lib/ui';
	import { stackTitle } from './model';
	import { backupPoliciesQuery, type Stack } from './queries';

	interface Props {
		stack: Stack;
	}

	let { stack }: Props = $props();
	const title = $derived(stackTitle(stack));
	const policies = createQuery(() => backupPoliciesQuery());
	const mine = $derived(
		(policies.data ?? []).filter(
			(p) =>
				(p.scope === 'all' || p.environmentId === stack.environmentId) &&
				!(p.excludeStacks ?? []).includes(stack.id)
		)
	);
	const hidden = $derived((policies.data ?? []).some((p) => p.view === 'minimal'));
</script>

<Card title="Backups" id="backups">
	{#snippet actions()}
		<Button size="sm" href={routes.backups()}>Open Backups</Button>
	{/snippet}
	{#if policies.isPending}
		<div aria-busy="true"><Skeleton lines={3} /></div>
	{:else if policies.isError}
		<ErrorState
			error={policies.error}
			title="The backup policies could not be loaded."
			onretry={() => policies.refetch()}
			compact
		/>
	{:else if mine.length === 0}
		<EmptyState
			icon={DatabaseBackup}
			color="teal"
			title="No backup policy includes {title}."
			description={hidden
				? 'Policies you can only see by name are not checked here.'
				: 'Create an environment backup policy or remove this stack from its exclusions.'}
			level={3}
			compact
		/>
	{:else}
		<ul class="list" role="list">
			{#each mine as p (p.id)}
				{@const last = p.recentSets?.[0]}
				<li class="item">
					<div class="head">
						<a class="name" href={routes.backupPolicy(p.id)}>{p.name}</a>
						{#if p.enabled}<Badge tone="ok" dot>Scheduled</Badge>{:else}<Badge
								>Manual Only</Badge
							>{/if}
					</div>
					<dl class="facts">
						{#if p.schedule}
							<dt>Schedule</dt>
							<dd class="mono">
								{p.schedule.cron} <span class="muted">({p.schedule.timeZone})</span>
							</dd>
							{#if p.enabled && p.schedule.nextRun}
								<dt>Next Run</dt>
								<dd>{formatDateTime(p.schedule.nextRun, p.schedule.timeZone)}</dd>
							{/if}
						{/if}
						<dt>Includes</dt>
						<dd>
							The stack's project directory and named volumes{p.anonymousVolumes
								? ', with its anonymous volumes'
								: ''}{(p.excludeVolumes ?? []).length
								? ', except the volumes the policy leaves out'
								: ''}
						</dd>
						{#if p.shutdown}
							<dt>During Backups</dt>
							<dd>Containers are stopped and started again</dd>
						{/if}
						{#if last}
							<dt>Last Backup</dt>
							<dd>
								<StatusBadge
									status={last.state === 'complete' ? 'succeeded' : last.state}
									kind="job"
								/>
								<span class="muted">{formatRelative(last.startedAt)}</span>
							</dd>
						{/if}
					</dl>
				</li>
			{/each}
		</ul>
	{/if}
</Card>

<style>
	.list {
		display: grid;
		gap: var(--space-3);
	}

	.item {
		display: grid;
		gap: var(--space-2);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-2);
	}

	.name {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.facts {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 4px var(--space-4);
		margin: 0;
	}

	dt {
		color: var(--text-muted);
	}

	dd {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin: 0;
	}

	@media (max-width: 599px) {
		.facts {
			grid-template-columns: 1fr;
		}
	}
</style>
