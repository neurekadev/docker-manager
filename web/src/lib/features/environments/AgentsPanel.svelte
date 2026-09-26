<script lang="ts">
	// The agents of one environment (#3, #34): the active one and revoked
	// predecessors. Rotate the active agent's credential (the old one stays
	// valid until the agent confirms the new one) or remove it (revokes the
	// credential; the environment stays offline and detached until a new
	// agent re-attaches it). Actions follow each agent's `actions`.
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { api, unwrap, unwrapEmpty, type Agent, type Environment } from '$lib/api/client';
	import { environmentAgentsQuery } from '$lib/api/queries';
	import { liveKeys } from '$lib/live/keys';
	import { routes } from '$lib/routes';
	import {
		Badge,
		Button,
		Card,
		ConfirmDialog,
		DestructiveConfirm,
		EmptyState,
		ErrorState,
		Skeleton,
		StatusBadge,
		Table,
		formatDateTime,
		formatRelative,
		toast,
		type Column
	} from '$lib/ui';
	import { COMPATIBILITY, agentLabel } from './model';

	let { env, now }: { env: Environment; now?: Date } = $props();

	const qc = useQueryClient();
	const agents = createQuery(() => environmentAgentsQuery(env.id));
	const rows = $derived(
		[...(agents.data ?? [])].sort(
			(a, b) =>
				Number(b.status === 'active') - Number(a.status === 'active') ||
				b.id.localeCompare(a.id)
		)
	);

	let target = $state<Agent | null>(null);
	let rotateOpen = $state(false);
	let removeOpen = $state(false);

	async function refresh() {
		await Promise.all([
			qc.invalidateQueries({ queryKey: liveKeys.list('agents') }),
			qc.invalidateQueries({ queryKey: liveKeys.item('environments', env.id) }),
			qc.invalidateQueries({ queryKey: liveKeys.list('environments') })
		]);
	}

	async function rotate() {
		if (!target) return;
		const out = await unwrap(
			api.POST('/api/v1/agents/{agentId}/credential-rotations', {
				params: {
					path: { agentId: target.id },
					header: { 'Idempotency-Key': crypto.randomUUID() }
				}
			})
		);
		await refresh();
		toast.success(`Rotated the credential of ${agentLabel(target)}`, {
			body:
				out.state === 'completed'
					? 'The agent stored the new credential; the old one is revoked.'
					: 'The agent picks up the new credential when it next connects; the old one works until then.'
		});
	}

	async function remove() {
		if (!target) return;
		await unwrapEmpty(
			api.DELETE('/api/v1/agents/{agentId}', {
				params: {
					path: { agentId: target.id },
					header: { 'If-Match': `"${target.revision ?? 0}"` }
				}
			})
		);
		await refresh();
		toast.success(`Removed agent ${agentLabel(target)}`, {
			body: `${env.name} stays offline until you re-attach it with a new agent.`
		});
	}

	const columns: Column<Agent>[] = [
		{ id: 'agent', header: 'Agent', cell: agentCell, stack: 'title' },
		{ id: 'status', header: 'Status', cell: statusCell, width: '150px', stack: 'status' },
		{ id: 'version', header: 'Version', cell: versionCell, width: '200px' },
		{ id: 'seen', header: 'Last seen', cell: seenCell, width: '150px' },
		{
			id: 'actions',
			header: 'Actions',
			cell: actionsCell,
			hideHeader: true,
			align: 'end',
			width: '290px',
			stack: 'actions'
		}
	];
</script>

