<script lang="ts">
	// Backup sets (#10): one row per run of a policy with its state
	// (partial when any member failed or is missing, never complete) and
	// every member's own snapshot time. Partial and failed sets offer to
	// retry only the members that did not complete.
	import { useQueryClient } from '@tanstack/svelte-query';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import { api, unwrap } from '$lib/api/client';
	import { routes } from '$lib/routes';
	import { Badge, Button, Table, formatDateTime, toast, type Column } from '$lib/ui';
	import { newIdempotencyKey } from '$lib/features/common/data';
	import { actionError } from '$lib/features/common/errors';
	import NameCell from '$lib/features/common/NameCell.svelte';
	import SetMembers from './SetMembers.svelte';
	import { incompleteMembers, setState, type BackupSet } from './model';

	type Row = BackupSet & { policyId?: string; policyName?: string };

	interface Props {
		sets: Row[];
		label: string;
		/** Policy IDs the caller may run (retry). */
		canRetry?: (policyId: string) => boolean;
		environmentName?: (id: string) => string;
		showPolicy?: boolean;
	}

	let {
		sets,
		label,
		canRetry = () => false,
		environmentName,
		showPolicy = true
	}: Props = $props();
	const qc = useQueryClient();
	let retrying = $state<string | null>(null);

	async function retry(s: Row) {
		if (!s.policyId) return;
		retrying = s.id;
		try {
			await unwrap(
				api.POST('/api/v1/backup-policies/{policyId}/runs', {
					params: {
						path: { policyId: s.policyId },
						header: { 'Idempotency-Key': newIdempotencyKey() }
					},
					body: { retrySetId: s.id }
				})
			);
			toast.success(
				`Retrying ${incompleteMembers(s).length} of ${s.members.length} members`,
				{
					body: 'Only the members that did not complete run again; the set keeps its ID.'
				}
			);
			await qc.invalidateQueries({ queryKey: ['policies'] });
			await qc.invalidateQueries({ queryKey: ['backups'] });
		} catch (e) {
			toast.error('The retry did not start', {
				body: actionError(e, {
					nothing_to_retry: 'Every member of this set already completed.'
				})
			});
		} finally {
			retrying = null;
		}
	}

	const columns = $derived<Column<Row>[]>([
		{
			id: 'started',
			header: 'Started',
			cell: startedCell,
			sortValue: (s) => s.startedAt,
			width: '200px',
			stack: 'title'
		},
		{ id: 'state', header: 'State', cell: stateCell, width: '120px', stack: 'status' },
		{ id: 'members', header: 'Members and snapshot times', cell: membersCell },
		{
			id: 'actions',
			header: 'Actions',
			hideHeader: true,
			cell: actionsCell,
			width: '140px',
			stack: 'actions'
		}
	]);
</script>

{#snippet startedCell(s: Row)}
	<NameCell
		name={formatDateTime(s.startedAt)}
		href={showPolicy && s.policyId ? routes.backupPolicy(s.policyId) : undefined}
		sub={[
			showPolicy ? s.policyName : undefined,
			s.origin === 'scheduled'
				? 'Scheduled'
				: s.origin === 'api_token'
					? 'API token'
					: 'Started by hand'
		]
			.filter(Boolean)
			.join(', ')}
	/>
{/snippet}
{#snippet stateCell(s: Row)}
	{@const st = setState(s.state)}
	<Badge tone={st.tone} dot>{st.label}</Badge>
{/snippet}
{#snippet membersCell(s: Row)}<SetMembers members={s.members} {environmentName} />{/snippet}
{#snippet actionsCell(s: Row)}
	{#if (s.state === 'partial' || s.state === 'failed') && s.policyId && canRetry(s.policyId)}
		<Button size="sm" icon={RotateCcw} loading={retrying === s.id} onclick={() => retry(s)}
			>Retry missing</Button
		>
	{/if}
{/snippet}

<Table
	{label}
	rows={sets}
	{columns}
	rowKey={(s) => s.id}
	sort={{ column: 'started', direction: 'desc' }}
/>
