<script lang="ts">
	// Enrollment tokens (#3): pending ones with their expiry and the last
	// refused attempt, recently used ones with the agent they created. A
	// pending token can be revoked. Token values are never shown again.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrapEmpty, type AgentEnrollment } from '$lib/api/client';
	import { enrollmentsQuery } from '$lib/api/queries';
	import { liveKeys } from '$lib/live/keys';
	import { routes } from '$lib/routes';
	import {
		Button,
		Card,
		ConfirmDialog,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import { enrollmentIntent, enrollmentStateStatus } from './enrollment';

	let { now }: { now?: Date } = $props();

	const qc = useQueryClient();
	const enrollments = createQuery(() => enrollmentsQuery());
	// Tokens that can still enroll an agent (used ones became environments).
	const rows = $derived((enrollments.data ?? []).filter((e) => e.state === 'pending'));

	let revoking = $state<AgentEnrollment | null>(null);
	let confirmOpen = $state(false);

	async function revoke() {
		if (!revoking) return;
		const e = revoking;
		await unwrapEmpty(
			api.DELETE('/api/v1/agent-enrollments/{enrollmentId}', {
				params: { path: { enrollmentId: e.id } }
			})
		);
		await qc.invalidateQueries({ queryKey: liveKeys.list('agents') });
		toast.success('Revoked enrollment token', {
			body: 'An agent can no longer enroll with it.'
		});
	}

	const columns: Column<AgentEnrollment>[] = [
		{ id: 'name', header: 'For', cell: nameCell, stack: 'title' },
		{ id: 'state', header: 'State', cell: stateCell, width: '130px', stack: 'status' },
		{ id: 'expires', header: 'Expires', cell: expiresCell, width: '170px' },
		{
			id: 'actions',
			header: 'Actions',
			cell: actionsCell,
			hideHeader: true,
			width: '110px',
			align: 'end',
			stack: 'actions'
		}
	];
</script>

{#snippet nameCell(e: AgentEnrollment)}
	<div class="for">
		<span class="strong">{e.environmentName || 'Name from the agent'}</span>
		<span class="muted small">{enrollmentIntent(e.intent)}</span>
		{#if e.lastRejection && e.state === 'pending'}
			<span class="rejected small"
				>Refused {formatRelative(e.lastRejection.at, now)}: {e.lastRejection.message}</span
			>
		{/if}
		{#if e.state === 'used' && e.agentId}
			<a class="small" href={routes.environments()}
				>Enrolled {formatRelative(e.usedAt ?? e.createdAt, now)}</a
			>
		{/if}
	</div>
{/snippet}
{#snippet stateCell(e: AgentEnrollment)}
	<StatusBadge
		status={enrollmentStateStatus(e.state)}
		label={e.state === 'pending' ? 'Waiting' : undefined}
	/>
{/snippet}
{#snippet expiresCell(e: AgentEnrollment)}
	{#if e.state === 'pending'}
		<time datetime={e.expiresAt} title={formatDateTime(e.expiresAt)}
			>{formatRelative(e.expiresAt, now)}</time
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionsCell(e: AgentEnrollment)}
	{#if e.state === 'pending'}
		<Button
			size="sm"
			variant="ghost"
			onclick={() => {
				revoking = e;
				confirmOpen = true;
			}}>Revoke</Button
		>
	{/if}
{/snippet}

{#if rows.length}
	<Card title="Enrollment tokens" subtitle="Waiting for an agent" padding="none" id="enrollments">
		<Table label="Enrollment tokens" {rows} {columns} rowKey={(e) => e.id} manualSort />
	</Card>
{/if}

<ConfirmDialog
	bind:open={confirmOpen}
	title="Revoke enrollment token"
	message="The install command that contains this token stops working."
	consequences={[
		'An agent that has not enrolled yet cannot use this token.',
		'Agents that already enrolled keep working.'
	]}
	confirmLabel="Revoke token"
	tone="danger"
	onconfirm={revoke}
/>

<style>
	.for {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.strong {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.small {
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.rejected {
		color: var(--danger);
	}
</style>