{#snippet agentCell(a: Agent)}
	<div class="agent">
		<span class="strong">{agentLabel(a)}</span>
		<span class="muted small mono">{a.id}</span>
		{#if a.rotationPending}<span class="small warn">New credential waiting for the agent</span
			>{/if}
	</div>
{/snippet}
{#snippet statusCell(a: Agent)}
	{#if a.status === 'revoked'}
		<StatusBadge status="cancelled" label="Removed" title={a.revokedReason} />
	{:else}
		<StatusBadge
			status={a.connected ? 'online' : 'offline'}
			label={a.connected ? 'Connected' : 'Not connected'}
		/>
	{/if}
{/snippet}
{#snippet versionCell(a: Agent)}
	<div class="version">
		<span class="num">{a.version ?? '—'}</span>
		{#if a.status === 'active' && a.compatibility && a.compatibility !== 'current'}
			<Badge tone={COMPATIBILITY[a.compatibility]?.tone ?? 'warn'} dot
				>{COMPATIBILITY[a.compatibility]?.label}</Badge
			>
		{/if}
	</div>
{/snippet}
{#snippet seenCell(a: Agent)}
	{#if a.revokedAt}
		<time datetime={a.revokedAt} title={formatDateTime(a.revokedAt)}
			>Removed {formatRelative(a.revokedAt, now)}</time
		>
	{:else if a.lastSeenAt}
		<time datetime={a.lastSeenAt} title={formatDateTime(a.lastSeenAt)}
			>{formatRelative(a.lastSeenAt, now)}</time
		>
	{:else}<span class="muted">—</span>{/if}
{/snippet}
{#snippet actionsCell(a: Agent)}
	{#if a.status === 'active'}
		<div class="actions">
			{#if a.actions.includes('agent.manage')}
				<Button
					size="sm"
					icon={KeyRound}
					onclick={() => {
						target = a;
						rotateOpen = true;
					}}>Rotate credential</Button
				>
			{/if}
			{#if a.actions.includes('agent.remove')}
				<Button
					size="sm"
					variant="danger-soft"
					icon={Trash2}
					onclick={() => {
						target = a;
						removeOpen = true;
					}}>Remove</Button
				>
			{/if}
		</div>
	{/if}
{/snippet}

<Card title="Agents" padding="none" id="agents">
	{#if agents.isPending}
		<div class="pad" aria-busy="true"><Skeleton lines={3} height="20px" /></div>
	{:else if agents.isError}
		<div class="pad">
			<ErrorState
				error={agents.error}
				title="The agents could not be loaded."
				onretry={() => agents.refetch()}
				compact
			/>
		</div>
	{:else}
		<Table label="Agents of {env.name}" {rows} {columns} rowKey={(a) => a.id} manualSort>
			{#snippet empty()}
				<EmptyState
					title="No agent is attached."
					description="Re-attach {env.name} by enrolling an agent on its Docker Engine."
					level={3}
					compact
				>
					{#snippet actions()}
						<Button href={routes.addEnvironment(env.id)}>Re-attach</Button>
					{/snippet}
				</EmptyState>
			{/snippet}
		</Table>
	{/if}
</Card>

<ConfirmDialog
	bind:open={rotateOpen}
	title="Rotate agent credential"
	message="Docker Manager issues a new credential to {target ? agentLabel(target) : 'the agent'}."
	consequences={[
		'The agent stores the new credential and confirms it; then the old one is revoked.',
		'If the agent is offline, it receives the new credential when it reconnects. The old one keeps working until then.'
	]}
	confirmLabel="Rotate credential"
	onconfirm={rotate}
/>

<DestructiveConfirm
	bind:open={removeOpen}
	title="Remove agent"
	consequences={[
		`Revokes the agent's credential and closes its connection.`,
		`${env.name} stays offline and detached: jobs cannot run there until a new agent re-attaches it.`,
		'Nothing on the host changes: containers keep running. Stop and delete the agent container on the host yourself.'
	]}
	affected={target
		? [
				{
					label: agentLabel(target),
					detail: target.version ? `agent ${target.version}` : 'agent'
				}
			]
		: []}
	confirmText={env.name}
	confirmLabel="Remove agent"
	onconfirm={remove}
/>

<style>
	.agent,
	.version {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.version {
		flex-direction: row;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
	}

	.strong {
		color: var(--text-strong);
		font-weight: var(--weight-medium);
	}

	.small {
		font-size: var(--text-caption);
		line-height: var(--leading-caption);
	}

	.warn {
		color: var(--warn);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: var(--space-2);
	}

	.pad {
		padding: var(--space-4) var(--space-5) var(--space-5);
	}
</style>
